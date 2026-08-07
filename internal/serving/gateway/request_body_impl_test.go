package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

type recordingPath struct {
	lease    requestpath.Lease
	request  requestpath.Request
	selected bool
}

func (p *recordingPath) Select(_ context.Context, request requestpath.Request) (requestpath.Lease, error) {
	p.request, p.selected = request, true
	return p.lease, nil
}

func (p *recordingPath) State(context.Context) requestpath.State { return requestpath.State{} }
func (*recordingPath) Ready() bool                               { return true }
func (*recordingPath) Close() error                              { return nil }

type gatewayKVCache struct {
	queried bool
}

func (c *gatewayKVCache) Lookup(context.Context, kvcache.Query) (kvcache.Snapshot, error) {
	c.queried = true
	// 零 Snapshot 故意代表 lookup 没有可信 Match，以验证真实请求会走显式 load-aware 降级。
	return kvcache.Snapshot{}, nil
}

func (*gatewayKVCache) Reconcile(context.Context, []kvcache.Instance) error { return nil }
func (*gatewayKVCache) State() kvcache.StateSnapshot                        { return kvcache.StateSnapshot{} }
func (*gatewayKVCache) Close() error                                        { return nil }

func TestProxyBoundsAndReplaysBodyWhileExposingExactDegradation(t *testing.T) {
	requestBody := []byte(`{"model":"qwen","messages":[],"stream":true}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil || !bytes.Equal(body, requestBody) {
			t.Fatalf("upstream body = %q, err = %v", body, err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	path := &recordingPath{lease: requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a", URL: upstream.URL}, PreferredBackendID: "backend-a", Reason: routing.ReasonExactSignalUnavailable, Policy: routing.PolicyExactLoadFallbackV1},
		State:    requestpath.State{Exact: requestpath.ExactMatchUnavailable},
	}}
	server := newGatewayWithPath(t, path, routing.ModeExactCacheLoad, int64(len(requestBody)))

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader(requestBody)))
	if response.Code != http.StatusOK || response.Body.String() != "data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if !path.selected || path.request.Route != "/v1/chat/completions" || !bytes.Equal(path.request.Body, requestBody) {
		t.Fatalf("requestpath input = %+v", path.request)
	}
	if response.Header().Get(headerRouteReason) != string(routing.ReasonExactSignalUnavailable) || response.Header().Get(headerExactStatus) != string(requestpath.ExactMatchUnavailable) || response.Header().Get(headerPolicy) != string(routing.PolicyExactLoadFallbackV1) {
		t.Fatalf("decision headers = %v", response.Header())
	}
	if response.Header().Get(headerCachedPrefixTokens) != "0" {
		t.Fatalf("unknown cache must not publish a cached prefix: %v", response.Header())
	}
}

func TestProxyRejectsOversizedBodyBeforeSelection(t *testing.T) {
	path := &recordingPath{}
	server := newGatewayWithPath(t, path, routing.ModeExactCacheLoad, 4)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewBufferString("12345")))
	if response.Code != http.StatusRequestEntityTooLarge || path.selected {
		t.Fatalf("status/selection = %d/%t", response.Code, path.selected)
	}
}

func TestExactGatewayClosesRenderLookupSelectionAndSSELoop(t *testing.T) {
	var rendered bool
	renderer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rendered = request.URL.Path == "/v1/chat/completions/render"
		_, _ = writer.Write([]byte(`{"model":"qwen","token_ids":[1,2,3]}`))
	}))
	defer renderer.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	tokenizer, err := tokenization.NewVLLMRenderer(tokenization.DefaultConfig(renderer.URL, "qwen"), tokenization.Dependencies{HTTPClient: renderer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cache := &gatewayKVCache{}
	backendValue := backend.Backend{ID: "backend-a", URL: upstream.URL}
	resolver, err := discovery.NewStatic([]backend.Backend{backendValue})
	if err != nil {
		t.Fatal(err)
	}
	breaker, err := circuit.New(circuit.Config{EWMAAlpha: 1, ErrorThreshold: 0.5, MinimumRequests: 1, OpenDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	path, err := requestpath.New(requestpath.Config{Service: backendValue}, requestpath.Dependencies{
		Resolver: resolver, Strategy: routing.NewExactCacheLoad(), Circuits: breaker, Tokenizer: tokenizer, KVCache: cache, KVReconcile: func(context.Context, []backend.Backend) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()
	defer resolver.Close()
	server := newGatewayWithPath(t, path, routing.ModeExactCacheLoad, 1024)

	requestBody := []byte(`{"model":"qwen","messages":[],"stream":true}`)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader(requestBody)))
	if !rendered || !cache.queried || response.Code != http.StatusOK || response.Body.String() != "data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n" {
		t.Fatalf("rendered=%t lookup=%t status=%d body=%q", rendered, cache.queried, response.Code, response.Body.String())
	}
	if response.Header().Get(headerRouteReason) != string(routing.ReasonExactSignalUnavailable) || response.Header().Get(headerExactStatus) != string(requestpath.ExactMatchUnavailable) {
		t.Fatalf("degradation headers = %v", response.Header())
	}
}

func newGatewayWithPath(t testing.TB, path requestpath.Path, mode routing.Mode, bodyLimit int64) *Server {
	t.Helper()
	admissionController, err := admission.New(admission.Config{MaxInflight: 1})
	if err != nil {
		t.Fatal(err)
	}
	pool := transport.New(transport.Config{RequestTimeout: time.Second, MaxConnsPerHost: 1})
	server, err := New(Config{RoutingMode: mode, RequestTimeout: time.Second, MaxRequestBodyBytes: bodyLimit}, Dependencies{
		RequestPath: path, Admission: admissionController, Transport: pool, Metrics: NewMetrics(), Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return server
}
