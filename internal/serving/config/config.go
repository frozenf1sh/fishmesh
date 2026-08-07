// Package config 负责把环境变量映射为各 domain 配置，并向组合根提供已校验的进程配置。
package config

import (
	"fmt"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/gateway"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

// ProcessConfig 只描述监听和优雅关停，不传入任何 domain。
type ProcessConfig struct {
	ListenAddress     string
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
}

// Config 按 owner 保存各 domain 配置；cmd 只负责按依赖顺序构造它们。
type Config struct {
	Process         ProcessConfig
	Gateway         gateway.Config
	Discovery       discovery.Config
	Identity        identity.Config
	ObservationMode observation.Mode
	Observation     observation.Config
	Prometheus      observation.PrometheusConfig
	Routing         routing.Config
	Circuit         circuit.Config
	Admission       admission.Config
	Transport       transport.Config
	RequestPath     requestpath.Config
	Tokenization    tokenization.Config
	KVCache         kvcache.Config
}

// Validate 执行不需要外部 I/O 的跨配置约束检查。
func (c Config) Validate() error {
	if c.Process.ListenAddress == "" || c.Process.ReadHeaderTimeout <= 0 || c.Process.ShutdownTimeout <= 0 {
		return fmt.Errorf("process listen address and timeouts must be configured")
	}
	if err := c.RequestPath.Service.Validate(); err != nil {
		return fmt.Errorf("service backend: %w", err)
	}
	if c.Gateway.MaxRequestBodyBytes <= 0 || c.Admission.MaxInflight <= 0 || c.Transport.MaxConnsPerHost <= 0 || c.Transport.RequestTimeout <= 0 {
		return fmt.Errorf("gateway body, admission and transport limits must be positive")
	}
	if c.Discovery.Mode == discovery.ModeEndpointSlice {
		endpointSlice := c.Discovery.EndpointSlice
		if endpointSlice.Namespace == "" || endpointSlice.ServiceName == "" || endpointSlice.RefreshInterval <= 0 || c.RequestPath.DiscoveryMaxAge <= 0 {
			return fmt.Errorf("EndpointSlice discovery requires namespace, service and positive freshness bounds")
		}
	}
	if c.Discovery.Mode != discovery.ModeStatic && c.Discovery.Mode != discovery.ModeEndpointSlice {
		return fmt.Errorf("unsupported discovery mode %q", c.Discovery.Mode)
	}
	if c.ObservationMode != observation.ModeNone && c.ObservationMode != observation.ModePrometheus {
		return fmt.Errorf("unsupported observation mode %q", c.ObservationMode)
	}
	if c.ObservationMode == observation.ModePrometheus && (c.Observation.Interval <= 0 || c.Observation.MaxAge <= 0) {
		return fmt.Errorf("Prometheus observation interval and max age must be positive")
	}
	if !supportedRoutingMode(c.Routing.Mode) {
		return fmt.Errorf("unsupported routing mode %q", c.Routing.Mode)
	}
	if c.Routing.Mode == routing.ModeExactCacheLoad {
		if c.Discovery.Mode != discovery.ModeEndpointSlice {
			return fmt.Errorf("exact-cache-load requires EndpointSlice discovery")
		}
		if err := c.Tokenization.Validate(); err != nil {
			return fmt.Errorf("exact tokenization: %w", err)
		}
		if err := c.KVCache.Validate(); err != nil {
			return fmt.Errorf("exact KV cache: %w", err)
		}
	}
	if c.Routing.Mode == routing.ModeBoundedAffinity {
		bounded := c.Routing.BoundedAffinity
		if bounded.TTL <= 0 || bounded.MaxEntries <= 0 || bounded.InflightDelta < 0 || bounded.QueueDepthDelta < 0 {
			return fmt.Errorf("bounded affinity limits must be positive")
		}
	}
	if c.Circuit.EWMAAlpha <= 0 || c.Circuit.EWMAAlpha > 1 || c.Circuit.ErrorThreshold <= 0 || c.Circuit.ErrorThreshold > 1 || c.Circuit.MinimumRequests <= 0 || c.Circuit.OpenDuration <= 0 {
		return fmt.Errorf("circuit limits must be positive and bounded")
	}
	if c.Discovery.Mode == discovery.ModeStatic && directRoutingMode(c.Routing.Mode) && len(c.Discovery.Static) == 0 {
		return fmt.Errorf("%s routing requires at least one backend endpoint", c.Routing.Mode)
	}
	return nil
}

func supportedRoutingMode(mode routing.Mode) bool {
	switch mode {
	case routing.ModeService, routing.ModePrefixHash, routing.ModePrefixAffinity, routing.ModeLoadAware, routing.ModeBoundedAffinity, routing.ModeExactCacheLoad:
		return true
	default:
		return false
	}
}

func directRoutingMode(mode routing.Mode) bool {
	return mode != routing.ModeService
}
