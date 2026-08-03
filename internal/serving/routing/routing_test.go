package routing

import (
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func testBackends() []backend.Backend {
	return []backend.Backend{{ID: "a", URL: "http://a:8000"}, {ID: "b", URL: "http://b:8000"}}
}

func TestPrefixAffinityIsStable(t *testing.T) {
	strategy := NewPrefixAffinity()
	snapshot := Snapshot{Backends: testBackends()}
	first, err := strategy.Select("same-prefix", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		current, selectErr := strategy.Select("same-prefix", snapshot)
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		if current.Backend.ID != first.Backend.ID || current.Reason != ReasonPrefixAffinity {
			t.Fatalf("unstable decision: first=%+v current=%+v", first, current)
		}
	}
}

func TestLoadAwareSelectsLeastInflight(t *testing.T) {
	strategy := NewLoadAware()
	decision, err := strategy.Select("same-prefix", Snapshot{
		Backends: testBackends(),
		Inflight: map[backend.ID]int64{"a": 4, "b": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" || decision.Reason != ReasonLeastInflight {
		t.Fatalf("decision = %+v, want backend b", decision)
	}
}

func TestLoadAwareExcludesIneligibleBackend(t *testing.T) {
	strategy := NewLoadAware()
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

func TestNewAcceptsLegacyPrefixHashAlias(t *testing.T) {
	strategy, err := New(ModePrefixHash, backend.Backend{ID: "service", URL: "http://service:8000"})
	if err != nil {
		t.Fatal(err)
	}
	if strategy.Name() != ModePrefixAffinity {
		t.Fatalf("strategy name = %q, want %q", strategy.Name(), ModePrefixAffinity)
	}
}
