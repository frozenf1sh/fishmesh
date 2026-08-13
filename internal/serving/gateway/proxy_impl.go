package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

var errRequestBodyTooLarge = errors.New("request body exceeds gateway limit")

// proxyResult 保存一次上游代理的最终 HTTP 状态和 requestpath outcome。
//
// status 是 Gateway 对下游已经写出或准备写出的状态码；outcome 则描述 backend
// 生命周期对 circuit 和 lease 结算的语义。两者不能合并成一个 bool：例如 response
// headers 已经写出后上游断流时，下游仍可能看到 200，但 lease 必须记录
// OutcomeUpstreamStream；客户端主动取消时也不应把 backend 误判为传输失败。
type proxyResult struct {
	status  int
	outcome requestpath.Outcome
}

// bodyCopyResult 区分读取 upstream body 与写入 downstream 的失败。
//
// 读取错误意味着上游响应流在 headers 之后中断，写入错误通常意味着客户端已经
// 断开或 ResponseWriter 不再可用。两类错误都不能在 headers 发出后触发 retry，
// 但必须保持独立，以便 requestpath 决定是否更新 backend circuit。
type bodyCopyResult struct {
	upstreamError   error
	downstreamError error
}

// sseDetector 在不解析完整 SSE JSON 的前提下识别首个非终止 data 事件。
//
// Gateway 只需要首事件时间来计算 TTFT，不需要理解模型输出的 JSON schema。由于
// TCP/HTTP read 不保证以换行或 SSE event 为边界，line 保存跨 chunk 的未完成行，
// emitted 保证一次响应最多触发一次首事件回调。
type sseDetector struct {
	line    []byte
	emitted bool
}

// proxy 按固定顺序完成一次 HTTP/SSE 请求：准入、请求体、选路、转发和结算。
//
// 这里刻意只保留编排步骤，不解析 JSON、不实现路由公式，也不维护 KV 状态。任何
// 选路失败都在 lease 建立前返回；一旦 lease 建立，所有正常和异常的 upstream 结果
// 都必须经过 Complete，保证 local in-flight、prediction ticket 和 circuit 结算不会
// 因早退而泄漏。
func (s *Server) proxy(writer http.ResponseWriter, request *http.Request) {
	startedAt := time.Now()
	status := http.StatusBadGateway
	defer func() { s.metrics.observeRequest(request.Method, status, time.Since(startedAt)) }()

	// 1. 非阻塞获取进程准入名额，容量耗尽时立即返回，不建立隐式队列。
	requestID := requestID(request)
	permit, err := s.admission.TryAcquire()
	if err != nil {
		status = s.rejectAdmission(writer, requestID, err)
		return
	}
	defer permit.Release()
	s.metrics.inflight.Inc()
	defer s.metrics.inflight.Dec()

	// 2. 一次性有界读取 body；同一份字节既是 Render 输入，也会重放给实际 upstream。
	//    先读完再选路有两个原因：KV-aware Render 需要完整原始请求，限制也必须在任何
	//    外部调用之前生效；选路成功后不能重新从已消费的 request.Body 读取空内容。
	body, err := readBoundedBody(request, s.config.MaxRequestBodyBytes)
	if err != nil {
		status = s.rejectRequestBody(writer, requestID, err)
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))

	// 3. 从协议无关的 RequestPath 获取必须完成的 backend lease。
	//    Gateway 只传递路径、routing hint 和原始 body；EndpointSlice、KVEvents、
	//    freshness 与 fallback 都由 requestpath 解释，避免 delivery 层复制第二套事实。
	lease, err := s.requestPath.Select(request.Context(), requestpath.Request{
		RoutingKey: request.Header.Get(headerSessionKey), Route: request.URL.Path, Body: body,
	})
	if err != nil {
		status = http.StatusServiceUnavailable
		http.Error(writer, "request path unavailable", status)
		return
	}
	s.metrics.observeSelection(s.config.RoutingMode, lease)
	s.writeDecisionHeaders(writer, requestID, lease)

	// 4. 转发一次 upstream 流；response headers 发出后绝不重试。
	//    decision headers 在这里之前写入，因此下游即使只收到部分流，也能知道本次
	//    选择的 backend、policy、reason 和 KV-aware 状态；这些 headers 不代表流一定完成。
	result := s.proxyUpstream(writer, request, requestID, lease, startedAt)
	status = result.status

	// 5. 将精确 outcome 交还 lease，统一释放 in-flight 并更新 circuit。
	//    Complete 自身必须幂等，但正常路径仍只调用一次；metrics 使用返回的结算结果
	//    更新 circuit label，从而不会把已经离开 membership 的 backend 重新写回指标。
	s.metrics.observeCompletion(lease, lease.Complete(result.outcome))
}

