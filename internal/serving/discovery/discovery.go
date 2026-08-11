// Package discovery publishes backend snapshots independently from routing.
package discovery

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	ModeStatic        Mode = "static"
	ModeEndpointSlice Mode = "endpointslice"

	StatusOK          Status = "ok"
	StatusDegraded    Status = "degraded"
	StatusUnavailable Status = "unavailable"
)

// Mode 选择静态列表或 Kubernetes EndpointSlice 实现。
type Mode string

// Validate 检查 discovery 实现是否属于当前进程支持的集合。
func (m Mode) Validate() error {
	switch m {
	case ModeStatic, ModeEndpointSlice:
		return nil
	default:
		return fmt.Errorf("unsupported discovery mode %q", m)
	}
}

// Status describes whether a discovery snapshot can be used.
type Status string

// Resolver returns a point-in-time backend snapshot.
type Resolver interface {
	Snapshot(context.Context) ([]backend.Backend, error)
	Status() ResolverStatus
	Close() error
}

// ResolverStatus describes discovery freshness independently from the cached
// backend list. Callers decide when a degraded cache is too old for readiness.
type ResolverStatus struct {
	Status          Status
	LastSuccess     time.Time
	LastError       string
	Freshness       time.Duration
	ReadyBackends   int
	ResourceVersion string
}

// EndpointSliceConfig configures namespace-scoped Kubernetes discovery.
// The implementation uses the REST API directly to avoid leaking client-go
// types into the domain contract.
type EndpointSliceConfig struct {
	Namespace       string
	ServiceName     string
	BaseURL         string
	TokenFile       string
	CAFile          string
	HTTPClient      *http.Client
	RefreshInterval time.Duration
}

// Config 是组合根选择 discovery 实现所需的完整配置。
type Config struct {
	Mode          Mode
	Static        []backend.Backend
	EndpointSlice EndpointSliceConfig
}
