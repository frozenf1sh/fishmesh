// Package routing contains deterministic, request-path routing policies.
//
// The package deliberately knows nothing about Kubernetes, HTTP clients or
// Prometheus. A policy receives a snapshot of candidate backends and returns a
// decision; the gateway owns lifecycle, transport and observability concerns.
package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"
)

const (
	ModeService         = "service"
	ModePrefixHash      = "prefix-hash" // Deprecated alias kept for old experiments.
	ModePrefixAffinity  = "prefix-affinity"
	ModeLoadAware       = "load-aware"
	ModeBoundedAffinity = "bounded-affinity"
)

// Backend is the stable identity and address used by a routing policy.
// ID is intentionally separate from URL so logs and metrics do not depend on
// URL formatting.
type Backend struct {
	ID       string
	URL      string
	Metadata map[string]string
}

// BackendIdentity links a resolved address to Kubernetes workload identity.
// GPURequested is the declared resource request, not live utilization; a
// separate node-level exporter must provide utilization before a scheduler can
// use it as a score input.
type BackendIdentity struct {
	PodName      string
	NodeName     string
	GPURequested float64
	Ready        bool
	Status       ObservationStatus
	Error        string
}

// ObservationStatus describes the quality of a backend's latest telemetry.
// It is intentionally a string in the routing contract so exporters and
// future policies can preserve unknown states without a package-wide enum
// migration.
type ObservationStatus string

const (
	ObservationOK          ObservationStatus = "ok"
	ObservationDegraded    ObservationStatus = "degraded"
	ObservationUnavailable ObservationStatus = "unavailable"
)

// Sample distinguishes an observed zero from a missing value. Adapters fill
// one sample per signal; the observation service invalidates stale samples
// before a scheduler sees them.
type Sample[T any] struct {
	Value      T
	Valid      bool
	ObservedAt time.Time
	Source     string
	Error      string
}

// BackendObservation is the low-cardinality, per-backend telemetry contract.
// The routing package owns the shape; infrastructure adapters own how values
// are collected. Queue/running use per-field samples so an observed zero is
// not confused with a missing or stale value. Status remains an aggregate
// health summary for diagnostics and backwards-compatible metrics.
type BackendObservation struct {
	Identity            BackendIdentity
	Status              ObservationStatus
	Source              string
	ObservedAt          time.Time
	Freshness           time.Duration
	QueueLength         Sample[float64]
	RunningRequests     Sample[float64]
	PrefixCacheHitRate  float64
	TTFTP95Milliseconds float64
	KVCacheUsagePercent float64
	Error               string
}

// Snapshot is the gateway's read-only view of the current backend state.
// Inflight is optional and only used by policies that need local concurrency
// information. Future EndpointSlice/vLLM metric adapters can add state without
// changing the Strategy interface.
type Snapshot struct {
	Backends     []Backend
	Inflight     map[string]int64
	Observations map[string]BackendObservation
	// Ineligible keeps lifecycle and circuit state separate from membership.
	// Policies can preserve a stable affinity preference while spilling around
	// a temporarily unavailable backend.
	Ineligible map[string]string
}

// Decision records the selected backend and an explainable reason.
type Decision struct {
	Backend            Backend
	PreferredBackendID string
	Reason             string
	SpilloverReason    string
	Policy             string
}

// Strategy selects one backend for a request. It is synchronous by design so
// it can remain on the low-latency request path; slow analysis belongs outside
// this package.
type Strategy interface {
	Name() string
	Select(prefixKey string, snapshot Snapshot) (Decision, error)
}

// BackendReconciler is implemented by stateful strategies. The gateway calls
// it with the discovery membership so state for deleted endpoints is reclaimed
// without treating a temporary circuit as permanent membership removal.
type BackendReconciler interface {
	ReconcileBackends([]Backend)
}

type serviceStrategy struct{ backend Backend }

// NewService returns the Service baseline strategy.
func NewService(backend Backend) Strategy { return serviceStrategy{backend: backend} }

func (s serviceStrategy) Name() string { return ModeService }

func (s serviceStrategy) Select(_ string, _ Snapshot) (Decision, error) {
	if s.backend.ID == "" || s.backend.URL == "" {
		return Decision{}, fmt.Errorf("service backend is incomplete")
	}
	return Decision{Backend: s.backend, PreferredBackendID: s.backend.ID, Reason: "service-default", Policy: "service-v1"}, nil
}

type prefixAffinityStrategy struct{}

// NewPrefixAffinity returns the stable prefix-to-backend strategy. The old
// prefix-hash name remains an accepted configuration alias for experiment
// compatibility, while new deployments should use prefix-affinity.
func NewPrefixAffinity() Strategy { return prefixAffinityStrategy{} }

func (prefixAffinityStrategy) Name() string { return ModePrefixAffinity }

func (prefixAffinityStrategy) Select(prefixKey string, snapshot Snapshot) (Decision, error) {
	backends := EligibleBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("prefix affinity requires at least one backend")
	}
	hash := sha256.Sum256([]byte(prefixKey))
	index := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(backends)))
	return Decision{Backend: backends[index], PreferredBackendID: backends[index].ID, Reason: "prefix-affinity", Policy: "pure-affinity-v1"}, nil
}

type loadAwareStrategy struct{}

// NewLoadAware returns a local in-flight-aware policy. It is intentionally
// described as load-aware rather than GPU-aware: the snapshot currently only
// contains gateway-owned in-flight counts.
func NewLoadAware() Strategy { return loadAwareStrategy{} }

func (loadAwareStrategy) Name() string { return ModeLoadAware }

func (loadAwareStrategy) Select(prefixKey string, snapshot Snapshot) (Decision, error) {
	backends := EligibleBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("load-aware routing requires at least one backend")
	}
	hash := sha256.Sum256([]byte(prefixKey))
	start := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(backends)))
	best := backends[start]
	bestInflight := snapshot.Inflight[best.ID]
	for offset := 1; offset < len(backends); offset++ {
		candidate := backends[(start+offset)%len(backends)]
		candidateInflight := snapshot.Inflight[candidate.ID]
		if candidateInflight < bestInflight {
			best, bestInflight = candidate, candidateInflight
		}
	}
	return Decision{Backend: best, PreferredBackendID: best.ID, Reason: "least-inflight", Policy: "least-inflight-v1"}, nil
}

// EligibleBackends returns a copy containing only endpoints that are not
// excluded by lifecycle or circuit state.
func EligibleBackends(snapshot Snapshot) []Backend {
	result := make([]Backend, 0, len(snapshot.Backends))
	for _, backend := range snapshot.Backends {
		if _, blocked := snapshot.Ineligible[backend.ID]; !blocked {
			result = append(result, backend)
		}
	}
	return result
}

// New returns a strategy for a configured mode. prefix-hash is accepted as a
// backwards-compatible alias and is normalized by the caller's metrics layer.
func New(mode string, service Backend) (Strategy, error) {
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
