package routing

import (
	"testing"
	"time"
)

func TestCircuitOpensExpiresAndRecovers(t *testing.T) {
	now := time.Unix(1, 0)
	registry, err := NewCircuitRegistry(CircuitConfig{
		EWMAAlpha: 0.5, ErrorThreshold: 0.6, MinimumRequests: 3,
		OpenDuration: 10 * time.Second, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Record("a", true) || registry.Record("a", true) {
		t.Fatal("circuit opened before the minimum request count")
	}
	if !registry.Record("a", true) || !registry.IsOpen("a") {
		t.Fatal("three consecutive transport errors should open the circuit")
	}
	now = now.Add(11 * time.Second)
	if registry.IsOpen("a") {
		t.Fatal("circuit did not expire")
	}
	for i := 0; i < 3; i++ {
		if registry.Record("a", false) {
			t.Fatal("successful requests must not open the circuit")
		}
	}
	if registry.IsOpen("a") {
		t.Fatal("successful probe window should keep the circuit closed")
	}
}

func TestCircuitReconcileBoundsEndpointState(t *testing.T) {
	registry, err := NewCircuitRegistry(DefaultCircuitConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry.Record("a", true)
	registry.Record("removed", true)
	removed := registry.Reconcile([]Backend{{ID: "a"}})
	if len(removed) != 1 || removed[0] != "removed" || registry.Len() != 1 {
		t.Fatalf("removed=%v states=%d", removed, registry.Len())
	}
}