// proxyUpstream 创建一次有超时、可取消的 upstream 请求，并把响应交给 SSE 透传路径。
//
// URL 拼接、hop-by-hop header 清理和连接复用 header 都属于 delivery adapter 的协议
// 翻译。建立连接前的错误仍可返回明确的 502/504；只有 response headers 已经写出后，
// 才进入不可重试的 stream outcome 路径。
func (s *Server) proxyUpstream(writer http.ResponseWriter, request *http.Request, requestID string, lease requestpath.Lease, startedAt time.Time) proxyResult {
	upstream, err := url.Parse(lease.Decision.Backend.URL)
	if err != nil {
		http.Error(writer, "invalid upstream request", http.StatusBadRequest)
		return proxyResult{status: http.StatusBadRequest, outcome: requestpath.OutcomeInvalidClientInput}
	}
	upstreamRequest, cancel := s.newUpstreamRequest(request, requestID, upstream)
	defer cancel()
	response, err := s.transport.ClientFor(lease.Decision.Backend).Do(upstreamRequest)
	if err != nil {
		return s.handleUpstreamError(writer, request, requestID, lease, err)
	}
	defer response.Body.Close()

	copyResponseHeaders(writer.Header(), response.Header)
	s.writeUpstreamHeader(writer, upstream)
	writer.WriteHeader(response.StatusCode)
	copyResult := s.copyResponse(writer, response.Body, lease, startedAt)
	return s.classifyStreamResult(request, requestID, lease, response.StatusCode, copyResult)
}

