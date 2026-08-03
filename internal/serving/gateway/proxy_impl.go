package gateway

import (
	"bufio"
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

type proxyResult struct {
	status  int
	outcome requestpath.Outcome
}

type bodyCopyResult struct {
	upstreamError   error
	downstreamError error
}

type sseDetector struct {
	line    []byte
	emitted bool
}

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

	// 2. 从协议无关的 RequestPath 获取必须完成的 backend lease。
	lease, err := s.requestPath.Select(request.Context(), requestpath.Request{RoutingKey: request.Header.Get(headerPrefixKey)})
	if err != nil {
		status = http.StatusServiceUnavailable
		http.Error(writer, "request path unavailable", status)
		return
	}
	s.metrics.observeSelection(s.config.RoutingMode, lease)

	// 3. 转发一次 upstream 流；response headers 发出后绝不重试。
	result := s.proxyUpstream(writer, request, requestID, lease, startedAt)
	status = result.status

	// 4. 将精确 outcome 交还 lease，统一释放 in-flight 并更新 circuit。
	s.metrics.observeCompletion(lease, lease.Complete(result.outcome))
}

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
	s.writeResponseHeaders(writer, requestID, upstream, lease)
	writer.WriteHeader(response.StatusCode)
	copyResult := s.copyResponse(writer, response.Body, startedAt)
	return s.classifyStreamResult(request, requestID, lease, response.StatusCode, copyResult)
}

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

func (s *Server) writeResponseHeaders(writer http.ResponseWriter, requestID string, upstream *url.URL, lease requestpath.Lease) {
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
	writer.Header().Set(headerUpstream, upstream.Host)
}

func (s *Server) copyResponse(writer http.ResponseWriter, body io.Reader, startedAt time.Time) bodyCopyResult {
	detector := &sseDetector{}
	firstEventRecorded := false
	return copyResponseBody(writer, body, func() {
		if !firstEventRecorded {
			firstEventRecorded = true
			s.metrics.ttftSeconds.Observe(time.Since(startedAt).Seconds())
		}
	}, detector)
}

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

func copyResponseHeaders(destination, source http.Header) {
	cleaned := source.Clone()
	removeHopByHopHeaders(cleaned)
	for key, values := range cleaned {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

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

func joinURLPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		return requestPath
	}
	return path.Join(basePath, requestPath)
}

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
