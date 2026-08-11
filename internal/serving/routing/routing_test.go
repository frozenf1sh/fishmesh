package routing

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// testBackends 提供两个最简后端的公共测试夹具：
// a/b 的 URL 满足 backend.Validate 的最小要求。
func testBackends() []backend.Backend {
	return []backend.Backend{{ID: "a", URL: "http://a:8000"}, {ID: "b", URL: "http://b:8000"}}
}

// TestSessionKeyIsStable 验证同一 session key 反复选择始终落在同一后端。
func TestSessionKeyIsStable(t *testing.T) {
	strategy, err := NewSessionKey(testSessionKeyConfig())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Backends: testBackends()}
	first, err := strategy.Select("same-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		current, selectErr := strategy.Select("same-session", snapshot)
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		if current.Backend.ID != first.Backend.ID || current.Reason != ReasonSessionKeyHit {
			t.Fatalf("unstable decision: first=%+v current=%+v", first, current)
		}
	}
}

// TestLoadBalancedSelectsLeastInflight 验证普通负载均衡选中本地在途请求最少的后端。
func TestLoadBalancedSelectsLeastInflight(t *testing.T) {
	strategy := NewLoadBalanced()
	decision, err := strategy.Select("same-prefix", Snapshot{
		Backends: testBackends(),
		Inflight: map[backend.ID]int64{"a": 4, "b": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Reason != ReasonLoadBalanced {
		t.Fatalf("decision = %+v, want backend b", decision)
	}
}

// TestLoadBalancedExcludesIneligibleBackend 验证被熔断（ineligible）的后端
// 即便负载更低也绝不参与选择。
func TestLoadBalancedExcludesIneligibleBackend(t *testing.T) {
	strategy := NewLoadBalanced()
	decision, err := strategy.Select("same-prefix", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 0, "b": 3},
		Ineligible: map[backend.ID]Reason{"a": ReasonCircuitOpen},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, want eligible backend b", decision)
	}
}

// TestNewConfiguredCreatesLoadBalancedByDefault 验证显式配置的空模式仍解析为普通负载均衡。
func TestNewConfiguredCreatesLoadBalancedByDefault(t *testing.T) {
	strategy, err := NewConfigured(Config{Mode: "", Service: backend.Backend{ID: "service", URL: "http://service:8000"}})
	if err != nil {
		t.Fatal(err)
	}
	if strategy.Name() != ModeLoadBalanced {
		t.Fatalf("strategy name = %q, want %q", strategy.Name(), ModeLoadBalanced)
	}
}

func testSessionKeyConfig() SessionKeyConfig {
	return SessionKeyConfig{
		TTL:             5 * time.Minute,
		MaxEntries:      10_000,
		InflightDelta:   2,
		QueueDepthDelta: 1,
		Clock:           time.Now,
	}
}

func testKVAwareConfig() KVAwareConfig {
	return KVAwareConfig{QueueTokenPenalty: 512, RunningTokenPenalty: 128, InflightTokenPenalty: 64}
}
