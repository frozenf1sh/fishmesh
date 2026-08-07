package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestMetricsProjectExactKVStateWithoutSensitiveLabels(t *testing.T) {
	metrics := NewMetrics()
	backendID := backend.ID("backend-a")
	state := requestpath.State{Exact: requestpath.ExactMatchUnavailable, KVCache: map[backend.ID]requestpath.KVCacheState{
		backendID: {
			Valid: true, Reason: kvcache.ReasonNone, Freshness: 2 * time.Second,
			LastSequence: 7, AppliedBatches: 8, ReplayBatches: 3,
		},
	}}
	metrics.updateRequestPath(state)
	metrics.observeSelection(routing.ModeExactCacheLoad, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: backendID}, Reason: routing.ReasonExactSignalUnavailable},
		State:    state,
	})

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_kv_cache_instance_valid{backend_id="backend-a"} 1`,
		`fishmesh_gateway_kv_cache_freshness_seconds{backend_id="backend-a"} 2`,
		`fishmesh_gateway_kv_cache_last_sequence{backend_id="backend-a"} 7`,
		`fishmesh_gateway_kv_cache_applied_batches{backend_id="backend-a"} 8`,
		`fishmesh_gateway_kv_cache_replay_batches{backend_id="backend-a"} 3`,
		`fishmesh_gateway_exact_requests_total{status="match-unavailable"} 1`,
		`fishmesh_gateway_exact_degradations_total{status="match-unavailable"} 1`,
		`process_resident_memory_bytes`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
	if strings.Contains(output, "token_ids") || strings.Contains(output, "pod_uid") || strings.Contains(output, "routing_key") {
		t.Fatalf("metrics exposed sensitive exact input: %s", output)
	}
}
