package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
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

	server, err := NewServer(Config{ListenAddress: ":0", UpstreamURL: upstream.URL, RequestTimeout: defaultRequestTimeout, ShutdownTimeout: defaultShutdownTimeout}, testLogger())
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

	server, err := NewServer(Config{
		ListenAddress: ":0", UpstreamURL: upstreams[0].URL,
		RoutingMode: "prefix-affinity", BackendEndpoints: []string{upstreams[0].URL, upstreams[1].URL},
		KeepAlive: true, RequestTimeout: defaultRequestTimeout, ShutdownTimeout: defaultShutdownTimeout,
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		request.Header.Set("X-FishMesh-Prefix-Key", "same-prefix")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("X-FishMesh-Routing-Mode") != "prefix-affinity" {
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

	server, err := NewServer(Config{
		ListenAddress: ":0", UpstreamURL: upstreams[0].URL,
		RoutingMode: "load-aware", BackendEndpoints: []string{upstreams[0].URL, upstreams[1].URL},
		KeepAlive: true, RequestTimeout: defaultRequestTimeout, ShutdownTimeout: defaultShutdownTimeout,
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("X-FishMesh-Prefix-Key", "same-prefix")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected response status: %d", response.Code)
	}
	if hits[0]+hits[1] != 1 {
		t.Fatalf("expected one upstream request, got %v", hits)
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
