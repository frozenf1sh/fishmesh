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
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
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
	renderClient *http.Client
	kvCache      kvcache.Index
	tuner        admission.Tuner

	close sync.Once
}

type requestComponents struct {
	strategy  routing.Strategy
	breaker   circuit.Breaker
	admission admission.Controller
	tuner     admission.Tuner
	pool      transport.Pool
	metrics   *gateway.Metrics
}

// kvEventMetricsObserver 是组合根的协议翻译点：kvcache 只发布稳定值对象，Gateway metrics 只接收
// 已成功 apply 的低基数值。它不进入 requestpath/routing，因此指标观测不能反向影响选路或降级。
type kvEventMetricsObserver struct {
	metrics *gateway.Metrics
}

func (o kvEventMetricsObserver) ObserveKVEvent(observation kvcache.EventObservation) {
	o.metrics.ObserveKVEvent(string(observation.Backend), observation.Replayed, observation.PublishToApply)
}

func (o kvEventMetricsObserver) ObserveSequenceReset(observation kvcache.SequenceResetObservation) {
	o.metrics.ObserveKVSequenceReset(string(observation.Backend), observation.Replayed)
}

func (o kvEventMetricsObserver) ObserveKVEventError(observation kvcache.EventErrorObservation) {
	o.metrics.ObserveKVEventError(string(observation.Backend), string(observation.Reason))
}

