package kvcache

import (
	"context"
	"errors"
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestVLLMStoreMatchesCacheSaltAndConsumesRemoval(t *testing.T) {
	store, err := newVLLMStore(testConfig())
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
	store, err := newVLLMStore(testConfig())
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

func TestVLLMStoreAcceptsSingleFullAttentionGroup(t *testing.T) {
	store, err := newVLLMStore(testConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	group := 0
	event := &kvevents.BlockStoredEvent{
		BlockHashes:     []uint64{101, 102},
		Tokens:          sequentialTokens(32),
		BlockSize:       16,
		DeviceTier:      "gpu",
		GroupIdx:        &group,
		KVCacheSpecKind: kvevents.KVCacheSpecKindFullAttention,
	}
	if err := store.applyOne(context.Background(), "pod-a", "qwen", event); err != nil {
		t.Fatalf("single full-attention group must be compatible: %v", err)
	}
	if err := store.applyOne(context.Background(), "pod-a", "qwen", &kvevents.BlockRemovedEvent{
		BlockHashes: []uint64{101, 102}, DeviceTier: "gpu", GroupIdx: &group,
	}); err != nil {
		t.Fatalf("single full-attention group removal must be compatible: %v", err)
	}
}

func TestVLLMStoreRejectsNonCanonicalGroupSemantics(t *testing.T) {
	store, err := newVLLMStore(testConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	nonZeroGroup := 1
	slidingWindowGroup := 0
	slidingWindow := 128
	for name, event := range map[string]*kvevents.BlockStoredEvent{
		"non-zero group": {
			BlockHashes: []uint64{101, 102}, Tokens: sequentialTokens(32), BlockSize: 16, DeviceTier: "gpu",
			GroupIdx: &nonZeroGroup, KVCacheSpecKind: kvevents.KVCacheSpecKindFullAttention,
		},
		"sliding window": {
			BlockHashes: []uint64{201, 202}, Tokens: sequentialTokens(32), BlockSize: 16, DeviceTier: "gpu",
			GroupIdx: &slidingWindowGroup, KVCacheSpecKind: kvevents.KVCacheSpecKindSlidingWindow, KVCacheSpecSlidingWindowSize: &slidingWindow,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := store.applyOne(context.Background(), "pod-a", "qwen", event)
			var fault *eventFault
			if !errors.As(err, &fault) || fault.reason != ReasonUnsupportedEvent {
				t.Fatalf("unsupported group semantics were not typed: %T %v", err, err)
			}
		})
	}
}

func TestVLLMStoreRejectsExistingEngineRequestKeyMismatch(t *testing.T) {
	store, err := newVLLMStore(testConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	first := &kvevents.BlockStoredEvent{
		BlockHashes: []uint64{101}, Tokens: sequentialTokens(16), BlockSize: 16, DeviceTier: "gpu",
	}
	if err := store.applyOne(context.Background(), "pod-a", "qwen", first); err != nil {
		t.Fatalf("apply first stored event: %v", err)
	}
	secondTokens := sequentialTokens(16)
	secondTokens[0]++
	err = store.applyOne(context.Background(), "pod-a", "qwen", &kvevents.BlockStoredEvent{
		BlockHashes: []uint64{101}, Tokens: secondTokens, BlockSize: 16, DeviceTier: "gpu",
	})
	var fault *eventFault
	if !errors.As(err, &fault) || fault.reason != ReasonEngineRequestKeyMismatch {
		t.Fatalf("existing engine/request mismatch was not rejected: %T %v", err, err)
	}
}

func TestVLLMStoreStopsHashingAfterFirstQueryMiss(t *testing.T) {
	processor := &recordingTokenProcessor{blockSize: 2}
	index := &recordingIndex{hitKeys: map[kvblock.BlockHash]bool{1: true}}
	store := &vllmStore{blockSize: 2, tokenProcessor: processor, index: index}

	keys, keyToPods, err := store.lookupRequestKeysUntilMiss(context.Background(), []uint32{1, 2, 3, 4, 5, 6}, "qwen", "", []string{"pod-a"})
	if err != nil {
		t.Fatalf("lookup request keys: %v", err)
	}
	if processor.calls != 2 || len(index.lookups) != 2 || len(keys) != 1 || len(keyToPods) != 1 {
		t.Fatalf("query hash chain was not short-circuited: calls=%d lookups=%d keys=%v keyToPods=%v", processor.calls, len(index.lookups), keys, keyToPods)
	}
}

type recordingTokenProcessor struct {
	blockSize int
	calls     int
}

func (p *recordingTokenProcessor) TokensToKVBlockKeys(_ kvblock.BlockHash, _ []uint32, _ string, _ []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
	p.calls++
	return []kvblock.BlockHash{kvblock.BlockHash(p.calls)}, nil
}

func (p *recordingTokenProcessor) BlockSize() int { return p.blockSize }

type recordingIndex struct {
	hitKeys map[kvblock.BlockHash]bool
	lookups [][]kvblock.BlockHash
}

func (i *recordingIndex) Lookup(_ context.Context, keys []kvblock.BlockHash, _ sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error) {
	i.lookups = append(i.lookups, append([]kvblock.BlockHash(nil), keys...))
	if len(keys) == 1 && i.hitKeys[keys[0]] {
		return map[kvblock.BlockHash][]kvblock.PodEntry{keys[0]: {{PodIdentifier: "pod-a"}}}, nil
	}
	return map[kvblock.BlockHash][]kvblock.PodEntry{}, nil
}

func (*recordingIndex) Add(context.Context, []kvblock.BlockHash, []kvblock.BlockHash, []kvblock.PodEntry) error {
	return nil
}

func (*recordingIndex) Evict(context.Context, kvblock.BlockHash, kvblock.KeyType, []kvblock.PodEntry) error {
	return nil
}

func (*recordingIndex) GetRequestKey(context.Context, kvblock.BlockHash) (kvblock.BlockHash, error) {
	return kvblock.EmptyBlockHash, errors.New("not found")
}

func (*recordingIndex) Clear(context.Context, string) error { return nil }

func sequentialTokens(count int) []uint32 {
	tokens := make([]uint32, count)
	for index := range tokens {
		tokens[index] = uint32(index)
	}
	return tokens
}
