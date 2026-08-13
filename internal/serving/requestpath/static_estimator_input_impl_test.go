package requestpath

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestStaticLatencyEstimatesProjectKVAndDeduplicatedLoad(t *testing.T) {
	estimator, err := prediction.NewStaticEstimator(prediction.StaticProfile{
		Identity: prediction.ProfileIdentity{
			Model: "qwen", HardwareProfile: "gpu", VLLMVersion: "v1", MinPromptTokens: 100, MaxModelTokens: 200,
		},
		Version: "profile-v1", Calibrated: true,
		PromptTokenBreakpoints: []int64{100, 200}, CachedRatioBreakpoints: []int64{0, 10_000},
		Prefill:             [][]time.Duration{{100 * time.Millisecond, 10 * time.Millisecond}, {200 * time.Millisecond, 20 * time.Millisecond}},
		QueueWaitPerRequest: 20 * time.Millisecond, LocalFallbackPerRequest: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := routing.Snapshot{
		Backends: []backend.Backend{{ID: "a"}, {ID: "b"}}, Inflight: map[backend.ID]int64{"a": 100, "b": 2},
		Loads: map[backend.ID]routing.Load{"a": {Valid: true, QueueDepth: 1}, "b": {Valid: false}},
	}
	estimates := staticLatencyEstimates(snapshot, routing.KVAwareInput{
		PromptTokens: 100, Matches: map[backend.ID]routing.KVMatch{
			"a": {Valid: true, MatchedTokens: 100}, "b": {Valid: true, MatchedTokens: 100},
		},
	}, estimator)
	if estimates["a"].TTFT != 30*time.Millisecond || estimates["a"].Confidence != routing.EstimateConfidenceCalibrated {
		t.Fatalf("valid external load estimate = %+v", estimates["a"])
	}
	if estimates["b"].TTFT != 20*time.Millisecond || estimates["b"].Confidence != routing.EstimateConfidenceDegraded {
		t.Fatalf("local fallback estimate = %+v", estimates["b"])
	}
}

func TestSelectedEstimateEvidenceProjectsOnlyBoundedNumericState(t *testing.T) {
	snapshot := routing.Snapshot{
		Backends: []backend.Backend{{ID: "a"}, {ID: "b"}},
		Inflight: map[backend.ID]int64{"a": 3},
		Loads: map[backend.ID]routing.Load{
			"a": {Valid: true, QueueDepth: 2, Running: 1, LocalDelta: 2},
			"b": {Valid: true, HardOverload: true},
		},
		Estimates: map[backend.ID]routing.LatencyEstimate{
			"a": {
				TTFT: 42 * time.Millisecond, Valid: true,
				Confidence: routing.EstimateConfidenceCalibrated, Version: "profile-v1", Reason: "calibrated",
			},
		},
	}
	evidence := selectedEstimateEvidence(
		snapshot,
		map[backend.ID]observation.Backend{"a": {Freshness: 250 * time.Millisecond}},
		routing.KVAwareInput{PromptTokens: 1024, Matches: map[backend.ID]routing.KVMatch{"a": {Valid: true, MatchedTokens: 768}}},
		"a",
	)
	if evidence.PromptTokens != 1024 || evidence.CachedPrefixTokens != 768 || evidence.UncachedTokens != 256 {
		t.Fatalf("token evidence = %+v", evidence)
	}
	if evidence.EstimatedTTFT != 42*time.Millisecond || !evidence.Valid || evidence.Version != "profile-v1" {
		t.Fatalf("estimate evidence = %+v", evidence)
	}
	if !evidence.LoadValid || evidence.QueueDepth != 2 || evidence.Running != 1 || evidence.LocalDelta != 2 || evidence.LocalInflight != 3 || evidence.LoadSampleAge != 250*time.Millisecond {
		t.Fatalf("load evidence = %+v", evidence)
	}
	if evidence.HardOverloadedCandidates != 1 {
		t.Fatalf("hard-overloaded candidates = %d", evidence.HardOverloadedCandidates)
	}
}
