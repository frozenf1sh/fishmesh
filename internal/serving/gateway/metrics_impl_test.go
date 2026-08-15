package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestMetricsProjectKVAwareKVStateWithoutSensitiveLabels(t *testing.T) {
	metrics := NewMetrics()
	backendID := backend.ID("backend-a")
	state := requestpath.State{KV: requestpath.KVMatchUnavailable, KVCache: map[backend.ID]requestpath.KVCacheState{
		backendID: {
			Valid: true, Reason: kvcache.ReasonNone, Freshness: 2 * time.Second,
			LastSequence: 7, AppliedBatches: 8, ReplayBatches: 3,
		},
	}}
	metrics.updateRequestPath(state)
	metrics.observeSelection(routing.ModeKVAware, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: backendID}, Reason: routing.ReasonKVAwareSignalUnavailable},
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
		`fishmesh_gateway_kv_aware_requests_total{status="match-unavailable"} 1`,
		`fishmesh_gateway_kv_aware_degradations_total{status="match-unavailable"} 1`,
		`process_resident_memory_bytes`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
	if strings.Contains(output, "token_ids") || strings.Contains(output, "pod_uid") || strings.Contains(output, "routing_key") {
		t.Fatalf("metrics exposed sensitive KV-aware input: %s", output)
	}
}

func TestMetricsObserveKVEventAndAvailableCachedPrefixWithoutUnknownZero(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeKVEvent("backend-a", false, 3*time.Millisecond)
	metrics.observeKVEvent("backend-a", true, 5*time.Millisecond)
	metrics.observeSelection(routing.ModeKVAware, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}},
		State:    requestpath.State{KV: requestpath.KVAvailable, CachedPrefixTokens: 0},
	})
	metrics.observeSelection(routing.ModeKVAware, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}},
		State:    requestpath.State{KV: requestpath.KVMatchUnavailable, CachedPrefixTokens: 99},
	})

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_kv_event_publish_to_apply_seconds_count{backend_id="backend-a",source="live"} 1`,
		`fishmesh_gateway_kv_event_publish_to_apply_seconds_count{backend_id="backend-a",source="replay"} 1`,
		`fishmesh_gateway_kv_aware_cached_prefix_tokens_count 1`,
		`fishmesh_gateway_kv_aware_cached_prefix_tokens_sum 0`,
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

func TestMetricsSeparatesShortContextBypassFromKVDegradation(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeSelection(routing.ModeKVAware, requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}, Reason: routing.ReasonKVAwareShortContextFallback, Policy: routing.PolicyKVAwareShortContextFallbackV1},
		State:    requestpath.State{KV: requestpath.KVShortContextBypassed},
	})

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	if !strings.Contains(output, `fishmesh_gateway_kv_aware_bypasses_total{reason="kv-aware-short-context-fallback"} 1`) {
		t.Fatalf("short-context bypass metric missing:\n%s", output)
	}
	if strings.Contains(output, `fishmesh_gateway_kv_aware_degradations_total{status="short-context-bypassed"}`) {
		t.Fatalf("short-context bypass was reported as degradation:\n%s", output)
	}
}

func TestMetricsProjectPredictionShadowWithoutRequestLabels(t *testing.T) {
	metrics := NewMetrics()
	lease := requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}},
		State:    requestpath.State{Prediction: prediction.Shadow{Status: prediction.StatusAvailable, WouldSelect: "backend-b"}},
	}
	metrics.observeSelection(routing.ModeKVAware, lease)
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

func TestMetricsProjectStaticEstimateAndHardOverloadWithoutRequestIdentity(t *testing.T) {
	metrics := NewMetrics()
	lease := requestpath.Lease{
		Decision: routing.Decision{Backend: backend.Backend{ID: "backend-a"}, Reason: routing.ReasonKVAwareStatic},
		State: requestpath.State{Estimate: requestpath.EstimateEvidence{
			PromptTokens: 1024, EstimatedTTFT: 40 * time.Millisecond, Valid: true,
			Confidence: routing.EstimateConfidenceCalibrated, Reason: "calibrated", HardOverloadedCandidates: 1,
		}},
	}
	metrics.observeSelection(routing.ModeKVAware, lease)
	metrics.observePrediction(lease, requestpath.FirstTokenObservation{Actual: 50 * time.Millisecond})

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_static_estimator_selections_total{confidence="calibrated",estimator_reason="calibrated"} 1`,
		`fishmesh_gateway_static_estimated_ttft_seconds_count 1`,
		`fishmesh_gateway_static_estimator_absolute_error_seconds_sum 0.01`,
		`fishmesh_gateway_hard_overload_selections_total{outcome="partial"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
}

func TestMetricsDeleteBackendRemovesKVEventHistogramLabels(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeKVEvent("backend-a", false, time.Millisecond)
	metrics.DeleteBackend("backend-a", string(routing.ModeKVAware))

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(response.Body.String(), `fishmesh_gateway_kv_event_publish_to_apply_seconds_count{backend_id="backend-a"`) {
		t.Fatalf("removed backend retained KV event histogram labels:\n%s", response.Body.String())
	}
}

func TestMetricsSeparatesAdmittedRequestsFromCapacityRejections(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeAdmissionAccepted()
	metrics.admissionRejections.Inc()

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_admitted_requests_total 1`,
		`fishmesh_gateway_admission_rejections_total 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
}

func TestMetricsExposeAdmissionRejectionClasses(t *testing.T) {
	metrics := NewMetrics()
	metrics.admissionRejections.Inc()
	metrics.admissionSoftRejections.Inc()
	metrics.softRejectedTotal.Add(1)
	metrics.admissionRejections.Inc()
	metrics.admissionHardRejections.Inc()
	metrics.hardRejectedTotal.Add(1)

	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_admission_soft_rejections_total 1`,
		`fishmesh_gateway_admission_hard_rejections_total 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
}

func TestMetricsExposeAdmissionTuningState(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveAdmissionTuning(admission.Decision{
		Mode: admission.TuningActive, ObservedAt: time.Unix(100, 0), PreviousTarget: 16,
		SuggestedTarget: 8, AppliedTarget: 8, HardLimit: 32, Valid: true, Changed: true, Reason: "overloaded",
	})
	response := httptest.NewRecorder()
	metrics.handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	output := response.Body.String()
	for _, want := range []string{
		`fishmesh_gateway_admission_target 8`,
		`fishmesh_gateway_admission_hard_limit 32`,
		`fishmesh_gateway_admission_suggested_target 8`,
		`fishmesh_gateway_admission_tuning_actions_total 1`,
		`fishmesh_gateway_admission_tuning_mode{mode="active"} 1`,
		`fishmesh_gateway_admission_tuning_reason{reason="overloaded"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metric %q missing:\n%s", want, output)
		}
	}
}
