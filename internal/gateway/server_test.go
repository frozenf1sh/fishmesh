package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
