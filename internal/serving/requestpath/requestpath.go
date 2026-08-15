// Package requestpath 负责单次推理请求的 backend 选择生命周期，并提供可解释、可结算的路由 lease。
package requestpath

import (
	"context"
	"fmt"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
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
	// Route 和 Body 是 Gateway 读取并有界复制后的原始推理输入。只有 KV-aware 策略把它翻译为
	// tokenization.Input；其他策略不解析或保留 body。
	Route string
	Body  []byte
}

// KVStatus 描述本次选择是否取得可用于 KV-aware routing 的完整信号。
type KVStatus string

const (
	KVNotRequested         KVStatus = "not-requested"
	KVAvailable            KVStatus = "available"
	KVTokenizationFailed   KVStatus = "tokenization-failed"
	KVLookupFailed         KVStatus = "lookup-failed"
	KVMatchUnavailable     KVStatus = "match-unavailable"
	KVShortContextBypassed KVStatus = "short-context-bypassed"
)

// KVCacheState 是 delivery 可以安全观测的逐 backend KV index 快照。
// 它不包含 Pod UID、ZMQ endpoint、prompt 或 token IDs；unknown/stale 必须由 Valid=false 与 Reason 共同表达。
type KVCacheState struct {
	Valid          bool
	Reason         kvcache.Reason
	Freshness      time.Duration
	LastSequence   uint64
	AppliedBatches uint64
	ReplayBatches  uint64
}

// State 是一次选择时使用的可观测快照，供 delivery 层投影为 metrics。
type State struct {
	Backends     []backend.Backend
	Observations map[backend.ID]observation.Backend
	CircuitOpen  map[backend.ID]bool
	Discovery    discovery.ResolverStatus
	KV           KVStatus
	KVCache      map[backend.ID]KVCacheState
	// CachedPrefixTokens 是本次 KV lookup 对最终 backend 的真实完整 block 前缀；unknown/stale 时为零且 KV 不为 available。
	CachedPrefixTokens int
	// Prediction 是不参与本次实际选择的本地 TTFT 影子结论。
	Prediction prediction.Shadow
	Estimate   EstimateEvidence
}

// EstimateEvidence 是最终 backend 的低基数数值 provenance；不包含 prompt、token IDs 或 prefix identity。
type EstimateEvidence struct {
	PromptTokens             int
	CachedPrefixTokens       int
	UncachedTokens           int
	EstimatedTTFT            time.Duration
	Valid                    bool
	Confidence               routing.EstimateConfidence
	Version                  string
	Reason                   string
	LoadValid                bool
	LoadSampleAge            time.Duration
	QueueDepth               int64
	Running                  int64
	LocalDelta               int64
	LocalInflight            int64
	HardOverloadedCandidates int
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
	// HardQueueDepth and HardLocalInflight are safety gates for KV-aware
	// candidates. Zero disables the corresponding gate.
	HardQueueDepth                    int64
	HardLocalInflight                 int64
	ShortPromptTokens                 int
	RuntimeCPUHardLimitCores          float64
	RuntimeMemoryHardLimitBytes       float64
	RuntimeGPUUtilizationHardLimitPct float64
	RuntimeGPUMemoryHardLimitBytes    float64
	RuntimeGPUTemperatureHardLimitC   float64
}

// Dependencies 是由组合根注入的原子能力。
type Dependencies struct {
	Resolver         discovery.Resolver
	Observations     observation.Reader
	Strategy         routing.Strategy
	Circuits         circuit.Breaker
	Tokenizer        tokenization.Tokenizer
	KVCache          kvcache.Index
	KVReconcile      func(context.Context, []backend.Backend) error
	OnBackendRemoved func(backend.ID)
	// Predictor 是可选的纯观测能力；nil 保持预测关闭，不能影响既有选路。
	Predictor prediction.Tracker
	// StaticEstimator 是可选的纯 profile；nil 保持 token-cost 行为。
	StaticEstimator *prediction.StaticEstimator
}

// Validate 检查 requestpath 的启动配置。
//
// Service 是无 discovery 可用时的最终 fallback，因此即使某些请求不会立刻触发
// fallback，也必须在进程启动时确认它是完整 backend。DiscoveryMaxAge 只有在启用
// freshness gate 时才有意义，避免把“未启用”误判成配置错误。
func (c Config) Validate() error {
	if err := c.Service.Validate(); err != nil {
		return fmt.Errorf("requestpath service fallback: %w", err)
	}
	if c.RequireFreshDiscovery && c.DiscoveryMaxAge <= 0 {
		return fmt.Errorf("requestpath discovery max age must be positive")
	}
	if c.HardQueueDepth < 0 || c.HardLocalInflight < 0 {
		return fmt.Errorf("requestpath hard overload thresholds must not be negative")
	}
	if c.ShortPromptTokens < 0 {
		return fmt.Errorf("requestpath short prompt token threshold must not be negative")
	}
	if c.RuntimeCPUHardLimitCores < 0 || c.RuntimeMemoryHardLimitBytes < 0 || c.RuntimeGPUUtilizationHardLimitPct < 0 || c.RuntimeGPUMemoryHardLimitBytes < 0 || c.RuntimeGPUTemperatureHardLimitC < 0 {
		return fmt.Errorf("requestpath runtime hard overload thresholds must not be negative")
	}
	return nil
}

// Validate 检查 requestpath 必须拥有的依赖，并根据已选策略确认 KV-aware 模式的附加能力。
//
// 策略是依赖集合的判定条件之一，因此所有相关判断都集中在这里；New 不再散落多段
// 参数校验，Select 也不需要为启动期依赖做防御性分支。
func (d Dependencies) Validate() error {
	if d.Resolver == nil || d.Strategy == nil || d.Circuits == nil {
		return fmt.Errorf("requestpath resolver, strategy and circuits must not be nil")
	}
	if d.Strategy.Name() == routing.ModeKVAware {
		if d.Tokenizer == nil || d.KVCache == nil || d.KVReconcile == nil {
			return fmt.Errorf("kv-aware requestpath requires tokenizer, KV cache and KV reconcile")
		}
	}
	return nil
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

// FirstTokenObservation 是 delivery 可安全投影的预测误差，不暴露预测域的可变状态。
type FirstTokenObservation struct {
	Valid     bool
	Backend   backend.ID
	Predicted time.Duration
	Actual    time.Duration
	Error     time.Duration
}

// ObserveFirstToken 在首个非终止 SSE 事件时记录一次 TTFT；它不改变已经固定的 Decision。
func (l Lease) ObserveFirstToken(ttft time.Duration) FirstTokenObservation {
	if l.state == nil {
		return FirstTokenObservation{Actual: ttft}
	}
	observation := l.state.ticket.ObserveFirstToken(ttft)
	return FirstTokenObservation{Valid: observation.Valid, Backend: observation.Backend, Predicted: observation.Predicted, Actual: ttft, Error: observation.Error}
}
