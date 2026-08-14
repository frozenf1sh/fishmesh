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
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
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
	AdmissionTuning admission.TuningConfig
	Transport       transport.Config
	RequestPath     requestpath.Config
	Tokenization    tokenization.Config
	KVCache         kvcache.Config
	Prediction      prediction.Config
	StaticProfile   prediction.StaticProfile
}

// Validate 执行不需要外部 I/O 的跨配置约束检查。
func (c Config) Validate() error {
	if c.Process.ListenAddress == "" || c.Process.ReadHeaderTimeout <= 0 || c.Process.ShutdownTimeout <= 0 {
		return fmt.Errorf("process listen address and timeouts must be configured")
	}
	if err := c.Gateway.Validate(); err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	if err := c.RequestPath.Validate(); err != nil {
		return fmt.Errorf("requestpath: %w", err)
	}
	if err := c.Routing.Validate(); err != nil {
		return fmt.Errorf("routing: %w", err)
	}
	if err := c.Circuit.Validate(); err != nil {
		return fmt.Errorf("circuit: %w", err)
	}
	if err := c.Admission.Validate(); err != nil {
		return fmt.Errorf("admission: %w", err)
	}
	if err := c.AdmissionTuning.Validate(c.Admission.MaxInflight); err != nil {
		return fmt.Errorf("admission tuning: %w", err)
	}
	if err := c.Transport.Validate(); err != nil {
		return fmt.Errorf("transport: %w", err)
	}
	if c.Discovery.Mode == discovery.ModeEndpointSlice {
		endpointSlice := c.Discovery.EndpointSlice
		if endpointSlice.Namespace == "" || endpointSlice.ServiceName == "" || endpointSlice.RefreshInterval <= 0 || c.RequestPath.DiscoveryMaxAge <= 0 {
			return fmt.Errorf("EndpointSlice discovery requires namespace, service and positive freshness bounds")
		}
	}
	if err := c.Discovery.Mode.Validate(); err != nil {
		return err
	}
	if err := c.ObservationMode.Validate(); err != nil {
		return err
	}
	if c.ObservationMode == observation.ModePrometheus {
		if err := c.Observation.Validate(); err != nil {
			return fmt.Errorf("Prometheus observation: %w", err)
		}
		if err := c.Prometheus.Validate(); err != nil {
			return fmt.Errorf("Prometheus: %w", err)
		}
	}
	if c.Routing.Mode == routing.ModeKVAware {
		if c.Discovery.Mode != discovery.ModeEndpointSlice {
			return fmt.Errorf("kv-aware requires EndpointSlice discovery")
		}
		if err := c.Tokenization.Validate(); err != nil {
			return fmt.Errorf("kv-aware tokenization: %w", err)
		}
		if err := c.KVCache.Validate(); err != nil {
			return fmt.Errorf("kv-aware KV cache: %w", err)
		}
		if c.Routing.KVAware.EstimatorMode == routing.KVAwareEstimatorStatic {
			if err := c.StaticProfile.Validate(); err != nil {
				return fmt.Errorf("kv-aware static profile: %w", err)
			}
			if !c.StaticProfile.Calibrated {
				return fmt.Errorf("kv-aware static profile must be calibrated before active routing")
			}
			if c.StaticProfile.Identity.Model != c.Tokenization.Model {
				return fmt.Errorf("kv-aware static profile model must match tokenization model")
			}
		}
	}
	if err := c.Prediction.Validate(); err != nil {
		return fmt.Errorf("prediction: %w", err)
	}
	if c.Discovery.Mode == discovery.ModeStatic && len(c.Discovery.Static) == 0 {
		return fmt.Errorf("%s routing requires at least one backend endpoint", c.Routing.Mode)
	}
	return nil
}
