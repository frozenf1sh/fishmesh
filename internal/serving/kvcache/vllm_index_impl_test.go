package kvcache

import (
	"context"
	"errors"
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
)

func TestVLLMStoreMatchesCacheSaltAndConsumesRemoval(t *testing.T) {
	store, err := newVLLMStore(DefaultConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	instance := testInstance("backend-a", "uid-a", "10.0.0.1:8000")
	tokens := sequentialTokens(32)
	stored := &kvevents.BlockStoredEvent{
		BlockHashes: []uint64{101, 102},
		Tokens:      tokens,
		BlockSize:   16,
		DeviceTier:  "gpu",
		ExtraKeys:   [][]any{{"tenant-a"}, nil},
	}
	if err := store.applyOne(context.Background(), instance.PodIdentifier, instance.Model, stored); err != nil {
		t.Fatalf("apply stored event: %v", err)
	}

	instances := map[backend.ID]Instance{instance.Backend: instance}
	matched, blocks, err := store.Lookup(context.Background(), Query{
		Model:       instance.Model,
		CacheSalt:   "tenant-a",
		TokenGroups: [][]uint32{tokens},
	}, instances)
	if err != nil {
		t.Fatalf("lookup salted prompt: %v", err)
	}
	if blocks != 2 || matched[instance.Backend] != 2 {
		t.Fatalf("expected two salted blocks, blocks=%d matched=%v", blocks, matched)
	}

	otherSalt, _, err := store.Lookup(context.Background(), Query{
		Model:       instance.Model,
		CacheSalt:   "tenant-b",
		TokenGroups: [][]uint32{tokens},
	}, instances)
	if err != nil {
		t.Fatalf("lookup isolated salt: %v", err)
	}
	if otherSalt[instance.Backend] != 0 {
		t.Fatalf("cache salt isolation failed: %v", otherSalt)
	}

	removed := &kvevents.BlockRemovedEvent{BlockHashes: []uint64{101}, DeviceTier: "gpu"}
	if err := store.applyOne(context.Background(), instance.PodIdentifier, instance.Model, removed); err != nil {
		t.Fatalf("apply removed event: %v", err)
	}
	afterRemoval, _, err := store.Lookup(context.Background(), Query{
		Model:       instance.Model,
		CacheSalt:   "tenant-a",
		TokenGroups: [][]uint32{tokens},
	}, instances)
	if err != nil {
		t.Fatalf("lookup after removal: %v", err)
	}
	if afterRemoval[instance.Backend] != 0 {
		t.Fatalf("removed first block still matched: %v", afterRemoval)
	}
}

func TestVLLMStoreRejectsUnsupportedEngineSemantics(t *testing.T) {
	store, err := newVLLMStore(DefaultConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	event := &kvevents.BlockStoredEvent{
		BlockHashes: []uint64{101},
		Tokens:      sequentialTokens(32),
		BlockSize:   32,
		DeviceTier:  "gpu",
	}
	err = store.applyOne(context.Background(), "pod-a", "qwen", event)
	var fault *eventFault
	if !errors.As(err, &fault) || fault.reason != ReasonUnsupportedEvent {
		t.Fatalf("unsupported block size was not typed: %T %v", err, err)
	}
}

func sequentialTokens(count int) []uint32 {
	tokens := make([]uint32, count)
	for index := range tokens {
		tokens[index] = uint32(index)
	}
	return tokens
}