// handleUpstreamError 将建立连接阶段的错误映射为下游状态和 requestpath outcome。
//
// 必须先检查 request.Context：客户端取消可能以网络错误的形式返回，不能因为底层
// error 不是 context.Canceled 就打开 backend circuit。deadline、传输失败和客户端取消
// 的分类不同，分别影响客户端状态、指标和后续路由。
func (s *Server) handleUpstreamError(writer http.ResponseWriter, request *http.Request, requestID string, lease requestpath.Lease, err error) proxyResult {
	s.metrics.upstreamErrors.Inc()
	s.logger.Warn("upstream request failed", "request_id", requestID, "backend_id", lease.Decision.Backend.ID, "error", err)
	if request.Context().Err() != nil {
		return proxyResult{status: statusClientClosed, outcome: requestpath.OutcomeClientCanceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(writer, "inference upstream timed out", http.StatusGatewayTimeout)
		return proxyResult{status: http.StatusGatewayTimeout, outcome: requestpath.OutcomeDeadlineExceeded}
	}
	http.Error(writer, "inference upstream unavailable", http.StatusBadGateway)
	return proxyResult{status: http.StatusBadGateway, outcome: requestpath.OutcomeTransportFailure}
}

// writeDecisionHeaders 将一次选择的低基数 provenance 写入下游 response headers。
//
// 这些值来自 requestpath 的 typed Decision/State，而不是 Gateway 重新推导的字符串。
// CachedPrefixTokens 即使为零也可能表示真实零命中；KV-aware signal unavailable 则由
// KVStatus 单独表达，调用方不能把两者混为一谈。
func (s *Server) writeDecisionHeaders(writer http.ResponseWriter, requestID string, lease requestpath.Lease) {
	decision := lease.Decision
	writer.Header().Set(headerRequestID, requestID)
	writer.Header().Set(headerRoutingMode, string(s.config.RoutingMode))
	writer.Header().Set(headerRouteReason, string(decision.Reason))
	writer.Header().Set(headerBackendID, string(decision.Backend.ID))
	writer.Header().Set(headerPreferredBackendID, string(decision.PreferredBackendID))
	writer.Header().Set(headerPolicy, string(decision.Policy))
	if decision.SpilloverReason != "" {
		writer.Header().Set(headerSpilloverReason, string(decision.SpilloverReason))
	}
	writer.Header().Set(headerKVStatus, string(lease.State.KV))
	writer.Header().Set(headerCachedPrefixTokens, strconv.Itoa(lease.State.CachedPrefixTokens))
	evidence := lease.State.Estimate
	if evidence.PromptTokens > 0 {
		writer.Header().Set(headerPromptTokens, strconv.Itoa(evidence.PromptTokens))
		writer.Header().Set(headerUncachedTokens, strconv.Itoa(evidence.UncachedTokens))
		writer.Header().Set(headerEstimatedTTFTMS, strconv.FormatFloat(float64(evidence.EstimatedTTFT)/float64(time.Millisecond), 'f', 3, 64))
		writer.Header().Set(headerEstimatorValid, strconv.FormatBool(evidence.Valid))
		writer.Header().Set(headerEstimatorConfidence, string(evidence.Confidence))
		writer.Header().Set(headerEstimatorVersion, evidence.Version)
		writer.Header().Set(headerEstimatorReason, evidence.Reason)
		writer.Header().Set(headerLoadValid, strconv.FormatBool(evidence.LoadValid))
		writer.Header().Set(headerLoadSampleAgeMS, strconv.FormatFloat(float64(evidence.LoadSampleAge)/float64(time.Millisecond), 'f', 3, 64))
		writer.Header().Set(headerQueueDepth, strconv.FormatInt(evidence.QueueDepth, 10))
		writer.Header().Set(headerRunningRequests, strconv.FormatInt(evidence.Running, 10))
		writer.Header().Set(headerLocalDelta, strconv.FormatInt(evidence.LocalDelta, 10))
		writer.Header().Set(headerLocalInflight, strconv.FormatInt(evidence.LocalInflight, 10))
		writer.Header().Set(headerHardOverloadCount, strconv.Itoa(evidence.HardOverloadedCandidates))
	}
}

// writeUpstreamHeader 暴露实际连接到的 upstream host，便于单请求排障。
//
// 该值只来自已选 backend 的 URL，不接受客户端传入的任意 header，也不作为路由输入
// 或 Prometheus label 使用。
func (*Server) writeUpstreamHeader(writer http.ResponseWriter, upstream *url.URL) {
	writer.Header().Set(headerUpstream, upstream.Host)
}

// copyResponse 以流式方式复制上游响应，并在首个有效 SSE data 事件时记录 TTFT。
//
// callback 只会在 detector 首次返回 true 时运行一次。TTFT 的起点是 Gateway 开始
// 处理请求的时间，终点是首个非 [DONE] data 行；没有首事件、只有 keepalive 或只
// 收到 [DONE] 的响应不会伪造首 token 样本。
func (s *Server) copyResponse(writer http.ResponseWriter, body io.Reader, lease requestpath.Lease, startedAt time.Time) bodyCopyResult {
	detector := &sseDetector{}
	firstEventRecorded := false
	return copyResponseBody(writer, body, func() {
		if !firstEventRecorded {
			firstEventRecorded = true
			ttft := time.Since(startedAt)
			s.metrics.ttftSeconds.Observe(ttft.Seconds())
			s.metrics.observePrediction(lease, lease.ObserveFirstToken(ttft))
		}
	}, detector)
}

// classifyStreamResult 将 headers 之后的流结果映射为最终 proxyResult。
//
// 先检查客户端 context 是必要的：下游断开时，ResponseWriter 的写错误和 upstream
// 取消可能同时发生，但对路由而言应优先归类为 client-canceled，而不是把正常的
// 用户取消当作 backend 故障。成功只表示流完整读到 EOF，不表示模型业务内容正确。
func (s *Server) classifyStreamResult(request *http.Request, requestID string, lease requestpath.Lease, status int, result bodyCopyResult) proxyResult {
	if request.Context().Err() != nil {
		return proxyResult{status: statusClientClosed, outcome: requestpath.OutcomeClientCanceled}
	}
	if result.upstreamError != nil {
		s.metrics.streamErrors.Inc()
		s.logger.Warn("upstream stream failed after response headers",
			"request_id", requestID, "backend_id", lease.Decision.Backend.ID, "error", result.upstreamError)
		return proxyResult{status: status, outcome: requestpath.OutcomeUpstreamStream}
	}
	if result.downstreamError != nil {
		return proxyResult{status: status, outcome: requestpath.OutcomeDownstreamFailure}
	}
	return proxyResult{status: status, outcome: requestpath.OutcomeResponseCompleted}
}

// rejectAdmission 返回准入失败的 HTTP 响应，并只在确实达到容量上限时增加拒绝指标。
//
// admission 是非阻塞的：容量不足不会在 Gateway 内部排队。Retry-After 只对明确的
// capacity error 设置；其他内部准入错误保持 503，避免向客户端承诺可以安全重试。
func (s *Server) rejectAdmission(writer http.ResponseWriter, requestID string, err error) int {
	status := http.StatusServiceUnavailable
	if errors.Is(err, admission.ErrCapacity) {
		status = http.StatusTooManyRequests
		s.metrics.admissionRejections.Inc()
		writer.Header().Set(headerRetryAfter, retryAfterSeconds)
		writer.Header().Set(headerRouteReason, string(routing.ReasonAdmissionCapacity))
	}
	writer.Header().Set(headerRequestID, requestID)
	http.Error(writer, "gateway concurrency limit reached", status)
	return status
}

// rejectRequestBody 在选路前处理请求体读取失败。
//
// 超限返回 413，表示客户端可以缩小输入；读取/连接错误返回 400，因为此时 Gateway
// 尚未取得 backend lease，也没有任何 upstream 请求需要结算。
func (s *Server) rejectRequestBody(writer http.ResponseWriter, requestID string, err error) int {
	writer.Header().Set(headerRequestID, requestID)
	if errors.Is(err, errRequestBodyTooLarge) {
		http.Error(writer, "request body exceeds gateway limit", http.StatusRequestEntityTooLarge)
		return http.StatusRequestEntityTooLarge
	}
	http.Error(writer, "request body unavailable", http.StatusBadRequest)
	return http.StatusBadRequest
}

// newUpstreamRequest 将下游请求复制为一次受控的 upstream 请求。
//
// Clone 保留 method、body、context 和普通请求头，然后只替换已选 backend 所需的
// scheme/host/path。RequestURI 必须清空，因为客户端请求只能发送 origin-form；
// hop-by-hop headers 不能跨代理转发；Keep-Alive 关闭时显式发送 Connection: close，
// 与 transport 的连接复用配置保持一致。
func (s *Server) newUpstreamRequest(request *http.Request, requestID string, upstream *url.URL) (*http.Request, context.CancelFunc) {
	requestContext, cancel := context.WithTimeout(request.Context(), s.config.RequestTimeout)
	upstreamRequest := request.Clone(requestContext)
	upstreamRequest.URL.Scheme = upstream.Scheme
	upstreamRequest.URL.Host = upstream.Host
	upstreamRequest.URL.Path = joinURLPath(upstream.Path, request.URL.Path)
	upstreamRequest.URL.RawPath = ""
	upstreamRequest.Host = upstream.Host
	upstreamRequest.RequestURI = ""
	removeHopByHopHeaders(upstreamRequest.Header)
	upstreamRequest.Header.Set(headerRequestID, requestID)
	if !s.config.KeepAlive {
		upstreamRequest.Header.Set(headerConnection, connectionClose)
	}
	return upstreamRequest, cancel
}

// readBoundedBody 读取最多 maximum+1 字节，用多读一个字节可靠区分“刚好达到上限”和“超过上限”。
//
// Content-Length 只是提前拒绝的快速路径，不能单独依赖它：chunked 请求可能没有长度，
// 也可能存在错误或恶意长度。因此仍需用 LimitReader 实际读取，并由调用方负责关闭
// 原始 request.Body（标准 HTTP server 会在请求结束时处理其生命周期）。
func readBoundedBody(request *http.Request, maximum int64) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
	}
	if request.ContentLength > maximum {
		return nil, errRequestBodyTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, errRequestBodyTooLarge
	}
	return body, nil
}

