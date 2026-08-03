// Package observation owns per-backend telemetry samples and their freshness.
package observation

import (
	"context"
	"net/http"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
)

const (
	ModeNone       Mode = "none"
	ModePrometheus Mode = "prometheus"

	StatusOK          Status = "ok"
	StatusDegraded    Status = "degraded"
	StatusUnavailable Status = "unavailable"
)

// Mode 选择是否启用 backend Prometheus 观测。
type Mode string

// Status describes the aggregate quality of one backend observation.
type Status string

// Sample distinguishes an observed zero from a missing or stale value.
type Sample[T any] struct {
	Value      T
	Valid      bool
	ObservedAt time.Time
	Source     string
	Error      string
}

// Backend is the low-cardinality telemetry snapshot for one backend.
type Backend struct {
	Identity            identity.Identity
	Status              Status
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

// Clock supplies observation timestamps and is injectable for freshness tests.
type Clock func() time.Time

// BackendSource supplies membership without coupling observation to a concrete
// discovery implementation.
type BackendSource interface {
	Snapshot(context.Context) ([]backend.Backend, error)
}

// Collector reads one telemetry snapshot from one backend.
type Collector interface {
	Collect(context.Context, backend.Backend) Backend
}

// PrometheusConfig contains the vLLM metrics adapter dependencies.
type PrometheusConfig struct {
	HTTPClient  *http.Client
	MetricsPath string
	Clock       Clock
}

// Reader publishes immutable observation snapshots.
type Reader interface {
	Snapshot() map[backend.ID]Backend
	Close() error
}

// Config contains dependencies and freshness bounds for an observation reader.
type Config struct {
	Interval       time.Duration
	MaxAge         time.Duration
	RequestTimeout time.Duration
	Clock          Clock
}

// Dependencies 由组合根注入 discovery、collector 和可选 identity adapter。
type Dependencies struct {
	Resolver  BackendSource
	Collector Collector
	Identity  identity.Enricher
}
