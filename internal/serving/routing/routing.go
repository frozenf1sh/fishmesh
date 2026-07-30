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
	ModeService        = "service"
	ModePrefixHash     = "prefix-hash" // Deprecated alias kept for old experiments.
	ModePrefixAffinity = "prefix-affinity"
	ModeLoadAware      = "load-aware"
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

// BackendObservation is the low-cardinality, per-backend telemetry contract.
// The routing package owns the shape; infrastructure adapters own how values
// are collected. Zero-valued numeric fields mean "not observed", not zero
// load, and Status/Error make that distinction explicit.
type BackendObservation struct {
	Identity            BackendIdentity
	Status              ObservationStatus
	Source              string
	ObservedAt          time.Time
	Freshness           time.Duration
	QueueLength         float64
	RunningRequests     float64
	PrefixCacheHitRate  float64
	TTFTP95Milliseconds float64
	GPUUtilization      float64
	GPUMemoryUsage      float64
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
}

// Decision records the selected backend and an explainable reason.
type Decision struct {
	Backend Backend
	Reason  string
}

// Strategy selects one backend for a request. It is synchronous by design so
// it can remain on the low-latency request path; slow analysis belongs outside
// this package.
type Strategy interface {
	Name() string
	Select(prefixKey string, snapshot Snapshot) (Decision, error)
}

type serviceStrategy struct{ backend Backend }

// NewService returns the Service baseline strategy.
func NewService(backend Backend) Strategy { return serviceStrategy{backend: backend} }

func (s serviceStrategy) Name() string { return ModeService }

func (s serviceStrategy) Select(_ string, _ Snapshot) (Decision, error) {
	if s.backend.ID == "" || s.backend.URL == "" {
		return Decision{}, fmt.Errorf("service backend is incomplete")
	}
	return Decision{Backend: s.backend, Reason: "service-default"}, nil
}

type prefixAffinityStrategy struct{}

// NewPrefixAffinity returns the stable prefix-to-backend strategy. The old
// prefix-hash name remains an accepted configuration alias for experiment
// compatibility, while new deployments should use prefix-affinity.
func NewPrefixAffinity() Strategy { return prefixAffinityStrategy{} }

func (prefixAffinityStrategy) Name() string { return ModePrefixAffinity }

func (prefixAffinityStrategy) Select(prefixKey string, snapshot Snapshot) (Decision, error) {
	if len(snapshot.Backends) == 0 {
		return Decision{}, fmt.Errorf("prefix affinity requires at least one backend")
	}
	hash := sha256.Sum256([]byte(prefixKey))
	index := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(snapshot.Backends)))
	return Decision{Backend: snapshot.Backends[index], Reason: "prefix-affinity"}, nil
}

type loadAwareStrategy struct{}

// NewLoadAware returns a local in-flight-aware policy. It is intentionally
// described as load-aware rather than GPU-aware: the snapshot currently only
// contains gateway-owned in-flight counts.
func NewLoadAware() Strategy { return loadAwareStrategy{} }

func (loadAwareStrategy) Name() string { return ModeLoadAware }

func (loadAwareStrategy) Select(prefixKey string, snapshot Snapshot) (Decision, error) {
	if len(snapshot.Backends) == 0 {
		return Decision{}, fmt.Errorf("load-aware routing requires at least one backend")
	}
	hash := sha256.Sum256([]byte(prefixKey))
	start := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(snapshot.Backends)))
	best := snapshot.Backends[start]
	bestInflight := snapshot.Inflight[best.ID]
	for offset := 1; offset < len(snapshot.Backends); offset++ {
		candidate := snapshot.Backends[(start+offset)%len(snapshot.Backends)]
		candidateInflight := snapshot.Inflight[candidate.ID]
		if candidateInflight < bestInflight {
			best, bestInflight = candidate, candidateInflight
		}
	}
	return Decision{Backend: best, Reason: "least-inflight"}, nil
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
	default:
		return nil, fmt.Errorf("unsupported routing mode %q", mode)
	}
}