// copyResponseBody 在固定大小 buffer 上循环读取并即时写出响应。
//
// BufferedWriter 减少小 chunk 的写调用，但每个成功写入的 chunk 都立即 Flush，保证
// SSE 不因 Gateway 缓冲而积累到完整响应才发送。先 Feed detector 再写下游，使首事件
// 观测与实际透传保持同一字节顺序；读取错误和写入错误分别返回，调用方据此分类。
func copyResponseBody(writer http.ResponseWriter, body io.Reader, onFirstEvent func(), detector *sseDetector) bodyCopyResult {
	bufferedWriter := bufio.NewWriterSize(writer, streamBufferBytes)
	defer bufferedWriter.Flush()
	buffer := make([]byte, streamBufferBytes)
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			if detector.Feed(buffer[:read]) {
				onFirstEvent()
			}
			if _, writeErr := bufferedWriter.Write(buffer[:read]); writeErr != nil {
				return bodyCopyResult{downstreamError: writeErr}
			}
			if flusher, ok := writer.(http.Flusher); ok {
				if flushErr := bufferedWriter.Flush(); flushErr != nil {
					return bodyCopyResult{downstreamError: flushErr}
				}
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return bodyCopyResult{}
			}
			return bodyCopyResult{upstreamError: readErr}
		}
	}
}

