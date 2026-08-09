package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/frozenf1sh/fishmesh/internal/platform/kubernetes"
	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
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

type runtime struct {
	handler      http.Handler
	requestPath  requestpath.Path
	observations observation.Reader
	resolver     discovery.Resolver
	transport    transport.Pool
	kvCache      kvcache.Index

	close sync.Once
}

type requestComponents struct {
	strategy  routing.Strategy
	breaker   circuit.Breaker
	admission admission.Controller
	pool      transport.Pool
	metrics   *gateway.Metrics
}

// kvEventMetricsObserver 是组合根的协议翻译点：kvcache 只发布稳定值对象，Gateway metrics 只接收
// 已成功 apply 的低基数值。它不进入 requestpath/routing，因此观测不能影响选路或降级。
type kvEventMetricsObserver struct {
	metrics *gateway.Metrics
}

func (o kvEventMetricsObserver) ObserveKVEvent(observation kvcache.EventObservation) {
	o.metrics.ObserveKVEvent(string(observation.Backend), observation.Replayed, observation.PublishToApply)
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

	// 4. exact 模式由组合根显式创建 Render adapter、ZMQ source 与有界 KV index。
	var tokenizer tokenization.Tokenizer
	var index kvcache.Index
	var reconcile func(context.Context, []backend.Backend) error
	if config.Routing.Mode == routing.ModeExactCacheLoad {
		tokenizer, err = tokenization.NewVLLMRenderer(config.Tokenization, tokenization.Dependencies{HTTPClient: http.DefaultClient})
		if err != nil {
			assembled.Close()
			return nil, fmt.Errorf("create exact tokenizer: %w", err)
		}
		index, err = kvcache.NewVLLM(context.Background(), config.KVCache, kvcache.Dependencies{
			EventSource: kvcache.NewZMQSource(), EventObserver: kvEventMetricsObserver{metrics: components.metrics},
		})
		if err != nil {
			assembled.Close()
			return nil, fmt.Errorf("create exact KV cache: %w", err)
		}
		assembled.kvCache = index
		reconcile = exactKVReconcile(index, config.Tokenization.Model)
	}

	// 5. 组合 RequestPath；backend 移除后在锁外统一清理 transport、metrics 和 KV instance。
	pathService, err := requestpath.New(config.RequestPath, requestpath.Dependencies{
		Resolver: resolver, Observations: observations, Strategy: components.strategy, Circuits: components.breaker,
		Tokenizer: tokenizer, KVCache: index, KVReconcile: reconcile,
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

	// 6. 最后创建只依赖接口的 HTTP/SSE delivery。
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
		if r.kvCache != nil {
			_ = r.kvCache.Close()
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

// exactKVReconcile 将 EndpointSlice 发布的 Pod UID/IP 翻译为 kvcache.Instance。
// 这里是 Kubernetes discovery 与 ZMQ endpoint 的唯一交汇处；routing/requestpath 不了解两种协议。
func exactKVReconcile(index kvcache.Index, model string) func(context.Context, []backend.Backend) error {
	return func(ctx context.Context, backends []backend.Backend) error {
		instances := make([]kvcache.Instance, 0, len(backends))
		for _, candidate := range backends {
			instance, err := kvInstance(candidate, model)
			if err != nil {
				return err
			}
			instances = append(instances, instance)
		}
		return index.Reconcile(ctx, instances)
	}
}

func kvInstance(candidate backend.Backend, model string) (kvcache.Instance, error) {
	parsed, err := url.Parse(candidate.URL)
	if err != nil || parsed.Hostname() == "" {
		return kvcache.Instance{}, fmt.Errorf("exact KV backend URL %q: %w", candidate.URL, err)
	}
	uid := candidate.Metadata[backend.MetadataPodUID]
	if uid == "" {
		return kvcache.Instance{}, fmt.Errorf("exact KV backend %q has no Pod UID", candidate.ID)
	}
	host := parsed.Hostname()
	podIdentifier := net.JoinHostPort(host, strconv.Itoa(8000))
	return kvcache.Instance{
		Backend: candidate.ID, PodUID: kvcache.WorkloadUID(uid), PodIdentifier: podIdentifier, Model: model,
		EventsEndpoint: "tcp://" + net.JoinHostPort(host, strconv.Itoa(5557)), ReplayEndpoint: "tcp://" + net.JoinHostPort(host, strconv.Itoa(5558)),
	}, nil
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
