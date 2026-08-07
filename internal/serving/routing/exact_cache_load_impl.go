package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

var _ Strategy = exactCacheLoadStrategy{}

type exactCacheLoadStrategy struct{}

// NewExactCacheLoad 返回联合真实 KV locality 和已知负载的纯策略。
func NewExactCacheLoad() Strategy {
	return exactCacheLoadStrategy{}
}

func (exactCacheLoadStrategy) Name() Mode {
	return ModeExactCacheLoad
}

func (exactCacheLoadStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
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
			Reason: ReasonHardOverloadFallback, Policy: PolicyExactCacheLoadV1,
		}, nil
	}

	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if exactLess(candidate, selected, snapshot) {
			selected = candidate
		}
	}
	selected = exactTieBreak(routingKey, selected, candidates, snapshot)
	return Decision{
		Backend: selected, PreferredBackendID: selected.ID,
		Reason: ReasonExactCacheLoad, Policy: PolicyExactCacheLoadV1,
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

// exactLess 按可解释的词典顺序比较：先最小未缓存 token，再比较可用的 queue/running，
// 然后 local in-flight。缺失 load 不伪装成零，因此排在已知 load 之后。
func exactLess(candidate, current backend.Backend, snapshot Snapshot) bool {
	candidateUncached := snapshot.Exact.PromptTokens - snapshot.Exact.Matches[candidate.ID].MatchedTokens
	currentUncached := snapshot.Exact.PromptTokens - snapshot.Exact.Matches[current.ID].MatchedTokens
	if candidateUncached != currentUncached {
		return candidateUncached < currentUncached
	}
	candidateLoad, currentLoad := snapshot.Loads[candidate.ID], snapshot.Loads[current.ID]
	if candidateLoad.Valid != currentLoad.Valid {
		return candidateLoad.Valid
	}
	if candidateLoad.Valid {
		if candidateLoad.QueueDepth != currentLoad.QueueDepth {
			return candidateLoad.QueueDepth < currentLoad.QueueDepth
		}
		if candidateLoad.Running != currentLoad.Running {
			return candidateLoad.Running < currentLoad.Running
		}
	}
	return snapshot.Inflight[candidate.ID] < snapshot.Inflight[current.ID]
}

// exactTieBreak 只在 cache 和全部 load 维度相同的 backend 间使用 session hint，避免 affinity
// 覆盖真实 cache/load。排序消除 discovery 返回顺序对确定性的影响。
func exactTieBreak(routingKey string, selected backend.Backend, candidates []backend.Backend, snapshot Snapshot) backend.Backend {
	tied := []backend.Backend{selected}
	for _, candidate := range candidates {
		if candidate.ID != selected.ID && !exactLess(candidate, selected, snapshot) && !exactLess(selected, candidate, snapshot) {
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
