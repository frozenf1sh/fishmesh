package requestpath

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

const testBackendURL = "http://backend:8000"

type mutableResolver struct {
	mu       sync.RWMutex
	backends []backend.Backend
	status   discovery.ResolverStatus
	err      error
}

func (r *mutableResolver) Snapshot(context.Context) ([]backend.Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]backend.Backend(nil), r.backends...), r.err
}

func (r *mutableResolver) Status() discovery.ResolverStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (*mutableResolver) Close() error { return nil }

func (r *mutableResolver) replace(backends []backend.Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends = append([]backend.Backend(nil), backends...)
	r.status.ReadyBackends = len(backends)
}

type invalidStrategy struct{}

func (invalidStrategy) Name() routing.Mode { return routing.ModePrefixAffinity }
func (invalidStrategy) Select(string, routing.Snapshot) (routing.Decision, error) {
	return routing.Decision{Backend: backend.Backend{ID: "invalid", URL: "/relative"}}, nil
}

func TestSelectUsesExplicitFallbackReasons(t *testing.T) {
	serviceBackend := backend.Backend{ID: "service", URL: "http://service:8000"}
	directBackend := backend.Backend{ID: "direct", URL: testBackendURL}
	resolver := &mutableResolver{
		backends: []backend.Backend{directBackend},
		status:   discovery.ResolverStatus{Status: discovery.StatusOK, ReadyBackends: 1, Freshness: time.Second},
	}
	breaker := newTestBreaker(t)
	path, err := New(Config{Service: serviceBackend, RequireFreshDiscovery: true, DiscoveryMaxAge: time.Minute}, Dependencies{
		Resolver: resolver, Strategy: invalidStrategy{}, Circuits: breaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()

	lease, err := path.Select(context.Background(), Request{RoutingKey: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Decision.Backend.ID != serviceBackend.ID || lease.Decision.Reason != routing.ReasonBackendFallback {
		t.Fatalf("invalid backend did not use service fallback: %+v", lease.Decision)
	}
	lease.Complete(OutcomeClientCanceled)

	resolver.mu.Lock()
	resolver.status.Freshness = 2 * time.Minute
	resolver.mu.Unlock()
	lease, err = path.Select(context.Background(), Request{RoutingKey: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Decision.Reason != routing.ReasonDiscoveryFallback {
		t.Fatalf("stale discovery reason = %q", lease.Decision.Reason)
	}
	lease.Complete(OutcomeClientCanceled)
}

func TestLeaseCompletionClassifiesCircuitOutcomesAndIsIdempotent(t *testing.T) {
	candidate := backend.Backend{ID: "direct", URL: testBackendURL}
	resolver := &mutableResolver{
		backends: []backend.Backend{candidate},
		status:   discovery.ResolverStatus{Status: discovery.StatusOK, ReadyBackends: 1},
	}
	breaker := newTestBreaker(t)
	strategy := routing.NewPrefixAffinity()
	path, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: strategy, Circuits: breaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()

	neutral, err := path.Select(context.Background(), Request{RoutingKey: "session"})
	if err != nil {
		t.Fatal(err)
	}
	neutral.Complete(OutcomeClientCanceled)
	if breaker.Len() != 0 {
		t.Fatalf("client cancellation created circuit state: %d", breaker.Len())
	}

	failed, err := path.Select(context.Background(), Request{RoutingKey: "session"})
	if err != nil {
		t.Fatal(err)
	}
	first := failed.Complete(OutcomeTransportFailure)
	second := failed.Complete(OutcomeResponseCompleted)
	if !first.CircuitOpened || first != second || !breaker.IsOpen(candidate.ID) {
		t.Fatalf("lease was not classified exactly once: first=%+v second=%+v", first, second)
	}
}

func TestRemovedBackendWaitsForInflightLeaseBeforeCleanup(t *testing.T) {
	backends := []backend.Backend{{ID: "a", URL: "http://a:8000"}, {ID: "b", URL: "http://b:8000"}}
	resolver := &mutableResolver{
		backends: backends,
		status:   discovery.ResolverStatus{Status: discovery.StatusOK, ReadyBackends: len(backends)},
	}
	var mu sync.Mutex
	var removed []backend.ID
	path, err := New(Config{Service: backend.Backend{ID: "service", URL: "http://service:8000"}}, Dependencies{
		Resolver: resolver, Strategy: routing.NewPrefixAffinity(), Circuits: newTestBreaker(t),
		OnBackendRemoved: func(id backend.ID) {
			mu.Lock()
			defer mu.Unlock()
			removed = append(removed, id)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()

	lease, err := path.Select(context.Background(), Request{RoutingKey: "session"})
	if err != nil {
		t.Fatal(err)
	}
	remaining := backends[:1]
	if lease.Decision.Backend.ID == backends[0].ID {
		remaining = backends[1:]
	}
	resolver.replace(remaining)
	path.State(context.Background())
	if len(removed) != 0 {
		t.Fatalf("in-flight backend was cleaned too early: %v", removed)
	}
	completion := lease.Complete(OutcomeClientCanceled)
	if !completion.BackendRemoved || len(removed) != 1 || removed[0] != lease.Decision.Backend.ID {
		t.Fatalf("removed backend cleanup = %v, completion = %+v", removed, completion)
	}
}

func TestReadyRequiresUsableFreshDiscovery(t *testing.T) {
	resolver := &mutableResolver{}
	path, err := New(Config{
		Service:               backend.Backend{ID: "service", URL: "http://service:8000"},
		RequireFreshDiscovery: true, DiscoveryMaxAge: time.Minute,
	}, Dependencies{Resolver: resolver, Strategy: routing.NewPrefixAffinity(), Circuits: newTestBreaker(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer path.Close()

	for name, test := range map[string]struct {
		status discovery.ResolverStatus
		want   bool
	}{
		"fresh":       {discovery.ResolverStatus{Status: discovery.StatusOK, ReadyBackends: 1, Freshness: time.Second}, true},
		"degraded":    {discovery.ResolverStatus{Status: discovery.StatusDegraded, ReadyBackends: 1, Freshness: time.Second}, true},
		"stale":       {discovery.ResolverStatus{Status: discovery.StatusDegraded, ReadyBackends: 1, Freshness: 2 * time.Minute}, false},
		"unavailable": {discovery.ResolverStatus{Status: discovery.StatusUnavailable}, false},
	} {
		t.Run(name, func(t *testing.T) {
			resolver.mu.Lock()
			resolver.status = test.status
			resolver.mu.Unlock()
			if got := path.Ready(); got != test.want {
				t.Fatalf("Ready() = %t, want %t", got, test.want)
			}
		})
	}
}

func newTestBreaker(t *testing.T) circuit.Breaker {
	t.Helper()
	breaker, err := circuit.New(circuit.Config{
		EWMAAlpha: 1, ErrorThreshold: 0.5, MinimumRequests: 1,
		OpenDuration: time.Minute, Clock: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return breaker
}
