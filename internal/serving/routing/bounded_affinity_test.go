package routing

import (
	"sync"
	"testing"
	"time"
)

func newBoundedForTest(t *testing.T, clock func() time.Time) *boundedAffinityStrategy {
	t.Helper()
	strategy, err := NewBoundedAffinity(BoundedAffinityConfig{
		TTL: time.Minute, MaxEntries: 4, InflightDelta: 1,
		QueueDepthDelta: 1, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return strategy.(*boundedAffinityStrategy)
}

func TestBoundedAffinityKeepsPreferenceWithinBounds(t *testing.T) {
	strategy := newBoundedForTest(t, func() time.Time { return time.Unix(1, 0) })
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[string]int64{"a": 0, "b": 0}}
	first, err := strategy.Select("session-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := strategy.Select("session-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Backend.ID != second.Backend.ID || second.Reason != "affinity-hit" {
		t.Fatalf("preference was not retained: first=%+v second=%+v", first, second)
	}
	if second.PreferredBackendID != second.Backend.ID || second.Policy != BoundedAffinityPolicyV1 {
		t.Fatalf("decision provenance is incomplete: %+v", second)
	}
}

func TestBoundedAffinitySpillsWithoutRewritingPreference(t *testing.T) {
	strategy := newBoundedForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[string]int64{}}
	first, err := strategy.Select("hot-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	other := "a"
	if first.Backend.ID == other {
		other = "b"
	}
	snapshot.Inflight[first.Backend.ID] = 3
	snapshot.Inflight[other] = 0
	spilled, err := strategy.Select("hot-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if spilled.Backend.ID != other || spilled.PreferredBackendID != first.Backend.ID || spilled.Reason != "affinity-spillover" || spilled.SpilloverReason != "local-inflight" {
		t.Fatalf("unexpected spillover: %+v", spilled)
	}
	snapshot.Inflight[first.Backend.ID] = 0
	resumed, err := strategy.Select("hot-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Backend.ID != first.Backend.ID || resumed.Reason != "affinity-hit" {
		t.Fatalf("spillover rewrote the preferred backend: %+v", resumed)
	}
}

func TestBoundedAffinityUsesOnlyFreshCompleteQueueSnapshot(t *testing.T) {
	strategy := newBoundedForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[string]int64{}}
	first, err := strategy.Select("queue-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	other := "a"
	if first.Backend.ID == other {
		other = "b"
	}
	snapshot.Observations = map[string]BackendObservation{
		first.Backend.ID: {Status: ObservationOK, QueueLength: Sample[float64]{Value: 4, Valid: true}},
		other:            {Status: ObservationOK, QueueLength: Sample[float64]{Value: 0, Valid: true}},
	}
	spilled, err := strategy.Select("queue-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if spilled.Backend.ID != other || spilled.SpilloverReason != "queue-depth" {
		t.Fatalf("queue pressure did not spill: %+v", spilled)
	}
	snapshot.Observations[other] = BackendObservation{Status: ObservationDegraded, QueueLength: Sample[float64]{Value: 0, Valid: false}}
	ignored, err := strategy.Select("queue-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Backend.ID != first.Backend.ID || ignored.SpilloverReason != "" {
		t.Fatalf("partial/stale queue data influenced routing: %+v", ignored)
	}
}

func TestBoundedAffinityCircuitSpillDoesNotRewritePreference(t *testing.T) {
	strategy := newBoundedForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[string]int64{}}
	first, err := strategy.Select("circuit-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Ineligible = map[string]string{first.Backend.ID: "circuit-open"}
	spilled, err := strategy.Select("circuit-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if spilled.Backend.ID == first.Backend.ID || spilled.PreferredBackendID != first.Backend.ID || spilled.SpilloverReason != "circuit-open" {
		t.Fatalf("unexpected circuit spillover: %+v", spilled)
	}
	delete(snapshot.Ineligible, first.Backend.ID)
	resumed, err := strategy.Select("circuit-session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Backend.ID != first.Backend.ID || resumed.Reason != "affinity-hit" {
		t.Fatalf("circuit spillover rewrote affinity: %+v", resumed)
	}
}

func TestBoundedAffinityReconcileRemovesDeletedPreference(t *testing.T) {
	strategy := newBoundedForTest(t, time.Now)
	if _, err := strategy.Select("deleted-session", Snapshot{Backends: testBackends(), Inflight: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	strategy.ReconcileBackends(nil)
	if len(strategy.entries) != 0 {
		t.Fatalf("deleted endpoint affinity state remains: %d", len(strategy.entries))
	}
}

func TestBoundedAffinityMissingKeyUsesLeastLoadedWithoutEntry(t *testing.T) {
	strategy := newBoundedForTest(t, time.Now)
	decision, err := strategy.Select("", Snapshot{
		Backends: testBackends(), Inflight: map[string]int64{"a": 3, "b": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Reason != "missing-key-least-loaded" || len(strategy.entries) != 0 {
		t.Fatalf("unexpected missing-key decision: %+v entries=%d", decision, len(strategy.entries))
	}
}

func TestBoundedAffinityExpiresAndBoundsRegistry(t *testing.T) {
	now := time.Unix(1, 0)
	strategy := newBoundedForTest(t, func() time.Time { return now })
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[string]int64{}}
	for _, key := range []string{"one", "two", "three", "four", "five"} {
		if _, err := strategy.Select(key, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if len(strategy.entries) != strategy.config.MaxEntries {
		t.Fatalf("registry entries = %d, want %d", len(strategy.entries), strategy.config.MaxEntries)
	}
	var retainedKey [32]byte
	for key := range strategy.entries {
		retainedKey = key
		break
	}
	now = now.Add(2 * time.Minute)
	if _, ok := strategy.preferred(retainedKey, snapshot.Backends); ok {
		t.Fatal("expired affinity entry remained usable")
	}
}

func TestBoundedAffinityConcurrentSelection(t *testing.T) {
	strategy := newBoundedForTest(t, time.Now)
	snapshot := Snapshot{Backends: testBackends(), Inflight: map[string]int64{}}
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
