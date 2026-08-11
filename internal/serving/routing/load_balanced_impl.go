package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// 编译期断言：loadBalancedStrategy 必须实现 Strategy 接口。
var _ Strategy = loadBalancedStrategy{}

// loadBalancedStrategy 是无状态的负载感知策略：
// 在全部合格后端中挑选本 Gateway 视角在途请求最少的一个。
type loadBalancedStrategy struct{}

// NewLoadBalanced 返回本地 in-flight 感知的选择策略。
func NewLoadBalanced() Strategy {
	return loadBalancedStrategy{}
}

func (loadBalancedStrategy) Name() Mode {
	return ModeLoadBalanced
}

func (loadBalancedStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	backends := EligibleBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("load-balanced routing requires at least one backend")
	}
	// 哈希在这里不决定亲和，只用来随机化遍历起点：
	// 避免每次比较都从同一位置开始，使平局时的胜者与请求无关，
	// 同时防止所有请求羊群式地压向同一个后端。
	hash := sha256.Sum256([]byte(routingKey))
	start := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(backends)))
	best := backends[start]
	bestInflight := snapshot.Inflight[best.ID]
	// 从起点环状遍历其余后端，严格小于才替换，
	// 保证出现完全相同的负载时保留起点（由哈希决定，确定性不受顺序影响）。
	for offset := 1; offset < len(backends); offset++ {
		candidate := backends[(start+offset)%len(backends)]
		candidateInflight := snapshot.Inflight[candidate.ID]
		if candidateInflight < bestInflight {
			best = candidate
			bestInflight = candidateInflight
		}
	}
	return Decision{
		Backend:            best,
		PreferredBackendID: best.ID,
		Reason:             ReasonLoadBalanced,
		Policy:             PolicyLoadBalancedV1,
	}, nil
}
