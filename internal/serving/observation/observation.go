// Package observation owns per-backend telemetry samples and their freshness.
package observation

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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
	Runtime             Runtime
	QueueLength         Sample[float64]
	RunningRequests     Sample[float64]
	PrefixCacheHitRate  float64
	TTFTP95Milliseconds float64
	KVCacheUsagePercent float64
	Error               string
}

// Runtime contains optional resource/runtime signals explicitly attributed to a
// backend Pod. Missing fields are invalid samples, never implicit zeroes.
type Runtime struct {
	ObservedAt            time.Time
	CPUUsageCores         Sample[float64]
	MemoryUsageBytes      Sample[float64]
	GPUUtilizationPercent Sample[float64]
	GPUMemoryUsedBytes    Sample[float64]
	GPUTemperatureCelsius Sample[float64]
	Source                string
	Error                 string
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
	Runtime     RuntimePrometheusConfig
}

// Validate 检查 Prometheus adapter 的协议配置；HTTP client 和 clock 由实现层作为运行时依赖注入。
func (c PrometheusConfig) Validate() error {
	if strings.TrimSpace(c.MetricsPath) == "" {
		return fmt.Errorf("prometheus metrics path must not be empty")
	}
	if err := c.Runtime.Validate(); err != nil {
		return fmt.Errorf("runtime prometheus: %w", err)
	}
	return nil
}

// RuntimePrometheusConfig configures optional Prometheus HTTP API queries.
// Queries must contain both $namespace and $pod so a node-wide value cannot be
// accidentally attached to one backend.
type RuntimePrometheusConfig struct {
	Endpoint            string
	Namespace           string
	CPUQuery            string
	MemoryQuery         string
	GPUUtilizationQuery string
	GPUMemoryQuery      string
	GPUTemperatureQuery string
	HTTPClient          *http.Client
	Clock               Clock
}

// Validate allows the runtime adapter to be completely disabled by leaving its
// endpoint empty, while rejecting partially configured identity-unsafe queries.
func (c RuntimePrometheusConfig) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		if c.hasQuery() {
			return fmt.Errorf("runtime prometheus endpoint is required when a query is configured")
		}
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("runtime prometheus endpoint must be an absolute HTTP URL: %q", c.Endpoint)
	}
	if strings.TrimSpace(c.Namespace) == "" {
		return fmt.Errorf("runtime prometheus namespace must not be empty")
	}
	if !c.hasQuery() {
		return fmt.Errorf("runtime prometheus requires at least one query")
	}
	for name, query := range map[string]string{
		"cpu": c.CPUQuery, "memory": c.MemoryQuery, "gpu utilization": c.GPUUtilizationQuery,
		"gpu memory": c.GPUMemoryQuery, "gpu temperature": c.GPUTemperatureQuery,
	} {
		if strings.TrimSpace(query) == "" {
			continue
		}
		if !strings.Contains(query, "$namespace") || !strings.Contains(query, "$pod") {
			return fmt.Errorf("runtime prometheus %s query must contain $namespace and $pod", name)
		}
	}
	return nil
}

func (c RuntimePrometheusConfig) hasQuery() bool {
	return strings.TrimSpace(c.CPUQuery) != "" || strings.TrimSpace(c.MemoryQuery) != "" ||
		strings.TrimSpace(c.GPUUtilizationQuery) != "" || strings.TrimSpace(c.GPUMemoryQuery) != "" ||
		strings.TrimSpace(c.GPUTemperatureQuery) != ""
}

// RuntimeCollector reads optional resource metrics after the backend identity
// has been resolved. It is evidence-only until a later routing stage opts in.
type RuntimeCollector interface {
	Collect(context.Context, backend.Backend, identity.Identity) Runtime
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
	Runtime   RuntimeCollector
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
