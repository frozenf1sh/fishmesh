package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/frozenf1sh/fishmesh/internal/platform/kubernetes"
	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/gateway"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

type runtime struct {
	handler      http.Handler
	requestPath  requestpath.Path
	observations observation.Reader
	resolver     discovery.Resolver
	transport    transport.Pool

	close sync.Once
}

type requestComponents struct {
	strategy  routing.Strategy
	breaker   circuit.Breaker
	admission admission.Controller
	pool      transport.Pool
	metrics   *gateway.Metrics
}

// buildRuntime 是唯一实现装配点。domain 构造函数不读取环境变量，Gateway 也不创建具体实现。
func buildRuntime(config servingconfig.Config, logger *slog.Logger) (*runtime, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate serving config: %w", err)
	}

	// 1. 创建 discovery，以及 EndpointSlice/identity 共用的 Kubernetes HTTP client。
	discoveryConfig, identityConfig, err := kubernetesConfigs(config)
	if err != nil {
		return nil, err
	}
	resolver, err := discovery.New(discoveryConfig)
	if err != nil {
		return nil, fmt.Errorf("create discovery: %w", err)
	}
	assembled := &runtime{resolver: resolver}

	// 2. 按配置创建可选的慢速 backend observation。
	observations, err := buildObservations(config, identityConfig, resolver)
	if err != nil {
		assembled.Close()
		return nil, err
	}
	assembled.observations = observations

	// 3. 创建 RequestPath 和 delivery 需要的进程内组件。
	components, err := buildRequestComponents(config)
	if err != nil {
		assembled.Close()
		return nil, err
	}
	assembled.transport = components.pool

	// 4. 组合 RequestPath；backend 移除后在锁外统一清理 transport 和 metrics。
	pathService, err := requestpath.New(config.RequestPath, requestpath.Dependencies{
		Resolver: resolver, Observations: observations, Strategy: components.strategy, Circuits: components.breaker,
		OnBackendRemoved: func(backendID backend.ID) {
			components.pool.Remove(backendID)
			components.metrics.DeleteBackend(string(backendID), string(config.Routing.Mode))
		},
	})
	if err != nil {
		assembled.Close()
		return nil, fmt.Errorf("create request path: %w", err)
	}
	assembled.requestPath = pathService

	// 5. 最后创建只依赖接口的 HTTP/SSE delivery。
	server, err := gateway.New(config.Gateway, gateway.Dependencies{
		RequestPath: pathService, Admission: components.admission, Transport: components.pool,
		Metrics: components.metrics, Logger: logger,
	})
	if err != nil {
		assembled.Close()
		return nil, fmt.Errorf("create gateway delivery: %w", err)
	}
	assembled.handler = server.Handler()
	return assembled, nil
}

func buildRequestComponents(config servingconfig.Config) (requestComponents, error) {
	strategy, err := routing.NewConfigured(config.Routing)
	if err != nil {
		return requestComponents{}, fmt.Errorf("create routing strategy: %w", err)
	}
	breaker, err := circuit.New(config.Circuit)
	if err != nil {
		return requestComponents{}, fmt.Errorf("create circuit breaker: %w", err)
	}
	admissionController, err := admission.New(config.Admission)
	if err != nil {
		return requestComponents{}, fmt.Errorf("create admission controller: %w", err)
	}
	return requestComponents{
		strategy: strategy, breaker: breaker, admission: admissionController,
		pool: transport.New(config.Transport), metrics: gateway.NewMetrics(),
	}, nil
}

// Close 按依赖反序关闭 owner，保证 reconcile 回调不会访问已关闭的 transport。
func (r *runtime) Close() {
	r.close.Do(func() {
		if r.requestPath != nil {
			_ = r.requestPath.Close()
		}
		if r.observations != nil {
			_ = r.observations.Close()
		}
		if r.resolver != nil {
			_ = r.resolver.Close()
		}
		if r.transport != nil {
			r.transport.Close()
		}
	})
}

func kubernetesConfigs(config servingconfig.Config) (discovery.Config, identity.Config, error) {
	discoveryConfig := config.Discovery
	identityConfig := config.Identity
	if discoveryConfig.Mode != discovery.ModeEndpointSlice {
		return discoveryConfig, identityConfig, nil
	}
	client := discoveryConfig.EndpointSlice.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	var err error
	if discoveryConfig.EndpointSlice.CAFile != "" {
		client, err = kubernetes.NewHTTPClient(client, discoveryConfig.EndpointSlice.CAFile)
		if err != nil {
			return discovery.Config{}, identity.Config{}, err
		}
	}
	discoveryConfig.EndpointSlice.HTTPClient = client
	identityConfig.HTTPClient = client
	return discoveryConfig, identityConfig, nil
}

func buildObservations(config servingconfig.Config, identityConfig identity.Config, resolver discovery.Resolver) (observation.Reader, error) {
	if config.ObservationMode != observation.ModePrometheus {
		return nil, nil
	}
	var enricher identity.Enricher
	var err error
	if config.Discovery.Mode == discovery.ModeEndpointSlice {
		enricher, err = identity.NewKubernetes(identityConfig)
		if err != nil {
			return nil, fmt.Errorf("create identity enricher: %w", err)
		}
	}
	reader, err := observation.New(config.Observation, observation.Dependencies{
		Resolver: resolver, Collector: observation.NewPrometheus(config.Prometheus), Identity: enricher,
	})
	if err != nil {
		return nil, fmt.Errorf("create observations: %w", err)
	}
	return reader, nil
}
