package routing

import (
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestExactInputDistinguishesUnknownFromZeroMatch(t *testing.T) {
	backends := testBackends()
	zero := ExactInput{PromptTokens: 12, Matches: map[backend.ID]CacheMatch{
		"a": {Valid: true, MatchedTokens: 0}, "b": {Valid: true, MatchedTokens: 8},
	}}
	if !zero.UsableFor(backends) {
		t.Fatal("valid zero match must remain usable for exact routing")
	}
	unknown := zero
	unknown.Matches = map[backend.ID]CacheMatch{"a": {Valid: false}, "b": {Valid: true, MatchedTokens: 8}}
	if unknown.UsableFor(backends) {
		t.Fatal("unknown match must not participate in exact routing")
	}
}

func TestExactCacheLoadPrefersFewestUncachedThenLoad(t *testing.T) {
	strategy := NewExactCacheLoad()
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 4, "b": 1},
		Loads: map[backend.ID]Load{"a": {Valid: true, QueueDepth: 0, Running: 0}, "b": {Valid: true, QueueDepth: 8, Running: 4}},
		Exact: ExactInput{PromptTokens: 16, Matches: map[backend.ID]CacheMatch{"a": {Valid: true, MatchedTokens: 12}, "b": {Valid: true, MatchedTokens: 8}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "a" || decision.Reason != ReasonExactCacheLoad || decision.Policy != PolicyExactCacheLoadV1 {
		t.Fatalf("decision = %+v, want exact selection of a", decision)
	}
}

func TestExactCacheLoadUsesLoadAndSessionOnlyForExactTies(t *testing.T) {
	strategy := NewExactCacheLoad()
	snapshot := Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 3, "b": 2},
		Loads: map[backend.ID]Load{"a": {Valid: true, QueueDepth: 5, Running: 1}, "b": {Valid: true, QueueDepth: 1, Running: 3}},
		Exact: ExactInput{PromptTokens: 16, Matches: map[backend.ID]CacheMatch{"a": {Valid: true, MatchedTokens: 8}, "b": {Valid: true, MatchedTokens: 8}}},
	}
	decision, err := strategy.Select("session", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, want lower queued backend b", decision)
	}
}

func TestExactCacheLoadExcludesHardOverloadBeforeCacheLocality(t *testing.T) {
	strategy := NewExactCacheLoad()
	decision, err := strategy.Select("session", Snapshot{
		Backends: testBackends(), Inflight: map[backend.ID]int64{"a": 0, "b": 2},
		Loads: map[backend.ID]Load{"a": {Valid: true, HardOverload: true}, "b": {Valid: true}},
		Exact: ExactInput{PromptTokens: 16, Matches: map[backend.ID]CacheMatch{"a": {Valid: true, MatchedTokens: 16}, "b": {Valid: true, MatchedTokens: 0}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Backend.ID != "b" {
		t.Fatalf("decision = %+v, want non-overloaded backend b", decision)
	}
}
