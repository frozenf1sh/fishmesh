package discovery

import (
	"context"
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestStaticResolverValidatesAndCopiesSnapshot(t *testing.T) {
	input := []backend.Backend{{ID: "backend-a", URL: "http://backend-a:8000"}}
	resolver, err := NewStatic(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0].URL = "http://mutated:8000"

	snapshot, err := resolver.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot[0].URL != "http://backend-a:8000" {
		t.Fatalf("resolver retained caller slice: %#v", snapshot)
	}
	snapshot[0].URL = "http://mutated-again:8000"
	second, err := resolver.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second[0].URL != "http://backend-a:8000" {
		t.Fatalf("snapshot was not copied: %#v", second)
	}
}

func TestStaticResolverRejectsInvalidBackends(t *testing.T) {
	for name, backends := range map[string][]backend.Backend{
		"empty":       nil,
		"missing ID":  {{URL: "http://backend-a:8000"}},
		"missing URL": {{ID: "backend-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewStatic(backends); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
