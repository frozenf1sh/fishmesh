package client

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGatewayMetricsWindowComputesRatesAndLittleLaw(t *testing.T) {
	start := time.Unix(10, 0)
	window := gatewayMetricsWindow([]GatewayMetricsSnapshot{
		{ObservedAt: start, AdmittedRequestsTotal: 20, CompletedRequestsTotal: 30, InflightRequests: 0},
		{ObservedAt: start.Add(time.Second), AdmittedRequestsTotal: 25, CompletedRequestsTotal: 34, InflightRequests: 4},
		{ObservedAt: start.Add(2 * time.Second), AdmittedRequestsTotal: 30, CompletedRequestsTotal: 38, InflightRequests: 2},
	})
	if !window.Valid || window.Samples != 3 || window.AdmittedDelta != 10 || window.CompletedDelta != 8 {
		t.Fatalf("unexpected gateway window: %+v", window)
	}
	if window.AcceptedRateQPS != 5 || window.CompletedRateQPS != 4 || window.AverageInflight != 2.5 || window.LittleLawWaitMS != 500 || !window.LittleLawWaitValid {
		t.Fatalf("unexpected gateway rates: %+v", window)
	}
}

func TestPrometheusGatewayMetricsReaderSumsCompletedStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`# TYPE fishmesh_gateway_admitted_requests_total counter
fishmesh_gateway_admitted_requests_total 10
# TYPE fishmesh_gateway_requests_total counter
fishmesh_gateway_requests_total{method="POST",status="200"} 7
fishmesh_gateway_requests_total{method="POST",status="503"} 2
# TYPE fishmesh_gateway_admission_rejections_total counter
fishmesh_gateway_admission_rejections_total 1
# TYPE fishmesh_gateway_inflight_requests gauge
fishmesh_gateway_inflight_requests 3
`))
	}))
	defer server.Close()
	reader, err := NewPrometheusGatewayMetricsReader(PrometheusGatewayMetricsConfig{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AdmittedRequestsTotal != 10 || snapshot.CompletedRequestsTotal != 9 || snapshot.AdmissionRejectionsTotal != 1 || snapshot.InflightRequests != 3 || snapshot.ObservedAt.IsZero() {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestPrometheusGatewayMetricsReaderTreatsUnseenCompletedCounterAsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`# TYPE fishmesh_gateway_admitted_requests_total counter
fishmesh_gateway_admitted_requests_total 0
# TYPE fishmesh_gateway_admission_rejections_total counter
fishmesh_gateway_admission_rejections_total 0
# TYPE fishmesh_gateway_inflight_requests gauge
fishmesh_gateway_inflight_requests 0
`))
	}))
	defer server.Close()
	reader, err := NewPrometheusGatewayMetricsReader(PrometheusGatewayMetricsConfig{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CompletedRequestsTotal != 0 {
		t.Fatalf("unseen completed counter = %+v", snapshot)
	}
}

func TestPrometheusGatewayMetricsReaderRejectsInvalidEndpoint(t *testing.T) {
	if _, err := NewPrometheusGatewayMetricsReader(PrometheusGatewayMetricsConfig{Endpoint: "/metrics"}); err == nil {
		t.Fatal("relative metrics endpoint was accepted")
	}
}

func TestGatewayMetricsWindowRejectsCounterReset(t *testing.T) {
	start := time.Unix(10, 0)
	window := gatewayMetricsWindow([]GatewayMetricsSnapshot{
		{ObservedAt: start, AdmittedRequestsTotal: 20, CompletedRequestsTotal: 30},
		{ObservedAt: start.Add(time.Second), AdmittedRequestsTotal: 2, CompletedRequestsTotal: 31},
	})
	if window.Valid || window.Error != "gateway counter reset during metrics window" {
		t.Fatalf("counter reset was not rejected: %+v", window)
	}
}

func TestCombineGatewayMetricsWindowsExcludesScenarioGaps(t *testing.T) {
	start := time.Unix(10, 0)
	first := gatewayMetricsWindow([]GatewayMetricsSnapshot{
		{ObservedAt: start, AdmittedRequestsTotal: 10, CompletedRequestsTotal: 8, InflightRequests: 0},
		{ObservedAt: start.Add(time.Second), AdmittedRequestsTotal: 15, CompletedRequestsTotal: 12, InflightRequests: 4},
	})
	second := gatewayMetricsWindow([]GatewayMetricsSnapshot{
		{ObservedAt: start.Add(10 * time.Second), AdmittedRequestsTotal: 20, CompletedRequestsTotal: 17, InflightRequests: 2},
		{ObservedAt: start.Add(12 * time.Second), AdmittedRequestsTotal: 30, CompletedRequestsTotal: 27, InflightRequests: 6},
	})
	combined := combineGatewayMetricsWindows([]GatewayMetricsWindow{first, second})
	if !combined.Valid || combined.Segments != 2 || combined.ElapsedMS != 3000 || combined.AdmittedDelta != 15 || combined.CompletedDelta != 14 {
		t.Fatalf("unexpected combined window: %+v", combined)
	}
	if math.Abs(combined.AverageInflight-10.0/3.0) > 1e-9 || combined.AcceptedRateQPS != 5 || math.Abs(combined.LittleLawWaitMS-2000.0/3.0) > 1e-9 {
		t.Fatalf("unexpected combined rates: %+v", combined)
	}
}

type scriptedGatewayMetricsReader struct {
	mu       sync.Mutex
	samples  []GatewayMetricsSnapshot
	position int
}

func (r *scriptedGatewayMetricsReader) Snapshot(context.Context) (GatewayMetricsSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sample := r.samples[r.position]
	if r.position < len(r.samples)-1 {
		r.position++
	}
	return sample, nil
}

type cancelAwareGatewayMetricsReader struct {
	mu    sync.Mutex
	count int
}

func (r *cancelAwareGatewayMetricsReader) Snapshot(ctx context.Context) (GatewayMetricsSnapshot, error) {
	r.mu.Lock()
	r.count++
	count := r.count
	r.mu.Unlock()
	if count == 1 {
		return GatewayMetricsSnapshot{ObservedAt: time.Unix(100, 0)}, nil
	}
	if ctx.Err() != nil {
		<-ctx.Done()
		return GatewayMetricsSnapshot{}, ctx.Err()
	}
	return GatewayMetricsSnapshot{ObservedAt: time.Unix(101, 0)}, nil
}

func TestGatewayMetricsStopIgnoresCancellationCausedByShutdown(t *testing.T) {
	reader := &cancelAwareGatewayMetricsReader{}
	run := beginGatewayMetricsRun(context.Background(), reader, time.Millisecond)
	window := run.stop(context.Background())
	if window.Error == "context canceled" {
		t.Fatalf("shutdown cancellation poisoned metrics window: %+v", window)
	}
}

func TestRunPlanWithMetricsStartsAfterWarmup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderBackendID, "backend-a")
		writer.Header().Set(HeaderKVStatus, "available")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	service, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	reader := &scriptedGatewayMetricsReader{samples: []GatewayMetricsSnapshot{
		{ObservedAt: time.Unix(100, 0), AdmittedRequestsTotal: 10, CompletedRequestsTotal: 8},
		{ObservedAt: time.Unix(101, 0), AdmittedRequestsTotal: 11, CompletedRequestsTotal: 9},
	}}
	plan := BenchmarkPlan{
		RunID: "warmup-window", CacheMode: CacheControlledWarm, WorkloadSeed: 1, Treatment: "test", RunNonce: "run", CacheGeneration: "generation", MaxTokens: 8, RequestTimeoutMS: 1000,
		Scenarios: []BenchmarkScenario{{Name: "same", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: 1, WarmupRequests: 1}},
	}
	report, err := service.RunPlanWithMetrics(context.Background(), plan, &strings.Builder{}, reader, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.GatewayMetrics == nil || !report.GatewayMetrics.Valid || report.GatewayMetrics.WarmupRequests != 1 || !report.GatewayMetrics.WarmupExcluded || report.GatewayMetrics.AdmittedDelta != 1 {
		t.Fatalf("unexpected warmup metrics: %+v", report.GatewayMetrics)
	}
	if len(report.Scenarios) != 1 || report.Scenarios[0].GatewayMetrics == nil || !report.Scenarios[0].GatewayMetrics.Valid || report.Scenarios[0].Batches[0].GatewayMetrics == nil {
		t.Fatalf("scenario/batch Gateway metrics missing: %+v", report.Scenarios)
	}
}
