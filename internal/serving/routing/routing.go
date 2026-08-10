// Package routing owns deterministic, infrastructure-free endpoint selection policies.
package routing

import (
	"fmt"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
)

const (
	ModeService         Mode = "service"
	ModePrefixHash      Mode = "prefix-hash"
	ModePrefixAffinity  Mode = "prefix-affinity"
	ModeLoadAware       Mode = "load-aware"
	ModeBoundedAffinity Mode = "bounded-affinity"
	ModeExactCacheLoad  Mode = "exact-cache-load"

	PolicyServiceV1           Policy = "service-v1"
	PolicyPureAffinityV1      Policy = "pure-affinity-v1"
	PolicyLeastInflightV1     Policy = "least-inflight-v1"
	PolicyBoundedAffinityV1   Policy = "bounded-affinity-v1"
	PolicyServiceFallbackV1   Policy = "service-fallback-v1"
	PolicyExactCacheLoadV1    Policy = "exact-cache-load-v1"
	PolicyExactCacheLoadV2    Policy = "exact-cache-load-v2"
	PolicyExactLoadFallbackV1 Policy = "exact-load-fallback-v1"

	ReasonServiceDefault         Reason = "service-default"
	ReasonPrefixAffinity         Reason = "prefix-affinity"
	ReasonLeastInflight          Reason = "least-inflight"
	ReasonMissingKeyLeastLoaded  Reason = "missing-key-least-loaded"
	ReasonAffinityHit            Reason = "affinity-hit"
	ReasonAffinityMiss           Reason = "affinity-miss"
	ReasonAffinitySpillover      Reason = "affinity-spillover"
	ReasonQueueDepth             Reason = "queue-depth"
	ReasonLocalInflight          Reason = "local-inflight"
	ReasonIneligible             Reason = "ineligible"
	ReasonCircuitOpen            Reason = "circuit-open"
	ReasonDiscoveryFallback      Reason = "discovery-fallback"
	ReasonCircuitFallback        Reason = "circuit-fallback"
	ReasonStrategyFallback       Reason = "strategy-fallback"
	ReasonBackendFallback        Reason = "backend-fallback"
	ReasonAdmissionCapacity      Reason = "admission-capacity"
	ReasonExactCacheLoad         Reason = "exact-cache-load"
	ReasonExactSignalUnavailable Reason = "exact-signal-unavailable"
	ReasonHardOverloadFallback   Reason = "hard-overload-fallback"
)

// Mode identifies one configured endpoint-selection strategy.
type Mode string

// Policy identifies the versioned semantics used for a decision.
type Policy string

// Reason explains a selection, spillover, rejection, or fallback.
type Reason string

// BoundedAffinityConfig contains independently comparable pressure bounds.
type BoundedAffinityConfig struct {
	TTL             time.Duration
	MaxEntries      int
	InflightDelta   int64
	QueueDepthDelta float64
	Clock           func() time.Time
}

// ExactCacheLoadConfig 将各类已知压力折算为等价未缓存 token。它只决定同一已知 KV 状态下的
// 取舍，不接受或掩盖未知 load；具体数值必须在目标硬件/profile 上校准。
type ExactCacheLoadConfig struct {
	QueueTokenPenalty    int64
	RunningTokenPenalty  int64
	InflightTokenPenalty int64
}

// Config 是组合根创建具体 routing strategy 的配置。
type Config struct {
	Mode            Mode
	Service         backend.Backend
	BoundedAffinity BoundedAffinityConfig
	ExactCacheLoad  ExactCacheLoadConfig
}

// DefaultExactCacheLoadConfig 返回尚未经过具体 GPU 校准的保守 token-equivalent 起点。
// 队列中的请求尚未开始 prefill，因此其 penalty 高于已经运行或仅由本 Gateway 观察到的请求。
func DefaultExactCacheLoadConfig() ExactCacheLoadConfig {
	return ExactCacheLoadConfig{QueueTokenPenalty: 512, RunningTokenPenalty: 128, InflightTokenPenalty: 64}
}

