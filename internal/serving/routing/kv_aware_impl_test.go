package routing

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestKVAwareStaticEstimateCanOverrideTokenCost(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{EstimatorMode: KVAwareEstimatorStatic, QueueTokenPenalty: 64})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("", Snapshot{
		Backends: testBackends(), Loads: map[backend.ID]Load{"a": {Valid: true}, "b": {Valid: true}},
		KVAware: KVAwareInput{PromptTokens: 128, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 128}, "b": {Valid: true, MatchedTokens: 0},
		}},
		Estimates: map[backend.ID]LatencyEstimate{
			"a": {TTFT: 200 * time.Millisecond, Valid: true, Confidence: EstimateConfidenceCalibrated, Version: "v1"},
			"b": {TTFT: 100 * time.Millisecond, Valid: true, Confidence: EstimateConfidenceCalibrated, Version: "v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Policy != PolicyKVAwareStaticV1 || decision.Reason != ReasonKVAwareStatic {
		t.Fatalf("decision = %+v, want static TTFT selection of b", decision)
	}
}

func TestKVAwareStaticEstimateAllowsDegradedLoadFallback(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{EstimatorMode: KVAwareEstimatorStatic})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("", Snapshot{
		Backends: testBackends(),
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true}, "b": {Valid: true},
		}},
		Estimates: map[backend.ID]LatencyEstimate{
			"a": {TTFT: 20 * time.Millisecond, Valid: true, Confidence: EstimateConfidenceDegraded, Version: "v1"},
			"b": {TTFT: 30 * time.Millisecond, Valid: true, Confidence: EstimateConfidenceCalibrated, Version: "v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "a" || decision.Policy != PolicyKVAwareStaticV1 {
		t.Fatalf("decision = %+v, degraded local-load estimate should remain usable", decision)
	}
}

func TestKVAwareStaticFallsBackToTokenCostWhenEstimateIsIncomplete(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{EstimatorMode: KVAwareEstimatorStatic})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("", Snapshot{
		Backends: testBackends(),
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 16}, "b": {Valid: true},
		}},
		Estimates: map[backend.ID]LatencyEstimate{
			"a": {TTFT: 20 * time.Millisecond, Valid: true, Confidence: EstimateConfidenceCalibrated, Version: "v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "a" || decision.Policy != PolicyKVAwareV1 || decision.Reason != ReasonKVAwareStaticFallback {
		t.Fatalf("decision = %+v, want typed token-cost fallback", decision)
	}
}

func TestKVAwareStaticCannotBypassHardOverload(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{EstimatorMode: KVAwareEstimatorStatic})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("", Snapshot{
		Backends: testBackends(), Loads: map[backend.ID]Load{"a": {HardOverload: true}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 16}, "b": {Valid: true},
		}},
		Estimates: map[backend.ID]LatencyEstimate{
			"a": {TTFT: time.Millisecond, Valid: true, Confidence: EstimateConfidenceCalibrated, Version: "v1"},
			"b": {TTFT: 100 * time.Millisecond, Valid: true, Confidence: EstimateConfidenceCalibrated, Version: "v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Policy != PolicyKVAwareStaticV1 {
		t.Fatalf("decision = %+v, hard-overloaded estimate bypassed safety gate", decision)
	}
}

// TestKVAwareInputDistinguishesUnknownFromZeroMatch 验证 KVAwareInput.UsableFor
// 能区分两种本质不同的情况：真实的零命中（Valid=true, Matched=0）可用，
// 而未知/过期信号（Valid=false）绝不能让 KV-aware 策略参与决策。
func TestKVAwareInputDistinguishesUnknownFromZeroMatch(t *testing.T) {
	backends := testBackends()
	zero := KVAwareInput{PromptTokens: 12, Matches: map[backend.ID]KVMatch{
		"a": {Valid: true, MatchedTokens: 0}, "b": {Valid: true, MatchedTokens: 8},
	}}
	if !zero.UsableFor(backends) {
		t.Fatal("valid zero match must remain usable for KV-aware routing")
	}
	unknown := zero
	unknown.Matches = map[backend.ID]KVMatch{"a": {Valid: false}, "b": {Valid: true, MatchedTokens: 8}}
	if unknown.UsableFor(backends) {
		t.Fatal("unknown match must not participate in KV-aware routing")
	}
}

