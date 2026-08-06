package kvcache

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"

	upstreamcache "github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents/engineadapter"
	"k8s.io/apimachinery/pkg/util/sets"
)

const supportedDeviceTier = "gpu"

// eventFault 把协议/兼容错误映射为稳定的 instance degradation reason。
type eventFault struct {
	reason Reason
	err    error
}

func (e *eventFault) Error() string { return e.err.Error() }
func (e *eventFault) Unwrap() error { return e.err }

// applyResult 返回事件真正进入 index 后才能发布的观测信息。
type applyResult struct {
	publishedAt time.Time
}

// cacheStore 是 lifecycle owner 依赖的同步 KV 状态边界。
// Apply 返回前事件已经完全落入 index，因此 sequence 可以安全提交。
type cacheStore interface {
	Lookup(context.Context, Query, map[backend.ID]Instance) (map[backend.ID]int, int, error)
	Apply(context.Context, Instance, Event) (applyResult, error)
	Clear(context.Context, string) error
}

// vllmStore 组合上游 parser、canonical hash、bounded index 和 prefix scorer。
// FishMesh 只实现同步 apply 与兼容边界，不重新定义 vLLM msgpack wire format。
type vllmStore struct {
	blockSize      int
	tokenProcessor kvblock.TokenProcessor
	index          kvblock.Index
	scorer         upstreamcache.KVBlockScorer
	adapter        kvevents.EngineAdapter
}