// buildRuntime 是 Gateway 的唯一实现装配点。
//
// 它按依赖方向从叶子能力开始创建：先 discovery/observation，再 strategy/circuit/
// transport/admission，再按 KV-aware 模式决定是否创建 Render 与 KV index，最后创建
// requestpath 和 HTTP delivery。每一个中途失败分支都调用 assembled.Close，确保已经
// 创建的 watcher、subscriber、连接池和其他有状态资源不会留在半初始化进程中。
//
// domain 构造函数不读取环境变量，Gateway 也不创建具体实现；组合根是 Kubernetes
// EndpointSlice、Pod UID、ZMQ endpoint 和稳定 kvcache.Instance 唯一交汇的位置。
func buildRuntime(config servingconfig.Config, logger *slog.Logger) (*runtime, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate serving config: %w", err)
	}

	// 1. 创建 discovery，以及 EndpointSlice/identity 共用的 Kubernetes HTTP client。
	//    共用 client 只发生在组合根，具体 domain 仍只看到自己的 Config 和能力接口。
	discoveryConfig, identityConfig, err := kubernetesConfigs(config)
	if err != nil {
		return nil, err
	}
	resolver, err := discovery.New(discoveryConfig)
	if err != nil {
		return nil, fmt.Errorf("create discovery: %w", err)
	}
	assembled := &runtime{resolver: resolver}

	// 2. 按配置创建有界的可选 backend observation。
	//    observation 是请求路径的输入快照，不在这里预先计算路由结果；禁用观测时
	//    返回 nil，requestpath 会按其明确的 unknown/load fallback 语义继续工作。
	observations, err := buildObservations(config, identityConfig, resolver)
	if err != nil {
		assembled.Close()
		return nil, err
	}
	assembled.observations = observations

	// 3. 创建 RequestPath 和 delivery 需要的进程内组件。
	//    strategy、breaker、admission、transport 和 metrics 都由同一个 runtime 持有，
	//    这样 backend membership 变化时可以在一个回调里同步清理 transport 与指标。
	components, err := buildRequestComponents(config)
	if err != nil {
		assembled.Close()
		return nil, err
	}
	assembled.transport = components.pool
	assembled.tuner = components.tuner

	// 4. KV-aware 模式由组合根显式创建 Render adapter、ZMQ source 与有界 KV index。
	//    非 KV-aware 模式不启动 KV subscriber，也不为每个请求额外调用 Render；这是产品
	//    模式边界，而不是 requestpath 在运行时偷偷猜测依赖是否存在。
	var tokenizer tokenization.Tokenizer
	var index kvcache.Index
	var reconcile func(context.Context, []backend.Backend) error
	if config.Routing.Mode == routing.ModeKVAware {
		renderClient, clientErr := newRenderHTTPClient(config.Tokenization)
		if clientErr != nil {
			assembled.Close()
			return nil, fmt.Errorf("create KV-aware Render client: %w", clientErr)
		}
		assembled.renderClient = renderClient
		tokenizer, err = tokenization.NewVLLMRenderer(config.Tokenization, tokenization.Dependencies{HTTPClient: renderClient})
		if err != nil {
			assembled.Close()
			return nil, fmt.Errorf("create KV-aware tokenizer: %w", err)
		}
		index, err = kvcache.NewVLLM(context.Background(), config.KVCache, kvcache.Dependencies{
			EventSource: kvcache.NewZMQSource(), EventObserver: kvEventMetricsObserver{metrics: components.metrics},
		})
		if err != nil {
			assembled.Close()
			return nil, fmt.Errorf("create KV cache: %w", err)
		}
		assembled.kvCache = index
		reconcile = kvAwareReconcile(index, config.Tokenization.Model)
	}

	// 5. prediction tracker 只以 shadow 模式记录实际 TTFT；static estimator 是构造后
	//    不可变的已校准 profile，由 requestpath 投影为 routing 稳定值。
	//    预测器只接受请求完成后回写的首事件观测，不参与本次选择，避免影子实验改变
	//    kv-aware 的行为或把未经验证的预测引入生产路由。
	predictor, err := prediction.New(config.Prediction)
	if err != nil {
		assembled.Close()
		return nil, fmt.Errorf("create TTFT predictor: %w", err)
	}
	var staticEstimator *prediction.StaticEstimator
	if config.Routing.Mode == routing.ModeKVAware && config.Routing.KVAware.EstimatorMode == routing.KVAwareEstimatorStatic {
		staticEstimator, err = prediction.NewStaticEstimator(config.StaticProfile)
		if err != nil {
			assembled.Close()
			return nil, fmt.Errorf("create static TTFT estimator: %w", err)
		}
	}

	// 6. 组合 RequestPath；backend 移除后在锁外统一清理 transport、metrics 和 KV instance。
	//    回调只执行不依赖 requestpath 内部锁的清理动作；具体 membership 对齐和 lease
	//    等待仍由 requestpath owner 管理，避免跨 domain 形成锁顺序依赖。
	pathService, err := requestpath.New(config.RequestPath, requestpath.Dependencies{
		Resolver: resolver, Observations: observations, Strategy: components.strategy, Circuits: components.breaker,
		Tokenizer: tokenizer, KVCache: index, KVReconcile: reconcile,
		Predictor: predictor, StaticEstimator: staticEstimator,
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

	// 7. 最后创建只依赖接口的 HTTP/SSE delivery。
	//    直到 requestpath 和 transport 都构造成功，Gateway 才会暴露 handler，确保
	//    health/readiness 不会把一个尚未完成装配的 runtime 对外宣布为可用。
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
	metrics := gateway.NewMetrics()
	tuner, err := admission.NewTuner(config.AdmissionTuning, admissionController, metrics.AdmissionSignal, metrics.ObserveAdmissionTuning)
	if err != nil {
		return requestComponents{}, fmt.Errorf("create admission tuner: %w", err)
	}
	return requestComponents{
		strategy: strategy, breaker: breaker, admission: admissionController,
		tuner: tuner, pool: transport.New(config.Transport), metrics: metrics,
	}, nil
}

// Close 按依赖反序关闭所有有状态 owner，并且保证重复调用安全。
//
// 先关闭 requestpath，阻止新的选择和 membership 回调；再关闭 KV subscriber、
// observation watcher、discovery watcher，最后关闭 transport 连接池。这个顺序保证
// 任何仍在运行的 reconcile 都不会访问已经销毁的下游资源。sync.Once 使启动失败清理、
// defer 清理和测试显式清理可以安全地走同一条路径。
func (r *runtime) Close() {
	r.close.Do(func() {
		if r.tuner != nil {
			_ = r.tuner.Close()
		}
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
		if r.renderClient != nil {
			r.renderClient.CloseIdleConnections()
		}
		if r.transport != nil {
			r.transport.Close()
		}
	})
}

// kvAwareReconcile 将 EndpointSlice 发布的 Pod UID/IP 翻译为 kvcache.Instance。
//
// EndpointSlice 提供的是 Kubernetes backend URL、Pod UID 和 membership；kvcache 需要
// 的是模型、Pod 标识以及 live/replay ZMQ 地址。翻译集中在这里，保证 routing 和
// requestpath 只处理稳定值对象，不认识 Kubernetes wire type、端口约定或 ZMQ topic。
// 每次 reconcile 都按当前完整 membership 构造 desired instances，由 kvcache owner
// 负责比较、启动、停止和清理 subscriber。
func kvAwareReconcile(index kvcache.Index, model string) func(context.Context, []backend.Backend) error {
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

// kvInstance 将一个 backend 的 HTTP 地址和 Pod UID转换为 KV subscriber 所需的实例配置。
//
// KV event 端口是 vLLM 实例协议的一部分：5557 用于 live events，5558 用于 replay。
// 地址必须来自已选 discovery snapshot，Pod UID 不能缺失，因为 IP 可能在 Pod 重建后
// 被复用；没有 UID 时宁可让 reconcile 失败，也不能把新 Pod 的事件接到旧缓存状态。
func kvInstance(candidate backend.Backend, model string) (kvcache.Instance, error) {
	parsed, err := url.Parse(candidate.URL)
	if err != nil || parsed.Hostname() == "" {
		return kvcache.Instance{}, fmt.Errorf("KV event backend URL %q: %w", candidate.URL, err)
	}
	uid := candidate.Metadata[backend.MetadataPodUID]
	if uid == "" {
		return kvcache.Instance{}, fmt.Errorf("KV event backend %q has no Pod UID", candidate.ID)
	}
	host := parsed.Hostname()
	podIdentifier := net.JoinHostPort(host, strconv.Itoa(8000))
	return kvcache.Instance{
		Backend: candidate.ID, PodUID: kvcache.WorkloadUID(uid), PodIdentifier: podIdentifier, Model: model,
		EventsEndpoint: "tcp://" + net.JoinHostPort(host, strconv.Itoa(5557)), ReplayEndpoint: "tcp://" + net.JoinHostPort(host, strconv.Itoa(5558)),
	}, nil
}

// kubernetesConfigs 为 discovery 和 identity 准备共享的 Kubernetes HTTP client。
//
// 只有 EndpointSlice discovery 需要 CA 配置和 Kubernetes API client；static discovery
// 保持调用方提供的配置不变。HTTP client 的创建在组合根完成，domain 构造函数因此不
// 读取文件、不管理隐藏 client，也不会在多个 owner 中重复加载 CA。
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

// buildObservations 按 observation mode 创建可选的 backend 观测 reader。
//
// Prometheus 模式下，EndpointSlice discovery 才需要额外的 Kubernetes identity enricher；
// static 模式不访问 Pod/Node API。禁用模式返回 nil，保留 requestpath 对缺失观测的
// 显式降级语义，而不是创建一个看似可用但永远没有数据的 reader。
func buildObservations(config servingconfig.Config, identityConfig identity.Config, resolver discovery.Resolver) (observation.Reader, error) {
	if config.ObservationMode != observation.ModePrometheus {
		return nil, nil
	}
	var enricher identity.Enricher
	var runtimeCollector observation.RuntimeCollector
	var err error
	if config.Discovery.Mode == discovery.ModeEndpointSlice {
		enricher, err = identity.NewKubernetes(identityConfig)
		if err != nil {
			return nil, fmt.Errorf("create identity enricher: %w", err)
		}
	}
	if config.Prometheus.Runtime.Endpoint != "" {
		runtimeCollector, err = observation.NewPrometheusRuntime(config.Prometheus.Runtime)
		if err != nil {
			return nil, fmt.Errorf("create runtime observations: %w", err)
		}
	}
	reader, err := observation.New(config.Observation, observation.Dependencies{
		Resolver: resolver, Collector: observation.NewPrometheus(config.Prometheus), Identity: enricher, Runtime: runtimeCollector,
	})
	if err != nil {
		return nil, fmt.Errorf("create observations: %w", err)
	}
	return reader, nil
}
