package routing

import (
	"sync"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
)

// newSessionKeyForTest 构造一个小容量（MaxEntries=4）、容忍阈值都是 1 的测试策略。
// clock 允许注入固定时间，用于确定性验证过期行为。
func newSessionKeyForTest(t *testing.T, clock func() time.Time) *sessionKeyStrategy {
	t.Helper()
	strategy, err := NewSessionKey(SessionKeyConfig{
		TTL: time.Minute, MaxEntries: 4, InflightDelta: 1,
		QueueDepthDelta: 1, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return strategy.(*sessionKeyStrategy)
}

// TestSessionKeyKeepsPreferenceWithinBounds 验证：压力在容忍范围内时，
// 同一 key 始终命中同一个偏好后端，且决策的溯源字段（PreferredBackendID/Policy）完整。
func TestSessionKeyKeepsPreferenceWithinBounds(t *testing.T) {
	strategy := newSessionKeyForTest(t, func() time.Time { return time.Unix(1, 0) })
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 0, "b": 0}}
	first, err := strategy.Select("session-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := strategy.Select("session-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Backend.ID != second.Backend.ID || second.Reason != ReasonSessionKeyHit {
		t.Fatalf("preference was not retained: first=%+v second=%+v", first, second)
	}
	if second.PreferredBackendID != second.Backend.ID || second.Policy != PolicySessionKeyV1 {
		t.Fatalf("decision provenance is incomplete: %+v", second)
	}
}

// TestSessionKeySpillsWithoutRewritingPreference 验证溢出的核心语义：
// 压力超限时临时去别的后端（SpilloverReason 记录原因），
// 但压力消退后自动回到原偏好后端——溢出不重写记忆。
func TestSessionKeySpillsWithoutRewritingPreference(t *testing.T) {
	strategy := newSessionKeyForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{}}
	first, err := strategy.Select("hot-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	other := backend.ID("a")
	if first.Backend.ID == other {
		other = "b"
	}
	// 给偏好后端制造本地压力，超过 InflightDelta=1。
	snapshot.Inflight[first.Backend.ID] = 3
	snapshot.Inflight[other] = 0
	spilled, err := strategy.Select("hot-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if spilled.Backend.ID != other || spilled.PreferredBackendID != first.Backend.ID || spilled.Reason != ReasonSessionKeySpillover || spilled.SpilloverReason != ReasonLocalInflight {
		t.Fatalf("unexpected spillover: %+v", spilled)
	}
	// 压力消退后应恢复原偏好。
	snapshot.Inflight[first.Backend.ID] = 0
	resumed, err := strategy.Select("hot-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Backend.ID != first.Backend.ID || resumed.Reason != ReasonSessionKeyHit {
		t.Fatalf("spillover rewrote the preferred backend: %+v", resumed)
	}
}

// TestSessionKeyUsesOnlyFreshCompleteQueueSnapshot 验证队列观测的门槛：
// 只有所有合格后端都有有效队列数据时才比较队列；
// 一旦有任何一方的观测缺失/降级，队列维度整体失效，不能影响决策。
func TestSessionKeyUsesOnlyFreshCompleteQueueSnapshot(t *testing.T) {
	strategy := newSessionKeyForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{}}
	first, err := strategy.Select("queue-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	other := backend.ID("a")
	if first.Backend.ID == other {
		other = "b"
	}
	// 全员队列观测有效：偏好后端队列明显更深，应溢出。
	snapshot.Observations = map[backend.ID]observation.Backend{
		first.Backend.ID: {Status: observation.StatusOK, QueueLength: observation.Sample[float64]{Value: 4, Valid: true}},
		other:            {Status: observation.StatusOK, QueueLength: observation.Sample[float64]{Value: 0, Valid: true}},
	}
	spilled, err := strategy.Select("queue-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if spilled.Backend.ID != other || spilled.SpilloverReason != ReasonQueueDepth {
		t.Fatalf("queue pressure did not spill: %+v", spilled)
	}
	// 把 one 方的观测改成无效：部分/过期数据不得影响路由，应留在偏好后端。
	snapshot.Observations[other] = observation.Backend{Status: observation.StatusDegraded, QueueLength: observation.Sample[float64]{Value: 0, Valid: false}}
	ignored, err := strategy.Select("queue-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Backend.ID != first.Backend.ID || ignored.SpilloverReason != "" {
		t.Fatalf("partial/stale queue data influenced routing: %+v", ignored)
	}
}

// TestSessionKeyCircuitSpillDoesNotRewritePreference 验证熔断溢出
// 同样不重写记忆：熔断恢复后请求回到原偏好后端。
func TestSessionKeyCircuitSpillDoesNotRewritePreference(t *testing.T) {
	strategy := newSessionKeyForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{}}
	first, err := strategy.Select("circuit-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Ineligible = map[backend.ID]Reason{first.Backend.ID: ReasonCircuitOpen}
	spilled, err := strategy.Select("circuit-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if spilled.Backend.ID == first.Backend.ID || spilled.PreferredBackendID != first.Backend.ID || spilled.SpilloverReason != ReasonCircuitOpen {
		t.Fatalf("unexpected circuit spillover: %+v", spilled)
	}
	delete(snapshot.Ineligible, first.Backend.ID)
	resumed, err := strategy.Select("circuit-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Backend.ID != first.Backend.ID || resumed.Reason != ReasonSessionKeyHit {
		t.Fatalf("circuit spillover rewrote session-key: %+v", resumed)
	}
}

// TestSessionKeyReconcileRemovesDeletedPreference 验证后端列表被清空时，
// ReconcileBackends 会清掉全部指向已下线后端的记忆。
func TestSessionKeyReconcileRemovesDeletedPreference(t *testing.T) {
	strategy := newSessionKeyForTest(t, time.Now)
	if _, err := strategy.Select("deleted-session", Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{}}); err != nil {
		t.Fatal(err)
	}
	strategy.ReconcileBackends(nil)
	if len(strategy.entries) != 0 {
		t.Fatalf("deleted endpoint session-key state remains: %d", len(strategy.entries))
	}
}

