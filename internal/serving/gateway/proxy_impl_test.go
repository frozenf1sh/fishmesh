package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

// TestProxyPreservesPathAndAddsRequestID 验证代理的两个基本行为：
//  1. 请求路径原样透传给上游（用假上游断言收到的路径是 /v1/models）；
//  2. 客户端未提供 X-Request-ID 时自动生成并注入上游请求。
//
// 用 httptest.NewServer 起一个假上游，因此测试不依赖真实 vLLM 服务，
// 在 CI（GitHub Actions/act）中也可以独立运行。
func TestProxyPreservesPathAndAddsRequestID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if request.Header.Get("X-Request-ID") == "" {
			t.Fatal("expected generated request ID")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	server, err := newTestServer(t, testRuntimeConfig{UpstreamURL: upstream.URL, RequestTimeout: defaultRequestTimeout})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway/v1/models", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("response should expose request ID")
	}
}

func TestAdmissionRejectsWithoutQueueing(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstream.URL, RequestTimeout: time.Second,
		MaxInflightRequests: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		server.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`))))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach upstream")
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`))))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" || response.Header().Get("X-FishMesh-Route-Reason") != string(routing.ReasonAdmissionCapacity) {
		t.Fatalf("unexpected admission response: status=%d headers=%v", response.Code, response.Header())
	}
	close(release)
	<-firstDone
	if server.admission.Inflight() != 0 {
		t.Fatalf("admission permit leaked: %d", server.admission.Inflight())
	}
}

func TestClientCancellationDoesNotOpenBackendCircuit(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(upstreamStarted)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-request.Context().Done():
				close(upstreamCanceled)
				return
			case <-ticker.C:
				if _, err := writer.Write([]byte(": keepalive\n\n")); err != nil {
					close(upstreamCanceled)
					return
				}
				writer.(http.Flusher).Flush()
			}
		}
	}))
	defer upstream.Close()
	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstream.URL, RoutingMode: routing.ModeSessionKey,
		BackendEndpoints: []string{upstream.URL}, SessionKeyTTL: time.Minute, SessionKeyMaxEntries: 10,
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`))).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler().ServeHTTP(httptest.NewRecorder(), request)
	}()
	<-upstreamStarted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not stop the canceled request")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not propagate upstream")
	}
	if server.admission.Inflight() != 0 {
		t.Fatalf("cancellation leaked admission state: %d", server.admission.Inflight())
	}
}

func TestTransportCircuitSpillsWithoutChangingSessionKey(t *testing.T) {
	var fail [2]atomic.Bool
	var hits [2]atomic.Int64
	upstreams := make([]*httptest.Server, 2)
	for index := range upstreams {
		index := index
		upstreams[index] = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			hits[index].Add(1)
			if fail[index].Load() {
				hijacker := writer.(http.Hijacker)
				connection, _, hijackErr := hijacker.Hijack()
				if hijackErr == nil {
					_ = connection.Close()
				}
				return
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n"))
		}))
		defer upstreams[index].Close()
	}
	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstreams[0].URL, RoutingMode: routing.ModeSessionKey,
		BackendEndpoints: []string{upstreams[0].URL, upstreams[1].URL},
		SessionKeyTTL:    time.Minute, SessionKeyMaxEntries: 10, RequestTimeout: time.Second,
		CircuitEWMAAlpha: 0.5, CircuitErrorThreshold: 0.6, CircuitMinimumRequests: 3, CircuitOpenDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := server.requestPath.Select(context.Background(), requestpath.Request{RoutingKey: "failing-session"})
	if err != nil {
		t.Fatal(err)
	}
	preferredID := probe.Decision.Backend.ID
	probe.Complete(requestpath.OutcomeClientCanceled)
	preferredIndex := 0
	if preferredID == "backend-1" {
		preferredIndex = 1
	}
	fail[preferredIndex].Store(true)
	for requestNumber := 0; requestNumber < 3; requestNumber++ {
		request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		request.Header.Set("X-FishMesh-Session-Key", "failing-session")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadGateway {
			t.Fatalf("failure %d status = %d", requestNumber, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("X-FishMesh-Session-Key", "failing-session")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-FishMesh-Spillover-Reason") != string(routing.ReasonCircuitOpen) {
		t.Fatalf("circuit did not spill: status=%d headers=%v", response.Code, response.Header())
	}
	if response.Header().Get("X-FishMesh-Preferred-Backend-ID") != string(preferredID) || response.Header().Get("X-FishMesh-Backend-ID") == string(preferredID) {
		t.Fatalf("circuit rewrote preference: %v", response.Header())
	}
	if hits[preferredIndex].Load() != 3 {
		t.Fatalf("open circuit still received traffic: hits=%d", hits[preferredIndex].Load())
	}
}

func TestStreamingFailureIsNotRetriedAfterHeaders(t *testing.T) {
	var attempts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Content-Length", "100")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: partial\n\n"))
	}))
	defer upstream.Close()
	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstream.URL, RoutingMode: routing.ModeSessionKey,
		BackendEndpoints: []string{upstream.URL}, SessionKeyTTL: time.Minute, SessionKeyMaxEntries: 10,
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`))))
	if response.Code != http.StatusOK || attempts.Load() != 1 {
		t.Fatalf("stream was retried or status changed: status=%d attempts=%d", response.Code, attempts.Load())
	}
	metricsResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "http://gateway/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), "fishmesh_gateway_upstream_stream_errors_total 1") {
		t.Fatalf("stream failure metric missing:\n%s", metricsResponse.Body.String())
	}
}

