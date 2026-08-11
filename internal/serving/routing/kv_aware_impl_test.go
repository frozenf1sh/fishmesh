package routing

import (
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

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
// 成本最小者 a 胜出，且策略版本标记为 V2。
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
