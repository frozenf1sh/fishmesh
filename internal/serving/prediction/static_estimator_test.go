package prediction

import (
	"math"
	"testing"
	"time"
)

func TestStaticEstimatorInterpolatesPromptAndCacheRatio(t *testing.T) {
	estimator := newTestStaticEstimator(t, true)
	estimate := estimator.Estimate(StaticInput{
		Identity: testProfileIdentity(),
		Prompt:   PromptWork{PromptTokens: 1536, CachedPrefixTokens: 768},
		Load:     LoadWork{QueueDepth: 1, Running: 2, Valid: true},
	})
	// At 50% cache, prompt interpolation is 60ms; load is 20+20ms and safety is 5ms.
	if !estimate.Valid || estimate.TTFT != 105*time.Millisecond || estimate.Confidence != StaticConfidenceCalibrated || estimate.Reason != StaticReasonCalibrated {
		t.Fatalf("estimate = %+v", estimate)
	}
}

func TestStaticEstimatorUsesLocalFallbackForUnknownLoad(t *testing.T) {
	estimator := newTestStaticEstimator(t, true)
	estimate := estimator.Estimate(StaticInput{
		Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 1024, CachedPrefixTokens: 1024},
		Load: LoadWork{LocalInflight: 3, Valid: false},
	})
	if !estimate.Valid || estimate.TTFT != 30*time.Millisecond || estimate.Confidence != StaticConfidenceDegraded || estimate.Reason != StaticReasonLoadFallback {
		t.Fatalf("estimate = %+v", estimate)
	}
}

func TestStaticEstimatorNeverMarksUncalibratedProfileCalibrated(t *testing.T) {
	estimator := newTestStaticEstimator(t, false)
	estimate := estimator.Estimate(StaticInput{
		Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 1024}, Load: LoadWork{Valid: true},
	})
	if !estimate.Valid || estimate.Confidence != StaticConfidenceUncalibrated || estimate.Reason != StaticReasonUncalibrated {
		t.Fatalf("estimate = %+v", estimate)
	}
}

func TestStaticEstimatorRejectsMismatchRangeAndOverflow(t *testing.T) {
	estimator := newTestStaticEstimator(t, true)
	for name, input := range map[string]StaticInput{
		"identity":   {Identity: ProfileIdentity{Model: "other"}, Prompt: PromptWork{PromptTokens: 1}},
		"low range":  {Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 512}},
		"high range": {Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 4097}},
		"invalid":    {Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 10, CachedPrefixTokens: 11}},
		"overflow":   {Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 1024}, Load: LoadWork{QueueDepth: math.MaxInt64, Valid: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if estimate := estimator.Estimate(input); estimate.Valid {
				t.Fatalf("invalid input produced estimate: %+v", estimate)
			}
		})
	}
}

func TestStaticProfileRejectsNonMonotonicGrid(t *testing.T) {
	profile := testStaticProfile(true)
	profile.Prefill[0][1] = profile.Prefill[0][0] + time.Millisecond
	if _, err := NewStaticEstimator(profile); err == nil {
		t.Fatal("cache-ratio regression was accepted")
	}
	profile = testStaticProfile(true)
	profile.Prefill[1][0] = profile.Prefill[0][0] - time.Millisecond
	if _, err := NewStaticEstimator(profile); err == nil {
		t.Fatal("prompt monotonicity regression was accepted")
	}
}

func TestStaticProfileSeparatesModelCapacityFromCalibratedPromptRange(t *testing.T) {
	profile := testStaticProfile(true)
	profile.MaxPromptTokens = 3072
	profile.PromptTokenBreakpoints = []int64{1024, 2048, 3072}
	profile.Prefill = profile.Prefill[:3]
	estimator, err := NewStaticEstimator(profile)
	if err != nil {
		t.Fatal(err)
	}
	if estimate := estimator.Estimate(StaticInput{
		Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 3072}, Load: LoadWork{Valid: true},
	}); !estimate.Valid {
		t.Fatalf("calibrated upper bound rejected: %+v", estimate)
	}
	if estimate := estimator.Estimate(StaticInput{
		Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 3073}, Load: LoadWork{Valid: true},
	}); estimate.Valid || estimate.Reason != StaticReasonOutOfRange {
		t.Fatalf("uncalibrated prompt range accepted: %+v", estimate)
	}
}

func TestStaticEstimatorAddsLocalDeltaAndRejectsUncalibratedLoadRange(t *testing.T) {
	profile := testStaticProfile(true)
	profile.LocalDeltaPerRequest = 3 * time.Millisecond
	profile.LoadBounds = &StaticLoadBounds{MaxQueueDepth: 0, MaxRunning: 8, MaxLocalDelta: 4, MaxLocalFallback: 10}
	estimator, err := NewStaticEstimator(profile)
	if err != nil {
		t.Fatal(err)
	}
	estimate := estimator.Estimate(StaticInput{
		Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 1024, CachedPrefixTokens: 1024},
		Load: LoadWork{Running: 1, LocalDelta: 2, Valid: true},
	})
	if !estimate.Valid || estimate.TTFT != 31*time.Millisecond {
		t.Fatalf("local-delta estimate = %+v", estimate)
	}
	for _, load := range []LoadWork{{QueueDepth: 1, Valid: true}, {Running: 9, Valid: true}, {LocalDelta: 5, Valid: true}, {LocalInflight: 11}} {
		if estimate := estimator.Estimate(StaticInput{Identity: testProfileIdentity(), Prompt: PromptWork{PromptTokens: 1024}, Load: load}); estimate.Valid || estimate.Reason != StaticReasonOutOfRange {
			t.Fatalf("out-of-range load accepted: load=%+v estimate=%+v", load, estimate)
		}
	}
}

func newTestStaticEstimator(t *testing.T, calibrated bool) *StaticEstimator {
	t.Helper()
	estimator, err := NewStaticEstimator(testStaticProfile(calibrated))
	if err != nil {
		t.Fatal(err)
	}
	return estimator
}

func testStaticProfile(calibrated bool) StaticProfile {
	return StaticProfile{
		Identity: testProfileIdentity(), Version: "test-v1", Calibrated: calibrated,
		PromptTokenBreakpoints: []int64{1024, 2048, 4096},
		CachedRatioBreakpoints: []int64{0, 5000, 10000},
		Prefill: [][]time.Duration{
			{80 * time.Millisecond, 40 * time.Millisecond, 10 * time.Millisecond},
			{160 * time.Millisecond, 80 * time.Millisecond, 20 * time.Millisecond},
			{320 * time.Millisecond, 160 * time.Millisecond, 40 * time.Millisecond},
		},
		QueueWaitPerRequest: 20 * time.Millisecond, RunningDelayPerRequest: 10 * time.Millisecond,
		LocalFallbackPerRequest: 5 * time.Millisecond, SafetyMargin: 5 * time.Millisecond,
	}
}

func testProfileIdentity() ProfileIdentity {
	return ProfileIdentity{Model: "qwen2.5-0.5b-instruct", HardwareProfile: "rtx4060-timeslice", VLLMVersion: "0.23.0", MinPromptTokens: 1024, MaxModelTokens: 4096}
}
