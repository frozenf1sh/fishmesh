package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// 编译期断言：loadBalancedStrategy 必须实现 Strategy 接口。
var _ Strategy = loadBalancedStrategy{}

// loadBalancedStrategy 是无状态的本地负载均衡策略：只比较当前 Gateway 已登记的
// in-flight lease。它不把 vLLM queue/running 当作输入，因而既是可解释的消融组，
// 也是 load-aware 外部观测失效时的保守降级层。
type loadBalancedStrategy struct{}

// NewLoadBalanced 返回只依赖 local in-flight 的普通负载均衡策略。
func NewLoadBalanced() Strategy {
	return loadBalancedStrategy{}
}

func (loadBalancedStrategy) Name() Mode {
	return ModeLoadBalanced
}

func (loadBalancedStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	backends := loadBalancedBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("load-balanced routing requires at least one backend")
	}
	// 哈希在这里不决定亲和，只用来随机化遍历起点：
	// 避免每次比较都从同一位置开始，使平局时的胜者与请求无关，
	// 同时防止所有请求羊群式地压向同一个后端。
	hash := sha256.Sum256([]byte(routingKey))
	start := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(backends)))
	best := backends[start]
	// 从起点环状遍历其余后端，严格小于才替换，
	// 保证出现完全相同的负载时保留起点（由哈希决定，确定性不受顺序影响）。
	for offset := 1; offset < len(backends); offset++ {
		candidate := backends[(start+offset)%len(backends)]
		if localInflightLess(candidate, best, snapshot) {
			best = candidate
		}
	}
	return Decision{
		Backend:            best,
		PreferredBackendID: best.ID,
		Reason:             ReasonLoadBalanced,
		Policy:             PolicyLoadBalancedV1,
	}, nil
}

// loadBalancedBackends 先排除已发布的 hard-overload backend；如果所有候选都过载，
// 则保留全部候选以维持既有的可用性优先语义，由比较器选择压力最小者。
func loadBalancedBackends(snapshot Snapshot) []backend.Backend {
	eligible := EligibleBackends(snapshot)
	available := make([]backend.Backend, 0, len(eligible))
	for _, candidate := range eligible {
		if !snapshot.Loads[candidate.ID].HardOverload {
			available = append(available, candidate)
		}
	}
	if len(available) > 0 {
		return available
	}
	return eligible
}

func completeObservedLoad(backends []backend.Backend, loads map[backend.ID]Load) bool {
	if len(backends) == 0 {
		return false
	}
	for _, candidate := range backends {
		if !loads[candidate.ID].Valid {
			return false
		}
	}
	return true
}

// localInflightLess 仅按本 Gateway 的 lease 计数比较，并以 backend ID 固定平局。
func localInflightLess(candidate, current backend.Backend, snapshot Snapshot) bool {
	candidateInflight := snapshot.Inflight[candidate.ID]
	currentInflight := snapshot.Inflight[current.ID]
	if candidateInflight != currentInflight {
		return candidateInflight < currentInflight
	}
	return candidate.ID < current.ID
}
