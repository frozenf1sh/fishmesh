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

	PolicyServiceV1         Policy = "service-v1"
	PolicyPureAffinityV1    Policy = "pure-affinity-v1"
	PolicyLeastInflightV1   Policy = "least-inflight-v1"
	PolicyBoundedAffinityV1 Policy = "bounded-affinity-v1"
	PolicyServiceFallbackV1 Policy = "service-fallback-v1"

	ReasonServiceDefault        Reason = "service-default"
	ReasonPrefixAffinity        Reason = "prefix-affinity"
	ReasonLeastInflight         Reason = "least-inflight"
	ReasonMissingKeyLeastLoaded Reason = "missing-key-least-loaded"
	ReasonAffinityHit           Reason = "affinity-hit"
	ReasonAffinityMiss          Reason = "affinity-miss"
	ReasonAffinitySpillover     Reason = "affinity-spillover"
	ReasonQueueDepth            Reason = "queue-depth"
	ReasonLocalInflight         Reason = "local-inflight"
	ReasonIneligible            Reason = "ineligible"
	ReasonCircuitOpen           Reason = "circuit-open"
	ReasonDiscoveryFallback     Reason = "discovery-fallback"
	ReasonCircuitFallback       Reason = "circuit-fallback"
	ReasonStrategyFallback      Reason = "strategy-fallback"
	ReasonBackendFallback       Reason = "backend-fallback"
	ReasonAdmissionCapacity     Reason = "admission-capacity"
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

// Config 是组合根创建具体 routing strategy 的配置。
type Config struct {
	Mode            Mode
	Service         backend.Backend
	BoundedAffinity BoundedAffinityConfig
}

// Snapshot is the immutable input for one routing decision.
type Snapshot struct {
	Backends     []backend.Backend
	Inflight     map[backend.ID]int64
	Observations map[backend.ID]observation.Backend
	Ineligible   map[backend.ID]Reason
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
	default:
		return nil, fmt.Errorf("unsupported routing mode %q", mode)
	}
}

// NewConfigured 创建显式配置的策略，避免组合根理解策略内部默认分支。
func NewConfigured(config Config) (Strategy, error) {
	if config.Mode != ModeBoundedAffinity {
		return New(config.Mode, config.Service)
	}
	return NewBoundedAffinity(config.BoundedAffinity)
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
