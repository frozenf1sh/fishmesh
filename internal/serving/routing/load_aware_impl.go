package routing

import (
	"fmt"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// loadAwareStrategy 是普通请求的默认策略：完整 vLLM queue/running 快照存在时按外部
// 等待压力选路；任一候选缺失或过期时不把它解释成零，而是明确退回 local in-flight 策略。
type loadAwareStrategy struct{}

// NewLoadAware 返回消费 vLLM 负载、并具有 local in-flight 降级语义的普通策略。
func NewLoadAware() Strategy { return loadAwareStrategy{} }

func (loadAwareStrategy) Name() Mode { return ModeLoadAware }

func (loadAwareStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	backends := loadBalancedBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("load-aware routing requires at least one backend")
	}
	if !completeObservedLoad(backends, snapshot.Loads) {
		return NewLoadBalanced().Select(routingKey, snapshot)
	}

	best := backends[0]
	for _, candidate := range backends[1:] {
		if observedLoadLess(candidate, best, snapshot) {
			best = candidate
		}
	}
	return Decision{Backend: best, PreferredBackendID: best.ID, Reason: ReasonLoadAware, Policy: PolicyLoadAwareV1}, nil
}

// observedLoadLess 按 queue、running、local delta 与 local in-flight 比较。它只在所有
// candidates 的外部观测都有效时调用，避免将单个缺失样本变成虚假的最小压力。
func observedLoadLess(candidate, current backend.Backend, snapshot Snapshot) bool {
	candidateLoad := snapshot.Loads[candidate.ID]
	currentLoad := snapshot.Loads[current.ID]
	if candidateLoad.QueueDepth != currentLoad.QueueDepth {
		return candidateLoad.QueueDepth < currentLoad.QueueDepth
	}
	if candidateLoad.Running != currentLoad.Running {
		return candidateLoad.Running < currentLoad.Running
	}
	if candidateLoad.LocalDelta != currentLoad.LocalDelta {
		return candidateLoad.LocalDelta < currentLoad.LocalDelta
	}
	return localInflightLess(candidate, current, snapshot)
}