// copyResponseHeaders 复制端到端响应头，同时移除 HTTP 代理不应转发的 hop-by-hop 头。
//
// 先 Clone 源 header，避免修改 transport 返回的原始 map；使用 Add 保留一个 header
// 的多值语义。Gateway 自己的 decision/upstream headers 在调用方随后写入，避免被
// 上游同名 header 覆盖。
func copyResponseHeaders(destination, source http.Header) {
	cleaned := source.Clone()
	removeHopByHopHeaders(cleaned)
	for key, values := range cleaned {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

// removeHopByHopHeaders 删除 Connection 声明及 RFC 代理级 header。
//
// Connection header 可能列出额外的逐跳 header，因此先按逗号拆分并删除声明项，再
// 删除标准集合。该函数同时用于请求和响应，保证 Gateway 不把连接管理细节泄漏到
// 下一跳。
func removeHopByHopHeaders(headers http.Header) {
	for _, value := range headers.Values(headerConnection) {
		for _, token := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(token))
		}
	}
	for _, header := range []string{headerConnection, headerKeepAlive, headerProxyAuthenticate, headerProxyAuthorization, headerTE, headerTrailer, headerTransferEncoding, headerUpgrade} {
		headers.Del(header)
	}
}

// joinURLPath 组合 backend base path 与客户端请求路径，并保留客户端的 /v1/ 路由语义。
//
// basePath 为空或根路径时直接使用 requestPath，避免 path.Join 把绝对路径处理成意外
// 结果；存在 basePath 时使用 path.Join 消除重复斜杠。query 由 http.Request.URL 单独
// 保留，不在这里拼接。
func joinURLPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		return requestPath
	}
	return path.Join(basePath, requestPath)
}

// requestID 返回客户端提供的 request ID，或生成新的不可预测 ID。
//
// request ID 只用于日志和响应关联，不参与路由、不写入低基数指标 label。随机源失败
// 时使用纳秒时间作为最后兜底，保证故障路径仍能返回可追踪的 ID；该兜底不承担安全
// token 的职责。
func requestID(request *http.Request) string {
	if value := request.Header.Get(headerRequestID); value != "" {
		return value
	}
	bytes := make([]byte, requestIDBytes)
	if _, err := rand.Read(bytes); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(bytes)
}

// Feed 向 SSE detector 输入一个任意边界的字节片段。
//
// 它只识别形如 data: ... 的非终止行，不解析 JSON，也不要求 chunk 从行首开始。
// 换行到来之前的内容保存在 d.line；识别到首个非 [DONE] data 行后设置 emitted，
// 后续事件全部忽略，从而保证 TTFT 和 prediction ticket 都只记录一次。
func (d *sseDetector) Feed(chunk []byte) bool {
	if d.emitted {
		return false
	}
	for _, character := range chunk {
		if character != '\n' {
			d.line = append(d.line, character)
			continue
		}
		line := strings.TrimSpace(string(d.line))
		d.line = d.line[:0]
		if strings.HasPrefix(line, sseDataPrefix) && strings.TrimSpace(strings.TrimPrefix(line, sseDataPrefix)) != sseTerminalData {
			d.emitted = true
			return true
		}
	}
	return false
}
