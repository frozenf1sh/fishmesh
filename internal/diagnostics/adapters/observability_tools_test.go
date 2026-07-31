package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

func TestVLLMMetricsToolCollectsServingSignals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte(`# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting 3
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running 5
# TYPE vllm:prefix_cache_hits_total counter
vllm:prefix_cache_hits_total 7
# TYPE vllm:prefix_cache_queries_total counter
vllm:prefix_cache_queries_total 10
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc 0.25
# TYPE vllm:time_to_first_token_seconds histogram
vllm:time_to_first_token_seconds_bucket{le="0.1"} 1
vllm:time_to_first_token_seconds_bucket{le="0.5"} 10
vllm:time_to_first_token_seconds_bucket{le="+Inf"} 10
vllm:time_to_first_token_seconds_count 10
vllm:time_to_first_token_seconds_sum 2
`))
	}))
	defer server.Close()

	tool := VLLMMetricsTool{URLs: []string{server.URL}, HTTPClient: server.Client()}
	signal := tool.Collect(context.Background(), domain.Incident{})
	if signal.Status != domain.SignalOK {
		t.Fatalf("status = %q, error = %s", signal.Status, signal.Error)
	}
	if signal.Values["queue_length"] != 3 || signal.Values["running_requests"] != 5 || signal.Values["prefix_cache_hit_rate"] != 0.7 {
		t.Fatalf("unexpected values: %#v", signal.Values)
	}
	if signal.Values["ttft_p95_ms"] != 500 {
		t.Fatalf("ttft_p95_ms = %v, want 500", signal.Values["ttft_p95_ms"])
	}
	if signal.Values["kv_cache_usage_percent"] != 25 {
		t.Fatalf("kv_cache_usage_percent = %v, want 25", signal.Values["kv_cache_usage_percent"])
	}
}

func TestGPUStatusToolCollectsDCGMSignals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{gpu="0"} 75
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{gpu="0"} 6000
# TYPE DCGM_FI_DEV_FB_FREE gauge
DCGM_FI_DEV_FB_FREE{gpu="0"} 2000
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0"} 70
`))
	}))
	defer server.Close()

	signal := (GPUStatusTool{URL: server.URL, HTTPClient: server.Client()}).Collect(context.Background(), domain.Incident{})
	if signal.Status != domain.SignalOK {
		t.Fatalf("status = %q, error = %s", signal.Status, signal.Error)
	}
	if signal.Values["gpu_memory_percent"] != 75 || signal.Values["gpu_utilization_percent"] != 75 {
		t.Fatalf("unexpected values: %#v", signal.Values)
	}
}

func TestKubernetesEventsToolCollectsEventsAndPods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/namespaces/kubellm/events":
			_, _ = writer.Write([]byte(`{"items":[{"type":"Warning","reason":"BackOff"},{"type":"Normal","reason":"Started"}]}`))
		case "/api/v1/namespaces/kubellm/pods":
			_, _ = writer.Write([]byte(`{"items":[{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},{"status":{"phase":"Pending"}},{"status":{"phase":"Failed"}}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	signal := (KubernetesEventsTool{Namespace: "kubellm", BaseURL: server.URL, TokenFile: tokenFile, HTTPClient: server.Client(), Clock: func() time.Time { return time.Unix(1, 0) }}).Collect(context.Background(), domain.Incident{})
	if signal.Status != domain.SignalOK {
		t.Fatalf("status = %q, error = %s", signal.Status, signal.Error)
	}
	if signal.Values["warning_events"] != 1 || signal.Values["pods_ready"] != 1 || signal.Values["pods_not_ready"] != 1 || signal.Values["pods_failed"] != 1 {
		t.Fatalf("unexpected values: %#v", signal.Values)
	}
	if signal.Attributes["warning_reasons"] != "BackOff" {
		t.Fatalf("warning reasons = %q", signal.Attributes["warning_reasons"])
	}
}