func newVLLMStore(config Config) (*vllmStore, error) {
	tokenProcessor, err := kvblock.NewChunkedTokenDatabase(&kvblock.TokenProcessorConfig{
		BlockSizeTokens: config.BlockSizeTokens,
		HashSeed:        config.HashSeed,
	})
	if err != nil {
		return nil, fmt.Errorf("create token processor: %w", err)
	}
	index, err := kvblock.NewInMemoryIndex(&kvblock.InMemoryIndexConfig{
		Size:         config.MaxIndexKeys,
		PodCacheSize: config.MaxBackendsPerKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create bounded KV index: %w", err)
	}
	scorer, err := upstreamcache.NewKVBlockScorer(&upstreamcache.KVBlockScorerConfig{
		ScoringStrategy: upstreamcache.LongestPrefixMatch,
		BackendConfigs: []*upstreamcache.KVCacheBackendConfig{
			{Name: supportedDeviceTier, Weight: 1},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create prefix scorer: %w", err)
	}
	return &vllmStore{
		blockSize:      config.BlockSizeTokens,
		tokenProcessor: tokenProcessor,
		index:          index,
		scorer:         scorer,
		adapter:        engineadapter.NewVLLMAdapter(),
	}, nil
}

func (s *vllmStore) Lookup(
	ctx context.Context,
	query Query,
	instances map[backend.ID]Instance,
) (map[backend.ID]int, int, error) {
	podIDs := make([]string, 0, len(instances))
	backendByPod := make(map[string]backend.ID, len(instances))
	for backendID, instance := range instances {
		podIDs = append(podIDs, instance.PodIdentifier)
		backendByPod[instance.PodIdentifier] = backendID
	}

	matched := make(map[backend.ID]int, len(instances))
	totalBlocks := 0
	for _, tokens := range query.TokenGroups {
		keys, err := s.requestKeys(tokens, query.Model, query.CacheSalt)
		if err != nil {
			return nil, 0, err
		}
		if len(keys) == 0 {
			continue
		}

		keyToPods, err := s.index.Lookup(ctx, keys, sets.New(podIDs...))
		if err != nil {
			return nil, 0, fmt.Errorf("lookup KV block keys: %w", err)
		}
		scores, err := s.scorer.Score(ctx, keys, keyToPods)
		if err != nil {
			return nil, 0, fmt.Errorf("score KV block prefix: %w", err)
		}
		for podID, score := range scores {
			backendID, exists := backendByPod[podID]
			if !exists {
				continue
			}
			matched[backendID] += int(math.Round(score))
		}
		totalBlocks += len(keys)
	}
	return matched, totalBlocks, nil
}

func (s *vllmStore) Apply(ctx context.Context, instance Instance, event Event) (applyResult, error) {
	raw := &kvevents.RawMessage{Topic: event.Topic, Sequence: event.Sequence, Payload: event.Payload}
	podID, model, batch, err := s.adapter.ParseMessage(raw)
	if err != nil {
		return applyResult{}, newEventFault(ReasonEventDecodeFailed, fmt.Errorf("parse vLLM event: %w", err))
	}
	if podID != instance.PodIdentifier || model != instance.Model {
		return applyResult{}, newEventFault(
			ReasonEventDecodeFailed,
			fmt.Errorf("event identity %q/%q does not match instance %q/%q", podID, model, instance.PodIdentifier, instance.Model),
		)
	}
	for _, item := range batch.Events {
		if err := s.applyOne(ctx, podID, model, item); err != nil {
			return applyResult{}, err
		}
	}
	result := applyResult{}
	if batch.Timestamp > 0 {
		result.publishedAt = time.Unix(0, int64(batch.Timestamp*float64(time.Second)))
	}
	return result, nil
}

func (s *vllmStore) Clear(ctx context.Context, podIdentifier string) error {
	if err := s.index.Clear(ctx, podIdentifier); err != nil {
		return fmt.Errorf("clear KV index for %q: %w", podIdentifier, err)
	}
	return nil
}

func (s *vllmStore) requestKeys(tokens []uint32, model, cacheSalt string) ([]kvblock.BlockHash, error) {
	fullBlocks := len(tokens) / s.blockSize
	extraFeatures := foldCacheSalt(nil, cacheSalt, fullBlocks)
	keys, err := s.tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model, extraFeatures)
	if err != nil {
		return nil, fmt.Errorf("compute request KV block keys: %w", err)
	}
	return keys, nil
}

func (s *vllmStore) applyOne(ctx context.Context, podID, model string, event kvevents.GenericEvent) error {
	switch typed := event.(type) {
	case *kvevents.BlockStoredEvent:
		return s.applyStored(ctx, podID, model, typed)
	case *kvevents.BlockRemovedEvent:
		return s.applyRemoved(ctx, podID, typed)
	case *kvevents.AllBlocksClearedEvent:
		return s.Clear(ctx, podID)
	default:
		return newEventFault(ReasonUnsupportedEvent, fmt.Errorf("unsupported KV event %T", event))
	}
}

func (s *vllmStore) applyStored(ctx context.Context, podID, model string, event *kvevents.BlockStoredEvent) error {
	if event.BlockSize != s.blockSize || event.LoraID != nil || event.LoraName != nil || event.GroupIdx != nil {
		return newEventFault(
			ReasonUnsupportedEvent,
			fmt.Errorf("stored event uses unsupported block size, LoRA or HMA metadata"),
		)
	}
	deviceTier, err := normalizeDeviceTier(event.DeviceTier)
	if err != nil {
		return err
	}
	if len(event.BlockHashes) == 0 || len(event.Tokens) < s.blockSize {
		return newEventFault(ReasonUnsupportedEvent, fmt.Errorf("stored event has no complete block"))
	}
	if len(event.Tokens)%s.blockSize != 0 || len(event.BlockHashes) != len(event.Tokens)/s.blockSize {
		return newEventFault(ReasonUnsupportedEvent, fmt.Errorf("stored event block hashes and tokens have different granularity"))
	}

	parentKey := kvblock.EmptyBlockHash
	if event.ParentHash != 0 {
		parentKey, err = s.index.GetRequestKey(ctx, kvblock.BlockHash(event.ParentHash))
		if err != nil {
			return newEventFault(ReasonEventApplyFailed, fmt.Errorf("resolve parent block: %w", err))
		}
	}
	extraFeatures, err := kvblock.ParseRawExtraKeys(event.ExtraKeys)
	if err != nil {
		return newEventFault(ReasonEventDecodeFailed, fmt.Errorf("parse block extra keys: %w", err))
	}
	fullBlocks := len(event.Tokens) / s.blockSize
	if extraFeatures != nil && len(extraFeatures) != fullBlocks {
		return newEventFault(ReasonUnsupportedEvent, fmt.Errorf("event block granularity does not match canonical block size"))
	}
	requestKeys, err := s.tokenProcessor.TokensToKVBlockKeys(parentKey, event.Tokens, model, extraFeatures)
	if err != nil {
		return newEventFault(ReasonEventApplyFailed, fmt.Errorf("compute stored block keys: %w", err))
	}
	engineKeys := make([]kvblock.BlockHash, len(event.BlockHashes))
	for index, hash := range event.BlockHashes {
		engineKeys[index] = kvblock.BlockHash(hash)
	}
	entry := kvblock.PodEntry{PodIdentifier: podID, DeviceTier: deviceTier}
	if err := s.index.Add(ctx, engineKeys, requestKeys, []kvblock.PodEntry{entry}); err != nil {
		return newEventFault(ReasonEventApplyFailed, fmt.Errorf("add stored blocks: %w", err))
	}
	return nil
}

func (s *vllmStore) applyRemoved(ctx context.Context, podID string, event *kvevents.BlockRemovedEvent) error {
	if event.GroupIdx != nil {
		return newEventFault(ReasonUnsupportedEvent, fmt.Errorf("HMA block removal is not supported"))
	}
	deviceTier, err := normalizeDeviceTier(event.DeviceTier)
	if err != nil {
		return err
	}
	entry := kvblock.PodEntry{PodIdentifier: podID, DeviceTier: deviceTier}
	for _, hash := range event.BlockHashes {
		if err := s.index.Evict(ctx, kvblock.BlockHash(hash), kvblock.EngineKey, []kvblock.PodEntry{entry}); err != nil {
			return newEventFault(ReasonEventApplyFailed, fmt.Errorf("remove engine block %d: %w", hash, err))
		}
	}
	return nil
}

func foldCacheSalt(features []*kvblock.BlockExtraFeatures, salt string, fullBlocks int) []*kvblock.BlockExtraFeatures {
	if salt == "" || fullBlocks == 0 {
		return features
	}
	if features == nil {
		features = make([]*kvblock.BlockExtraFeatures, fullBlocks)
	}
	if features[0] == nil {
		features[0] = &kvblock.BlockExtraFeatures{}
	}
	// vLLM 只把 cache_salt 放进首个 block 的 extra_keys。事件侧上游 parser 会把该字符串映射为
	// MMHash，因此查询侧必须使用完全相同的列表表示，后续 hash chain 会自然隔离全部 block。
	features[0].MMHashes = append(features[0].MMHashes, kvblock.MMHash{Hash: salt})
	return features
}

func normalizeDeviceTier(raw string) (string, error) {
	deviceTier := strings.ToLower(strings.TrimSpace(raw))
	if deviceTier == "" {
		deviceTier = supportedDeviceTier
	}
	if deviceTier != supportedDeviceTier {
		return "", newEventFault(ReasonUnsupportedEvent, fmt.Errorf("device tier %q is not supported", raw))
	}
	return deviceTier, nil
}

func newEventFault(reason Reason, err error) error {
	return &eventFault{reason: reason, err: err}
}