// TestSessionKeyMissingKeyUsesLeastLoadedWithoutEntry 验证空 key 的请求
// 走最少负载路径，且不会在记忆表里留下任何条目。
func TestSessionKeyMissingKeyUsesLeastLoadedWithoutEntry(t *testing.T) {
	strategy := newSessionKeyForTest(t, time.Now)
	decision, err := strategy.Select("", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 3, "b": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Reason != ReasonMissingSessionKeyLoadBalanced || len(strategy.entries) != 0 {
		t.Fatalf("unexpected missing-key decision: %+v entries=%d", decision, len(strategy.entries))
	}
}

// TestSessionKeyExpiresAndBoundsRegistry 验证记忆表的两个上限机制：
// 条目数不超过 MaxEntries，且条目在 TTL 之后不可再用。
func TestSessionKeyExpiresAndBoundsRegistry(t *testing.T) {
	now := time.Unix(1, 0)
	strategy := newSessionKeyForTest(t, func() time.Time { return now })
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{}}
	// 塞入超过 MaxEntries=4 的 key，触发驱逐逻辑。
	for _, key := range []string{"one", "two", "three", "four", "five"} {
		if _, err := strategy.Select(key, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if len(strategy.entries) != strategy.config.MaxEntries {
		t.Fatalf("registry entries = %d, want %d", len(strategy.entries), strategy.config.MaxEntries)
	}
	// 取一个仍在表中的 key，验证时钟推进超过 TTL 后它失效。
	var retainedKey [32]byte
	for key := range strategy.entries {
		retainedKey = key
		break
	}
	now = now.Add(2 * time.Minute)
	if _, ok := strategy.preferred(retainedKey, snapshot.Backends); ok {
		t.Fatal("expired session-key entry remained usable")
	}
}

// TestSessionKeyConcurrentSelection 用多个 goroutine 并发触发选择，
// 由 -race 检测记忆表的锁保护是否完整。
func TestSessionKeyConcurrentSelection(t *testing.T) {
	strategy := newSessionKeyForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[backend.ID]int64{}}
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for request := 0; request < 100; request++ {
				if _, err := strategy.Select("shared", snapshot); err != nil {
					t.Errorf("Select() error = %v", err)
					return
				}
			}
		}()
	}
	workers.Wait()
}