// TestKVAwarePrefersFewestUncachedThenLoad 验证成本式比较的基本行为：
// a 未缓存 4 token、无队列压力，b 未缓存 8 token 且队列堆积，
// 成本最小者 a 胜出，且策略版本保持可回滚的 V1 contract。
func TestKVAwarePrefersFewestUncachedThenLoad(t *testing.T) {
	strategy, err := NewConfiguredKVAware(testKVAwareConfig())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 4, "b": 1},
		Loads:   map[backend.ID]Load{"a": {Valid: true, QueueDepth: 0, Running: 0}, "b": {Valid: true, QueueDepth: 8, Running: 4}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{"a": {Valid: true, MatchedTokens: 12}, "b": {Valid: true, MatchedTokens: 8}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "a" || decision.Reason != ReasonKVAware || decision.Policy != PolicyKVAwareV1 {
		t.Fatalf("decision = %+v, want KV-aware selection of a", decision)
	}
}

// TestKVAwareCostLetsKnownQueueOutweighCacheBenefit 验证成本量纲统一的意义：
// a 虽然 KV 全命中（未缓存 0），但排了 2 个请求 × 64 penalty = 128 token 成本，
// 超过 b 的未缓存 96 token，因此已知队列压力可以压过缓存收益。
func TestKVAwareCostLetsKnownQueueOutweighCacheBenefit(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{QueueTokenPenalty: 64, RunningTokenPenalty: 0, InflightTokenPenalty: 0})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(),
		Loads: map[backend.ID]Load{
			"a": {Valid: true, QueueDepth: 2},
			"b": {Valid: true},
		},
		KVAware: KVAwareInput{PromptTokens: 128, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 128},
			"b": {Valid: true, MatchedTokens: 32},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Policy != PolicyKVAwareV1 {
		t.Fatalf("decision = %+v, want queue-aware selection of b", decision)
	}
}

// TestKVAwareDoesNotInventUnknownLoadAsZero 验证未知外部负载不被当作零：
// a 的 Load.Valid=false 时，它的 queue/running 项不贡献成本（也不被"发明"惩罚），
// 只能靠本地 in-flight 参与比较——绝不能因为"看起来零负载"而吸走流量。
func TestKVAwareDoesNotInventUnknownLoadAsZero(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{QueueTokenPenalty: 64, RunningTokenPenalty: 64, InflightTokenPenalty: 32})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 1},
		Loads: map[backend.ID]Load{"a": {Valid: false}, "b": {Valid: true, QueueDepth: 1}},
		KVAware: KVAwareInput{PromptTokens: 128, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 128}, "b": {Valid: true, MatchedTokens: 128},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "a" {
		t.Fatalf("decision = %+v, unknown external load must not receive an invented penalty", decision)
	}
}

func TestKVAwareAddsOnlyLocalDeltaWhenExternalLoadIsValid(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{InflightTokenPenalty: 64})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 100},
		Loads: map[backend.ID]Load{"a": {Valid: true, Running: 100}, "b": {Valid: true}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 8}, "b": {Valid: true, MatchedTokens: 8},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "a" {
		t.Fatalf("decision = %+v, sampled running must suppress duplicate local cost", decision)
	}
	decision, err = strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 2},
		Loads: map[backend.ID]Load{"a": {Valid: true, LocalDelta: 2}, "b": {Valid: true}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 8}, "b": {Valid: true, MatchedTokens: 8},
		}},
	})
	if err != nil || decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, err=%v; unsampled local delta must contribute", decision, err)
	}
}

