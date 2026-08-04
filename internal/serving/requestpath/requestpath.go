// Package requestpath 负责单次推理请求的 backend 选择生命周期，并提供可解释、可结算的路由 lease。
package requestpath

import (
	"context"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

const (
	OutcomeResponseCompleted  Outcome = "response-completed"
	OutcomeTransportFailure   Outcome = "transport-failure"
	OutcomeDeadlineExceeded   Outcome = "deadline-exceeded"
	OutcomeUpstreamStream     Outcome = "upstream-stream-failure"
	OutcomeClientCanceled     Outcome = "client-canceled"
	OutcomeDownstreamFailure  Outcome = "downstream-failure"
	OutcomeInvalidClientInput Outcome = "invalid-client-input"
)

// Outcome 描述 Gateway 已完成的一次 upstream 生命周期结果。
// 只有真正代表 backend 传输健康度的结果才会进入 circuit。
type Outcome string

// Request 是与 HTTP 协议无关的请求路径输入。
type Request struct {
	RoutingKey string
}

// State 是一次选择时使用的可观测快照，供 delivery 层投影为 metrics。
type State struct {
	Backends     []backend.Backend
	Observations map[backend.ID]observation.Backend
	CircuitOpen  map[backend.ID]bool
	Discovery    discovery.ResolverStatus
}

// Completion 描述 lease 完成后发生的 circuit 状态变化。
type Completion struct {
	CircuitOpened  bool
	CircuitOpen    bool
	BackendRemoved bool
}

// Lease 固定一次选择结果，并确保 local in-flight 和 circuit 只结算一次。
// Lease 可以按值传递；内部状态指针保证复制后仍共享同一个幂等完成动作。
type Lease struct {
	Decision routing.Decision
	State    State
	state    *leaseState
}

// Config 只包含 requestpath 自己拥有的 fallback 和成员同步配置。
type Config struct {
	Service               backend.Backend
	RequireFreshDiscovery bool
	DiscoveryMaxAge       time.Duration
	ReconcileInterval     time.Duration
}

// Dependencies 是由组合根注入的原子能力。
type Dependencies struct {
	Resolver         discovery.Resolver
	Observations     observation.Reader
	Strategy         routing.Strategy
	Circuits         circuit.Breaker
	OnBackendRemoved func(backend.ID)
}

// Path 是 standalone Gateway 的请求选择与结算边界。
// integrated llm-d adapter 只复用 routing，避免重复 discovery、观测和请求生命周期状态。
type Path interface {
	Select(context.Context, Request) (Lease, error)
	State(context.Context) State
	Ready() bool
	Close() error
}

// Complete 幂等结算请求结果，并释放 backend local in-flight。
func (l Lease) Complete(outcome Outcome) Completion {
	if l.state == nil {
		return Completion{}
	}
	l.state.once.Do(func() {
		l.state.result = l.state.service.complete(l.state.backendID, l.state.counter, outcome)
	})
	return l.state.result
}
