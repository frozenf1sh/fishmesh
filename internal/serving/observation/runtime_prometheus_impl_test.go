package observation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
)

func TestPrometheusRuntimeCollectorMapsPodScopedQueries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		if !strings.Contains(query, `namespace="kubellm"`) || !strings.Contains(query, `pod="vllm-a"`) {
			t.Fatalf("query lacks Pod identity: %q", query)
		}
		value := "1"
		switch {
		case strings.Contains(query, "memory"):
			value = "2048"
		case strings.Contains(query, "gpu"):
			value = "75"
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": []any{map[string]any{"value": []any{float64(1), value}}}},
		})
	}))
	defer server.Close()
	clock := func() time.Time { return time.Unix(20, 0) }
	collector, err := NewPrometheusRuntime(RuntimePrometheusConfig{
		Endpoint: server.URL, Namespace: "kubellm", HTTPClient: server.Client(), Clock: clock,
		CPUQuery:            "sum(container_cpu_usage_seconds_total{namespace=$namespace,pod=$pod})",
		MemoryQuery:         "sum(memory_bytes{namespace=$namespace,pod=$pod})",
		GPUUtilizationQuery: "avg(gpu_util{namespace=$namespace,pod=$pod})",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := collector.Collect(context.Background(), backend.Backend{ID: "backend-a"}, identity.Identity{PodName: "vllm-a"})
	if !state.CPUUsageCores.Valid || state.CPUUsageCores.Value != 1 || !state.MemoryUsageBytes.Valid || state.MemoryUsageBytes.Value != 2048 || !state.GPUUtilizationPercent.Valid || state.GPUUtilizationPercent.Value != 75 {
		t.Fatalf("unexpected runtime state: %+v", state)
	}
	if state.ObservedAt != time.Unix(20, 0) || state.Error != "" {
		t.Fatalf("unexpected runtime metadata: %+v", state)
	}
}

func TestRuntimePrometheusValidationRejectsUnscopedQuery(t *testing.T) {
	_, err := NewPrometheusRuntime(RuntimePrometheusConfig{Endpoint: "http://prometheus", Namespace: "kubellm", CPUQuery: "sum(node_cpu_seconds_total)"})
	if err == nil || !strings.Contains(err.Error(), "$namespace and $pod") {
		t.Fatalf("unscoped query error = %v", err)
	}
}
