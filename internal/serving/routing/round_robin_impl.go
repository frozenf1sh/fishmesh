package routing

import (
	"fmt"
	"sync/atomic"
)

// roundRobinStrategy 只维护一个原子游标，因此可作为“没有负载/KV 信号”的请求级
// Service 基线。它不模拟 kube-proxy 的 connection tracking；真实 Service 对照仍应
// 单独走 ClusterIP 端点，不能把两者混为同一协议。
type roundRobinStrategy struct{ next atomic.Uint64 }

// NewRoundRobin 返回并发安全的请求级 round-robin 策略。
func NewRoundRobin() Strategy { return &roundRobinStrategy{} }

func (*roundRobinStrategy) Name() Mode { return ModeRoundRobin }

func (s *roundRobinStrategy) Select(_ string, snapshot Snapshot) (Decision, error) {
	backends := EligibleBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("round-robin routing requires at least one backend")
	}
	selected := backends[(s.next.Add(1)-1)%uint64(len(backends))]
	return Decision{Backend: selected, PreferredBackendID: selected.ID, Reason: ReasonRoundRobin, Policy: PolicyRoundRobinV1}, nil
}
