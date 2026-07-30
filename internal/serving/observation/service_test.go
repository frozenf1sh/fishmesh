package observation

import (
	"context"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/endpoint"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestServiceReplacesRemovedBackendState(t *testing.T) {
	resolver, err := endpoint.NewStatic([]routing.Backend{{ID: "a", URL: "http://a:8000"}})
	if err != nil {
		t.Fatal(err)
	}
	collector := collectorFunc(func(_ context.Context, backend routing.Backend) routing.BackendObservation {
		return routing.BackendObservation{Status: routing.ObservationOK, ObservedAt: time.Now(), Source: backend.URL}
	})
	service, err := New(Config{Resolver: resolver, Collector: collector, Interval: time.Hour})
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

type collectorFunc func(context.Context, routing.Backend) routing.BackendObservation

func (f collectorFunc) Collect(ctx context.Context, backend routing.Backend) routing.BackendObservation {
	return f(ctx, backend)
}
