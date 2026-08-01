package observation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/endpoint"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestPrometheusCollectorMapsPerBackendMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/metrics" {
			t.Fatalf("metrics path = %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting 2
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running 4
# TYPE vllm:prefix_cache_hits_total counter
vllm:prefix_cache_hits_total 8
# TYPE vllm:prefix_cache_queries_total counter
vllm:prefix_cache_queries_total 10
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc 0.25
# TYPE vllm:time_to_first_token_seconds histogram
vllm:time_to_first_token_seconds_bucket{le="0.1"} 1
vllm:time_to_first_token_seconds_bucket{le="0.5"} 10
vllm:time_to_first_token_seconds_bucket{le="+Inf"} 10
`))
	}))
	defer server.Close()
	state := (PrometheusCollector{HTTPClient: server.Client(), Clock: func() time.Time { return time.Unix(5, 0) }}).Collect(context.Background(), routing.Backend{ID: "endpoint-a", URL: server.URL + "/v1"})
	if state.Status != routing.ObservationOK || state.ObservedAt != time.Unix(5, 0) {
		t.Fatalf("unexpected state: %+v", state)
	}
	if !state.QueueLength.Valid || state.QueueLength.Value != 2 || !state.RunningRequests.Valid || state.RunningRequests.Value != 4 || state.PrefixCacheHitRate != 0.8 || state.TTFTP95Milliseconds != 500 || state.KVCacheUsagePercent != 25 {
		t.Fatalf("unexpected observations: %+v", state)
	}
}

type fakeCollector struct {
	clock time.Time
}

func (c fakeCollector) Collect(context.Context, routing.Backend) routing.BackendObservation {
	return routing.BackendObservation{Status: routing.ObservationOK, ObservedAt: c.clock, QueueLength: routing.Sample[float64]{Value: 3, Valid: true, ObservedAt: c.clock}}
}

func TestServiceMarksStaleObservationDegraded(t *testing.T) {
	resolver, err := endpoint.NewStatic([]routing.Backend{{ID: "endpoint-a", URL: "http://10.0.0.1:8000"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	now := time.Unix(100, 0)
	service, err := New(Config{Resolver: resolver, Collector: fakeCollector{clock: time.Unix(90, 0)}, Interval: time.Hour, MaxAge: 5 * time.Second, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	deadline := time.Now().Add(time.Second)
	var snapshot map[string]routing.BackendObservation
	for time.Now().Before(deadline) {
		snapshot = service.Snapshot()
		if len(snapshot) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	state := snapshot["endpoint-a"]
	if state.Status != routing.ObservationDegraded || state.Error != "observation is stale" || state.Freshness != 10*time.Second || state.QueueLength.Valid || state.QueueLength.Error != "sample is stale" {
		t.Fatalf("unexpected stale state: %+v", state)
	}
}

func TestPrometheusCollectorPreservesObservedZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting 0\n"))
	}))
	defer server.Close()
	state := (PrometheusCollector{HTTPClient: server.Client()}).Collect(context.Background(), routing.Backend{ID: "a", URL: server.URL})
	if !state.QueueLength.Valid || state.QueueLength.Value != 0 {
		t.Fatalf("observed zero was treated as missing: %+v", state.QueueLength)
	}
	if state.RunningRequests.Valid {
		t.Fatalf("missing running metric was treated as observed: %+v", state.RunningRequests)
	}
}
