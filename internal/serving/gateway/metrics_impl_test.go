package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
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

func TestMetricsObserveKVEventAndAvailableCachedPrefixWithoutUnknownZero(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeKVEvent("backend-a", false, 3*time.Millisecond)
	metrics.observeKVEvent("backend-a", true, 5*time.Millisecond)
	metrics.observeSelection(routing.ModeExactCacheLoad, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}},
		State:    requestpath.State{Exact: requestpath.ExactAvailable, CachedPrefixTokens: 0},
	})
	metrics.observeSelection(routing.ModeExactCacheLoad, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}},
		State:    requestpath.State{Exact: requestpath.ExactMatchUnavailable, CachedPrefixTokens: 99},
	})

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_kv_event_publish_to_apply_seconds_count{backend_id="backend-a",source="live"} 1`,
		`fishmesh_gateway_kv_event_publish_to_apply_seconds_count{backend_id="backend-a",source="replay"} 1`,
		`fishmesh_gateway_exact_cached_prefix_tokens_count 1`,
		`fishmesh_gateway_exact_cached_prefix_tokens_sum 0`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"pod_uid", "prompt", "token_ids", "topic"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("metrics exposed sensitive event data %q:\n%s", forbidden, output)
		}
	}
}

func TestMetricsProjectPredictionShadowWithoutRequestLabels(t *testing.T) {
	metrics := NewMetrics()
	lease := requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}},
		State:    requestpath.State{Prediction: prediction.Shadow{Status: prediction.StatusAvailable, WouldSelect: "backend-b"}},
	}
	metrics.observeSelection(routing.ModeExactCacheLoad, lease)
	metrics.observePrediction(lease, requestpath.FirstTokenObservation{Valid: true, Error: -3 * time.Millisecond})

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_prediction_shadows_total{outcome="different",status="available"} 1`,
		`fishmesh_gateway_prediction_absolute_error_seconds_count 1`,
		`fishmesh_gateway_prediction_absolute_error_seconds_sum 0.003`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"prompt", "token_ids", "routing_key"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("prediction metrics exposed %q:\n%s", forbidden, output)
		}
	}
}

func TestMetricsDeleteBackendRemovesKVEventHistogramLabels(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeKVEvent("backend-a", false, time.Millisecond)
	metrics.DeleteBackend("backend-a", string(routing.ModeExactCacheLoad))

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(response.Body.String(), `fishmesh_gateway_kv_event_publish_to_apply_seconds_count{backend_id="backend-a"`) {
		t.Fatalf("removed backend retained KV event histogram labels:\n%s", response.Body.String())
	}
}
