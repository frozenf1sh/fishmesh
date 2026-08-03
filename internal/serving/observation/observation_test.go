package observation

import (
	"context"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
)

func TestServiceReplacesRemovedBackendState(t *testing.T) {
	resolver, err := discovery.NewStatic([]backend.Backend{{ID: "a", URL: "http://a:8000"}})
	if err != nil {
		t.Fatal(err)
	}
	collector := collectorFunc(func(_ context.Context, candidate backend.Backend) Backend {
		return Backend{Status: StatusOK, ObservedAt: time.Now(), Source: candidate.URL}
	})
	service, err := New(Config{Interval: time.Hour}, Dependencies{Resolver: resolver, Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	defer resolver.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(service.Snapshot()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if len(service.Snapshot()) != 1 {
		t.Fatalf("expected one backend observation, got %d", len(service.Snapshot()))
	}
}

type collectorFunc func(context.Context, backend.Backend) Backend

func (f collectorFunc) Collect(ctx context.Context, candidate backend.Backend) Backend {
	return f(ctx, candidate)
}