func TestKVAwareUsesLocalInflightWhenExternalLoadIsUnknown(t *testing.T) {
	strategy, err := NewConfiguredKVAware(KVAwareConfig{InflightTokenPenalty: 64})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 2},
		Loads: map[backend.ID]Load{"a": {Valid: false}, "b": {Valid: false}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 8}, "b": {Valid: true, MatchedTokens: 8},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, unknown external load must retain local fallback", decision)
	}
}

// TestKVAwareConfigRejectsNegativePenalty 验证负 penalty 在构造时被拒绝——
// 负数会把压力变成负成本，毁掉最小成本的语义。
func TestKVAwareConfigRejectsNegativePenalty(t *testing.T) {
	if _, err := NewConfiguredKVAware(KVAwareConfig{QueueTokenPenalty: -1}); err == nil {
		t.Fatal("negative penalty was accepted")
	}
}

// TestKVAwareUsesLoadAndSessionOnlyForKVAwareTies 验证会话哈希只用于平局消解：
// 两个后端未缓存 token 相同（都是 8），最终按成本（含队列/in-flight）选出 b；
// routingKey 不能覆盖真实成本差异。
func TestKVAwareUsesLoadAndSessionOnlyForKVAwareTies(t *testing.T) {
	strategy, err := NewConfiguredKVAware(testKVAwareConfig())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 3, "b": 2},
		Loads:   map[backend.ID]Load{"a": {Valid: true, QueueDepth: 5, Running: 1}, "b": {Valid: true, QueueDepth: 1, Running: 3}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{"a": {Valid: true, MatchedTokens: 8}, "b": {Valid: true, MatchedTokens: 8}}},
	}
	decision, err := strategy.Select("session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, want lower queued backend b", decision)
	}
}

// TestKVAwareBreaksTiesDeterministicallyByID 验证成本完全相同时的平局消解：
// a/b 的 cache 与全部 load 维度一致，无论 discovery 顺序如何都选出 ID 更小的 a，
// 且不依赖客户端 routingKey。
func TestKVAwareBreaksTiesDeterministicallyByID(t *testing.T) {
	strategy, err := NewConfiguredKVAware(testKVAwareConfig())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 1, "b": 1},
		Loads: map[backend.ID]Load{"a": {Valid: true}, "b": {Valid: true}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 8}, "b": {Valid: true, MatchedTokens: 8},
		}},
	}
	for _, key := range []string{"", "session", "other-session"} {
		decision, err := strategy.Select(key, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Backend.ID != "a" {
			t.Fatalf("key %q: decision = %+v, want deterministic tie-break to smallest ID a", key, decision)
		}
	}
}

// TestKVAwareExcludesHardOverloadBeforeCacheLocality 验证硬过载优先级
// 高于缓存局部性：a 虽然 KV 全命中，但 HardOverload=true，必须出局。
func TestKVAwareExcludesHardOverloadBeforeCacheLocality(t *testing.T) {
	strategy, err := NewConfiguredKVAware(testKVAwareConfig())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 0, "b": 2},
		Loads:   map[backend.ID]Load{"a": {Valid: true, HardOverload: true}, "b": {Valid: true}},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{"a": {Valid: true, MatchedTokens: 16}, "b": {Valid: true, MatchedTokens: 0}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, want non-overloaded backend b", decision)
	}
}

func TestKVAwareFallsBackWhenAllCandidatesAreHardOverloaded(t *testing.T) {
	strategy, err := NewConfiguredKVAware(testKVAwareConfig())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 1},
		Loads: map[backend.ID]Load{
			"a": {Valid: true, QueueDepth: 4, HardOverload: true},
			"b": {Valid: true, QueueDepth: 1, HardOverload: true},
		},
		KVAware: KVAwareInput{PromptTokens: 16, Matches: map[backend.ID]KVMatch{
			"a": {Valid: true, MatchedTokens: 16}, "b": {Valid: true, MatchedTokens: 0},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Reason != ReasonKVAwareHardOverloadFallback {
		t.Fatalf("decision = %+v, want typed least-loaded hard-overload fallback", decision)
	}
}
