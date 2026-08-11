package circuit

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestCircuitOpensExpiresAndRecovers(t *testing.T) {
	now := time.Unix(1, 0)
	breaker, err := New(Config{
		EWMAAlpha: 0.5, ErrorThreshold: 0.6, MinimumRequests: 3,
		OpenDuration: 10 * time.Second, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if breaker.Record("a", OutcomeFailure) || breaker.Record("a", OutcomeFailure) {
		t.Fatal("circuit opened before the minimum request count")
	}
	if !breaker.Record("a", OutcomeFailure) || !breaker.IsOpen("a") {
		t.Fatal("three consecutive transport errors should open the circuit")
	}
	now = now.Add(11 * time.Second)
	if breaker.IsOpen("a") {
		t.Fatal("circuit did not expire")
	}
	for range 3 {
		if breaker.Record("a", OutcomeSuccess) {
			t.Fatal("successful requests must not open the circuit")
		}
	}
	if breaker.IsOpen("a") {
		t.Fatal("successful probe window should keep the circuit closed")
	}
}

func TestCircuitReconcileBoundsEndpointState(t *testing.T) {
	breaker, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	breaker.Record("a", OutcomeFailure)
	breaker.Record("removed", OutcomeFailure)
	removed := breaker.Reconcile([]backend.Backend{{ID: "a"}})
	if len(removed) != 1 || removed[0] != "removed" || breaker.Len() != 1 {
		t.Fatalf("removed=%v states=%d", removed, breaker.Len())
	}
}

func TestCircuitIgnoresUnknownOutcome(t *testing.T) {
	breaker, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if breaker.Record("a", "") || breaker.Len() != 0 {
		t.Fatalf("unknown outcome changed circuit state: states=%d", breaker.Len())
	}
}

func testConfig() Config {
	return Config{
		EWMAAlpha:       0.5,
		ErrorThreshold:  0.6,
		MinimumRequests: 3,
		OpenDuration:    10 * time.Second,
		Clock:           time.Now,
	}
}
