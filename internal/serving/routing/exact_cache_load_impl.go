package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const maximumExactCost = int64(^uint64(0) >> 1)

var _ Strategy = exactCacheLoadStrategy{}

type exactCacheLoadStrategy struct {
	config ExactCacheLoadConfig
}

// NewExactCacheLoad 返回联合真实 KV locality 和已知负载的纯策略。
func NewExactCacheLoad() Strategy {
	return exactCacheLoadStrategy{config: DefaultExactCacheLoadConfig()}
}

// NewConfiguredExactCacheLoad 构造成本式 exact 策略。配置只描述同量纲的压力换算，不读取外部状态。
func NewConfiguredExactCacheLoad(config ExactCacheLoadConfig) (Strategy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return exactCacheLoadStrategy{config: config}, nil
}

func (exactCacheLoadStrategy) Name() Mode {
	return ModeExactCacheLoad
}

func (s exactCacheLoadStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	eligible := EligibleBackends(snapshot)
	if len(eligible) == 0 {
		return Decision{}, fmt.Errorf("exact-cache-load routing requires at least one backend")
	}
	if !snapshot.Exact.UsableFor(eligible) {
		return Decision{}, fmt.Errorf("exact-cache-load routing requires valid exact matches for every eligible backend")
	}

	candidates := withoutHardOverload(eligible, snapshot.Loads)
	if len(candidates) == 0 {
		// 当所有健康 backend 都超过硬阈值时，保持可用性优先：cache locality 被否决，
		// 使用既有 load-only 顺序挑选一个 backend。requestpath 仍会记录 typed reason。
		selected := leastLoaded(snapshot, "")
		return Decision{
			Backend: selected, PreferredBackendID: selected.ID,
			Reason: ReasonHardOverloadFallback, Policy: PolicyExactCacheLoadV2,
		}, nil
	}

	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if exactCostLess(candidate, selected, snapshot, s.config) {
			selected = candidate
		}
	}
	selected = exactTieBreak(routingKey, selected, candidates, snapshot, s.config)
	return Decision{
		Backend: selected, PreferredBackendID: selected.ID,
		Reason: ReasonExactCacheLoad, Policy: PolicyExactCacheLoadV2,
	}, nil
}

func withoutHardOverload(backends []backend.Backend, loads map[backend.ID]Load) []backend.Backend {
	result := make([]backend.Backend, 0, len(backends))
	for _, candidate := range backends {
		if !loads[candidate.ID].HardOverload {
			result = append(result, candidate)
		}
	}
	return result
}

// exactCostLess 比较等价未缓存 token 成本。unknown external load 只是不贡献 queue/running 项，
// 不是零负载声明；KV unknown 已在策略入口前由 ExactInput.UsableFor 拒绝。
func exactCostLess(candidate, current backend.Backend, snapshot Snapshot, config ExactCacheLoadConfig) bool {
	return exactCost(candidate, snapshot, config) < exactCost(current, snapshot, config)
}

// exactTieBreak 只在 cache 和全部 load 维度相同的 backend 间使用 session hint，避免 affinity
// 覆盖真实 cache/load。排序消除 discovery 返回顺序对确定性的影响。
func exactTieBreak(routingKey string, selected backend.Backend, candidates []backend.Backend, snapshot Snapshot, config ExactCacheLoadConfig) backend.Backend {
	tied := []backend.Backend{selected}
	for _, candidate := range candidates {
		if candidate.ID != selected.ID && exactCost(candidate, snapshot, config) == exactCost(selected, snapshot, config) {
			tied = append(tied, candidate)
		}
	}
	if len(tied) == 1 {
		return selected
	}
	sort.Slice(tied, func(i, j int) bool { return tied[i].ID < tied[j].ID })
	hash := sha256.Sum256([]byte(routingKey))
	return tied[int(binary.BigEndian.Uint32(hash[:4])%uint32(len(tied)))]
}

func exactCost(candidate backend.Backend, snapshot Snapshot, config ExactCacheLoadConfig) int64 {
	match := snapshot.Exact.Matches[candidate.ID]
	cost := int64(snapshot.Exact.PromptTokens - match.MatchedTokens)
	load := snapshot.Loads[candidate.ID]
	if load.Valid {
		cost = saturatingAdd(cost, saturatingMultiply(load.QueueDepth, config.QueueTokenPenalty))
		cost = saturatingAdd(cost, saturatingMultiply(load.Running, config.RunningTokenPenalty))
	}
	return saturatingAdd(cost, saturatingMultiply(snapshot.Inflight[candidate.ID], config.InflightTokenPenalty))
}

func saturatingMultiply(count, penalty int64) int64 {
	if count <= 0 || penalty <= 0 {
		return 0
	}
	if count > maximumExactCost/penalty {
		return maximumExactCost
	}
	return count * penalty
}

func saturatingAdd(left, right int64) int64 {
	if left >= maximumExactCost-right {
		return maximumExactCost
	}
	return left + right
}
