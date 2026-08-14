package requestpath

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
)

type recordingKVCache struct {
	query kvcache.Query
	err   error
}

func (c *recordingKVCache) Lookup(_ context.Context, query kvcache.Query) (kvcache.Snapshot, error) {
	c.query = query
	return kvcache.Snapshot{}, c.err
}

func (*recordingKVCache) Reconcile(context.Context, []kvcache.Instance) error { return nil }
func (*recordingKVCache) State() kvcache.StateSnapshot                        { return kvcache.StateSnapshot{} }
func (*recordingKVCache) Close() error                                        { return nil }

type failingTokenizer struct{ err error }

func (t failingTokenizer) Tokenize(context.Context, tokenization.Input) (tokenization.Result, error) {
	return tokenization.Result{}, t.err
}

type barrierTokenizer struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (t barrierTokenizer) Tokenize(context.Context, tokenization.Input) (tokenization.Result, error) {
	close(t.started)
	<-t.release
	return tokenization.Result{}, nil
}

func TestKVAwareTokenizationAndKVReconcileOverlap(t *testing.T) {
	resolver := &mutableResolver{err: errors.New("discovery unavailable")}
	tokenizerStarted := make(chan struct{})
	reconcileStarted := make(chan struct{})
	release := make(chan struct{})
	path, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: newKVAwareStrategy(t), Circuits: newTestBreaker(t),
		Tokenizer: barrierTokenizer{started: tokenizerStarted, release: release}, KVCache: &recordingKVCache{},
		KVReconcile: func(context.Context, []backend.Backend) error {
			close(reconcileStarted)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()
	result := make(chan error, 1)
	go func() {
		_, selectErr := path.Select(context.Background(), Request{Route: string(tokenization.RouteChatCompletions), Body: []byte(`{"model":"qwen","messages":[]}`)})
		result <- selectErr
	}()
	for index, started := range []<-chan struct{}{tokenizerStarted, reconcileStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("parallel operation %d did not start", index)
		}
	}
	close(release)
	select {
	case selectErr := <-result:
		if selectErr != nil {
			t.Fatalf("Select() error = %v", selectErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Select() did not finish after parallel operations were released")
	}
}

func TestRoutingInputFromMatchesPreservesValidZeroAndUnknown(t *testing.T) {
	input := routingInputFromMatches(16, map[backend.ID]kvcache.Match{
		"a": {Backend: "a", Valid: true, MatchedTokens: 0},
		"b": {Backend: "b", Valid: false},
	})
	if input.Matches["a"] != (routing.KVMatch{Valid: true, MatchedTokens: 0}) {
		t.Fatalf("valid zero match = %+v", input.Matches["a"])
	}
	if input.Matches["b"].Valid {
		t.Fatalf("unknown match became valid: %+v", input.Matches["b"])
	}
}

func TestKVAwarePathBuildsKVQueryFromReadonlyTokenIDsAndDegradesUnknown(t *testing.T) {
	renderer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions/render" {
			t.Fatalf("render path = %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"model":"qwen","token_ids":[11,12,13],"cache_salt":"tenant-a"}`))
	}))
	defer renderer.Close()
	tokenizer, err := tokenization.NewVLLMRenderer(testTokenizationConfig(renderer.URL, "qwen"), tokenization.Dependencies{HTTPClient: renderer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cache := &recordingKVCache{}
	resolver := &mutableResolver{
		backends: []backend.Backend{{ID: "a", URL: "http://a:8000"}, {ID: "b", URL: "http://b:8000"}},
	}
	path, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: newKVAwareStrategy(t), Circuits: newTestBreaker(t), Tokenizer: tokenizer, KVCache: cache,
		KVReconcile: func(context.Context, []backend.Backend) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()

	lease, err := path.Select(context.Background(), Request{
		RoutingKey: "session", Route: string(tokenization.RouteChatCompletions), Body: []byte(`{"model":"qwen","messages":[],"cache_salt":"tenant-a"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cache.query.Model != "qwen" || cache.query.CacheSalt != "tenant-a" || !reflect.DeepEqual(cache.query.TokenGroups, [][]uint32{{11, 12, 13}}) || !reflect.DeepEqual(cache.query.Backends, []backend.ID{"a", "b"}) {
		t.Fatalf("KV query = %+v", cache.query)
	}
	if lease.Decision.Reason != routing.ReasonKVAwareSignalUnavailable || lease.State.KV != KVMatchUnavailable {
		t.Fatalf("unknown cache decision/state = %+v / %q", lease.Decision, lease.State.KV)
	}
}

func TestKVAwarePathPreservesCanceledTokenization(t *testing.T) {
	resolver := &mutableResolver{backends: []backend.Backend{{ID: "a", URL: "http://a:8000"}}}
	path, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: newKVAwareStrategy(t), Circuits: newTestBreaker(t),
		Tokenizer: failingTokenizer{err: context.Canceled}, KVCache: &recordingKVCache{}, KVReconcile: func(context.Context, []backend.Backend) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()
	_, err = path.Select(context.Background(), Request{Route: string(tokenization.RouteChatCompletions), Body: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Select() error = %v, want context cancellation", err)
	}
}

func TestKVAwarePathDegradesInternalRenderTimeout(t *testing.T) {
	resolver := &mutableResolver{backends: []backend.Backend{{ID: "a", URL: "http://a:8000"}}}
	path, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: newKVAwareStrategy(t), Circuits: newTestBreaker(t),
		Tokenizer: failingTokenizer{err: &tokenization.Error{Code: tokenization.CodeUpstreamUnavailable, Err: context.DeadlineExceeded}},
		KVCache:   &recordingKVCache{}, KVReconcile: func(context.Context, []backend.Backend) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()

	lease, err := path.Select(context.Background(), Request{Route: string(tokenization.RouteChatCompletions), Body: []byte(`{"model":"qwen","messages":[]}`)})
	if err != nil {
		t.Fatalf("internal Render timeout should degrade: %v", err)
	}
	if lease.State.KV != KVTokenizationFailed || lease.Decision.Reason != routing.ReasonKVAwareSignalUnavailable || lease.Decision.Policy != routing.PolicyKVAwareLoadFallbackV1 {
		t.Fatalf("degradation decision/state = %+v / %q", lease.Decision, lease.State.KV)
	}
	lease.Complete(OutcomeClientCanceled)
}

func TestKVAwarePathRequiresTokenizerAndKVCache(t *testing.T) {
	resolver := &mutableResolver{}
	_, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: newKVAwareStrategy(t), Circuits: newTestBreaker(t),
	})
	if err == nil {
		t.Fatal("KV-aware path accepted missing tokenizer and KV cache")
	}
}

func testTokenizationConfig(baseURL, model string) tokenization.Config {
	return tokenization.Config{
		BaseURL:          baseURL,
		Model:            model,
		Timeout:          5 * time.Second,
		MaxRequestBytes:  2 << 20,
		MaxResponseBytes: 8 << 20,
		MaxTotalTokens:   131072,
	}
}
