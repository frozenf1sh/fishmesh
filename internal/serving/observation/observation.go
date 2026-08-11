// Package observation owns per-backend telemetry samples and their freshness.
package observation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// Validate 检查观测实现是否属于当前进程支持的集合。
func (m Mode) Validate() error {
	switch m {
	case ModeNone, ModePrometheus:
		return nil
	default:
		return fmt.Errorf("unsupported observation mode %q", m)
	}
}

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

// Validate 检查 Prometheus adapter 的协议配置；HTTP client 和 clock 由实现层作为运行时依赖注入。
func (c PrometheusConfig) Validate() error {
	if strings.TrimSpace(c.MetricsPath) == "" {
		return fmt.Errorf("prometheus metrics path must not be empty")
	}
	return nil
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

// Validate 检查观测 reader 必须具备的固定数据源和 collector。
func (d Dependencies) Validate() error {
	if d.Resolver == nil || d.Collector == nil {
		return fmt.Errorf("observation resolver and collector must not be nil")
	}
	return nil
}

// Validate 检查观测采样周期和 freshness 边界。Clock 允许为空，由构造函数补入标准时钟。
func (c Config) Validate() error {
	if c.Interval <= 0 || c.MaxAge <= 0 || c.RequestTimeout <= 0 {
		return fmt.Errorf("observation interval, max age and request timeout must be positive")
	}
	return nil
}