// Validate 拒绝会把压力变成负成本的配置；零允许在受控实验中单独关闭一个已知项。
func (c ExactCacheLoadConfig) Validate() error {
	if c.QueueTokenPenalty < 0 || c.RunningTokenPenalty < 0 || c.InflightTokenPenalty < 0 {
		return fmt.Errorf("exact-cache-load token penalties must not be negative")
	}
	return nil
}

// Snapshot is the immutable input for one routing decision.
type Snapshot struct {
	Backends     []backend.Backend
	Inflight     map[backend.ID]int64
	Observations map[backend.ID]observation.Backend
	Ineligible   map[backend.ID]Reason
	Loads        map[backend.ID]Load
	Exact        ExactInput
}

// Load 是 routing 解释的逐 backend 负载值；缺失观测必须显式标记，而不能伪装成零负载。
type Load struct {
	QueueDepth   int64
	Running      int64
	Valid        bool
	HardOverload bool
}

// CacheMatch 是 routing 对真实 KV 状态的只读投影。它不暴露 kvcache 的实现或第三方类型。
// Valid=false 表示信号未知或过期；MatchedTokens=0 只有在 Valid=true 时才代表真实零命中。
type CacheMatch struct {
	Valid         bool
	MatchedTokens int
}

// ExactInput 是一次 exact-cache-load 决策的请求侧值对象。调用方必须用 tokenization 的
// TokenIDs 计算 PromptTokens，并把 kvcache.Match 逐字段投影为 Matches；routing 不拥有两者。
type ExactInput struct {
	PromptTokens int
	Matches      map[backend.ID]CacheMatch
}

// UsableFor 只有在每个候选都有有效 exact match 时返回 true。未知/过期不是零命中，调用方
// 必须据此显式降级到 load-aware，而非让 exact 策略猜测缺失数据。
func (i ExactInput) UsableFor(backends []backend.Backend) bool {
	if i.PromptTokens <= 0 || len(backends) == 0 {
		return false
	}
	for _, candidate := range backends {
		match, ok := i.Matches[candidate.ID]
		if !ok || !match.Valid || match.MatchedTokens < 0 || match.MatchedTokens > i.PromptTokens {
			return false
		}
	}
	return true
}

// Decision records the selected backend and explainable policy result.
type Decision struct {
	Backend            backend.Backend
	PreferredBackendID backend.ID
	Reason             Reason
	SpilloverReason    Reason
	Policy             Policy
}

// Strategy synchronously selects one backend from a snapshot.
type Strategy interface {
	Name() Mode
	Select(routingKey string, snapshot Snapshot) (Decision, error)
}

// BackendReconciler is implemented by strategies with membership-scoped state.
type BackendReconciler interface {
	ReconcileBackends([]backend.Backend)
}

// New creates the configured strategy. ModePrefixHash remains a deprecated
// compatibility alias for existing experiment configuration.
func New(mode Mode, service backend.Backend) (Strategy, error) {
	switch mode {
	case "", ModeService:
		return NewService(service), nil
	case ModePrefixHash, ModePrefixAffinity:
		return NewPrefixAffinity(), nil
	case ModeLoadAware:
		return NewLoadAware(), nil
	case ModeBoundedAffinity:
		return NewBoundedAffinity(DefaultBoundedAffinityConfig())
	case ModeExactCacheLoad:
		return NewExactCacheLoad(), nil
	default:
		return nil, fmt.Errorf("unsupported routing mode %q", mode)
	}
}

// NewConfigured 创建显式配置的策略，避免组合根理解策略内部默认分支。
func NewConfigured(config Config) (Strategy, error) {
	switch config.Mode {
	case ModeExactCacheLoad:
		return NewConfiguredExactCacheLoad(config.ExactCacheLoad)
	case ModeBoundedAffinity:
		return NewBoundedAffinity(config.BoundedAffinity)
	default:
		return New(config.Mode, config.Service)
	}
}

// EligibleBackends returns a copy without lifecycle- or circuit-blocked entries.
func EligibleBackends(snapshot Snapshot) []backend.Backend {
	result := make([]backend.Backend, 0, len(snapshot.Backends))
	for _, candidate := range snapshot.Backends {
		if _, blocked := snapshot.Ineligible[candidate.ID]; !blocked {
			result = append(result, candidate)
		}
	}
	return result
}