func TestPrefixHashRoutingKeepsPrefixOnOneEndpoint(t *testing.T) {
	var hits [2]int
	upstreams := make([]*httptest.Server, 2)
	for index := range upstreams {
		index := index
		upstreams[index] = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits[index]++
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n"))
		}))
		defer upstreams[index].Close()
	}

	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstreams[0].URL,
		RoutingMode: routing.ModeSessionKey, BackendEndpoints: []string{upstreams[0].URL, upstreams[1].URL},
		KeepAlive: true, RequestTimeout: defaultRequestTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		request.Header.Set("X-FishMesh-Session-Key", "same-prefix")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("X-FishMesh-Routing-Mode") != string(routing.ModeSessionKey) {
			t.Fatalf("unexpected response: status=%d headers=%v", response.Code, response.Header())
		}
	}
	if hits[0]+hits[1] != 3 {
		t.Fatalf("expected three upstream requests, got %v", hits)
	}
	if hits[0] != 0 && hits[1] != 0 {
		t.Fatalf("same prefix was routed to both endpoints: %v", hits)
	}
}

func TestLoadAwareRoutingPrefersIdleEndpoint(t *testing.T) {
	var hits [2]int
	upstreams := make([]*httptest.Server, 2)
	for index := range upstreams {
		index := index
		upstreams[index] = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits[index]++
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n"))
		}))
		defer upstreams[index].Close()
	}

	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstreams[0].URL,
		RoutingMode: routing.ModeLoadBalanced, BackendEndpoints: []string{upstreams[0].URL, upstreams[1].URL},
		KeepAlive: true, RequestTimeout: defaultRequestTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("X-FishMesh-Session-Key", "same-prefix")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected response status: %d", response.Code)
	}
	if hits[0]+hits[1] != 1 {
		t.Fatalf("expected one upstream request, got %v", hits)
	}
}

func TestSessionKeySpilloverIsExplainable(t *testing.T) {
	upstreams := make([]*httptest.Server, 2)
	for index := range upstreams {
		upstreams[index] = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n"))
		}))
		defer upstreams[index].Close()
	}

	server, err := newTestServer(t, testRuntimeConfig{
		UpstreamURL: upstreams[0].URL,
		RoutingMode: routing.ModeSessionKey, BackendEndpoints: []string{upstreams[0].URL, upstreams[1].URL},
		SessionKeyTTL: time.Minute, SessionKeyMaxEntries: 100, SessionKeyInflightDelta: 1,
		KeepAlive: true, RequestTimeout: defaultRequestTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := server.requestPath.Select(context.Background(), requestpath.Request{RoutingKey: "hot-session"})
	if err != nil {
		t.Fatal(err)
	}
	preferredID := first.Decision.Backend.ID
	second, err := server.requestPath.Select(context.Background(), requestpath.Request{RoutingKey: "hot-session"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Complete(requestpath.OutcomeClientCanceled)
	defer second.Complete(requestpath.OutcomeClientCanceled)

	request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("X-FishMesh-Session-Key", "hot-session")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("X-FishMesh-Route-Reason") != string(routing.ReasonSessionKeySpillover) || response.Header().Get("X-FishMesh-Spillover-Reason") != string(routing.ReasonLocalInflight) {
		t.Fatalf("missing spillover provenance: %v", response.Header())
	}
	if response.Header().Get("X-FishMesh-Preferred-Backend-ID") != string(preferredID) || response.Header().Get("X-FishMesh-Backend-ID") == string(preferredID) {
		t.Fatalf("preferred/selected identities are wrong: %v", response.Header())
	}
	if response.Header().Get("X-FishMesh-Policy") != string(routing.PolicySessionKeyV1) {
		t.Fatalf("policy = %q", response.Header().Get("X-FishMesh-Policy"))
	}

	metricsResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "http://gateway/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), `fishmesh_gateway_session_key_spillovers_total{reason="local-inflight"} 1`) {
		t.Fatalf("spillover metric missing:\n%s", metricsResponse.Body.String())
	}
}

// TestSSEDetectorHandlesChunkBoundaries 专门验证 SSE detector 在
// chunk 边界下的正确性（真实网络里 TCP 会任意切分数据）：
//  1. 终止事件 "[DONE]" 被切成两半时不能误判为首个事件；
//  2. 完整的终止事件也不能算；
//  3. 第一个真实 data 事件要能识别（无论从哪个位置开始）；
//  4. 之后的 data 事件不能再重复触发（TTFT 只测一次）。
func TestSSEDetectorHandlesChunkBoundaries(t *testing.T) {
	detector := &sseDetector{}
	if detector.Feed([]byte("data: [DO")) {
		t.Fatal("partial terminal event must not count")
	}
	if detector.Feed([]byte("NE]\n")) {
		t.Fatal("terminal event must not count")
	}
	if !detector.Feed([]byte("data: {\"choices\":[] }\n")) {
		t.Fatal("first data event should count")
	}
	if detector.Feed([]byte("data: {\"more\":true}\n")) {
		t.Fatal("only the first event should count")
	}
}
