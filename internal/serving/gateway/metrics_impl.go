package gateway

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 集中管理 Gateway 暴露的全部 Prometheus 指标。
//
// 指标命名遵循 Prometheus 惯例：命名空间 fishmesh + 子系统 gateway +
// 指标名，语义后缀 _total（counter）/ _seconds（时间直方图）。
//
// 第一轮实验的设计决定：指标直接以 Prometheus exposition 格式暴露在
// /metrics，不依赖独立的 Prometheus 服务。抓取方（或人工 curl）拿到
// 数据后自行聚合，减少实验环境的运行面。
type Metrics struct {
	registry              *prometheus.Registry
	inflight              prometheus.Gauge         // 当前正在代理中的请求数（反映瞬时并发）
	requests              *prometheus.CounterVec   // 完成的请求总数，按 method/status 分桶
	requestSeconds        *prometheus.HistogramVec // 端到端请求耗时，按 method/status 分桶
	ttftSeconds           prometheus.Histogram     // 首 token 延迟（TTFT），项目的核心测量指标
	upstreamErrors        prometheus.Counter       // 上游传输层失败次数（区别于业务层 4xx/5xx）
	streamErrors          prometheus.Counter       // response headers 之后的 upstream stream 读取失败
	admissionRejections   prometheus.Counter       // Gateway 达到并发硬上限后拒绝的请求
	circuitOpen           *prometheus.GaugeVec     // endpoint transport circuit 当前是否打开
	circuitOpens          *prometheus.CounterVec   // endpoint transport circuit 打开次数
	routingDecisions      *prometheus.CounterVec   // 路由模式和有限 backend ID 维度
	routingReasons        *prometheus.CounterVec   // 固定枚举 reason，不使用请求 key 作为 label
	sessionKeySpillovers  *prometheus.CounterVec   // session-key 发生溢出的次数，按触发原因分桶
	routeFallbacks        prometheus.Counter       // 路由策略失败后的 Service fallback
	observationStatus     *prometheus.GaugeVec
	observationFreshness  *prometheus.GaugeVec
	observationQueue      *prometheus.GaugeVec
	observationRunning    *prometheus.GaugeVec
	observationIdentity   *prometheus.GaugeVec
	observationGPU        *prometheus.GaugeVec
	discoveryStatus       *prometheus.GaugeVec
	discoveryFreshness    prometheus.Gauge
	discoveryReady        prometheus.Gauge
	kvCacheValid          *prometheus.GaugeVec
	kvCacheFreshness      *prometheus.GaugeVec
	kvCacheLastSequence   *prometheus.GaugeVec
	kvCacheAppliedBatches *prometheus.GaugeVec
	kvCacheReplayBatches  *prometheus.GaugeVec
	kvCacheStatus         *prometheus.GaugeVec
	kvEventPublishToApply *prometheus.HistogramVec
	kvAwareRequests       *prometheus.CounterVec
	kvAwareDegradations   *prometheus.CounterVec
	kvAwareCachedPrefix   prometheus.Histogram
	predictionShadows     *prometheus.CounterVec
	predictionErrors      prometheus.Histogram
	kvCacheMu             sync.Mutex
	kvCacheIDs            map[string]struct{}
	kvCacheStatuses       map[string]string
	observationMu         sync.Mutex
	observationIDs        map[string]struct{}
	identityLabels        map[string][2]string
}

// NewMetrics 创建 standalone Gateway 自己的隔离 Prometheus registry。
//
// 不使用 prometheus.DefaultRegisterer，是为了让每个 Gateway 实例和测试实例拥有
// 独立的指标生命周期，避免多个 runtime 在同一进程内注册同名 collector 时冲突。
// registry 中同时注册进程和 Go runtime 指标；业务指标按请求、路由、discovery、
// observation、KV cache 和预测影子结果分组初始化。
func NewMetrics() *Metrics {
	m := &Metrics{
		registry:        prometheus.NewRegistry(),
		observationIDs:  make(map[string]struct{}),
		identityLabels:  make(map[string][2]string),
		kvCacheIDs:      make(map[string]struct{}),
		kvCacheStatuses: make(map[string]string),
	}
	m.initializeRequestMetrics()
	m.initializeRoutingMetrics()
	m.initializeObservationMetrics()
	m.initializeDiscoveryMetrics()
	m.initializeKVCacheMetrics()
	m.registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}), prometheus.NewGoCollector())
	return m
}

// initializeRequestMetrics 初始化请求数量、请求耗时、TTFT、上游错误和准入拒绝指标。
//
// 请求总数和请求耗时使用 method/status 两个有限维度；TTFT 使用固定 bucket，不把
// model、prompt 或 routing key 引入标签。上游错误和 headers 后 stream 错误分开，
// 这样“请求未建立”和“已经返回 200 但流不完整”不会被同一条曲线掩盖。
func (m *Metrics) initializeRequestMetrics() {
	m.inflight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricInflightRequests, Help: "Requests currently proxied by the gateway.",
	})
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricRequestsTotal, Help: "Completed gateway requests.",
	}, []string{labelMethod, labelStatus})
	m.requestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricRequestDurationSeconds, Help: "End-to-end gateway request duration.",
		Buckets: prometheus.DefBuckets,
	}, []string{labelMethod, labelStatus})
	// TTFT bucket 从 10ms 覆盖到 60s，并在常见的 0.1s～1s 区间保留细粒度。
	m.ttftSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricFirstSSEEventSeconds, Help: "Time from request receipt until the first non-terminal SSE event.",
		Buckets: firstSSEEventBuckets(),
	})
	m.upstreamErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricUpstreamErrorsTotal, Help: "Upstream transport failures.",
	})
	m.streamErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricUpstreamStreamErrorsTotal, Help: "Upstream response-body failures after headers were received.",
	})
	m.admissionRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricAdmissionRejectionsTotal, Help: "Requests rejected because the gateway reached its in-flight limit.",
	})
	m.registry.MustRegister(m.inflight, m.requests, m.requestSeconds, m.ttftSeconds, m.upstreamErrors, m.streamErrors, m.admissionRejections)
}

// initializeRoutingMetrics 初始化路由决策、溢出、fallback、circuit 和预测影子指标。
//
// backend ID 只来自当前 membership，requestpath 删除 backend 时会同步删除对应 label。
// reason、mode、policy 和 prediction outcome 都是固定枚举；尤其不能把客户端提供的
// client session key 作为 Prometheus label，否则高基数请求会造成指标内存和查询压力。
func (m *Metrics) initializeRoutingMetrics() {
	m.circuitOpen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricBackendCircuitOpen, Help: "Whether a backend transport-error circuit is currently open.",
	}, []string{labelBackendID})
	m.circuitOpens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricBackendCircuitOpensTotal, Help: "Number of times a backend transport-error circuit opened.",
	}, []string{labelBackendID})
	m.routingDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricRoutingDecisionsTotal, Help: "Routing decisions by mode and bounded backend identity.",
	}, []string{labelMode, labelBackendID})
	m.routingReasons = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricRoutingReasonsTotal, Help: "Routing decisions by bounded reason enum.",
	}, []string{labelReason})
	m.sessionKeySpillovers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricSessionKeySpilloversTotal, Help: "Session-key spillovers by pressure signal.",
	}, []string{labelReason})
	m.routeFallbacks = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricRouteFallbacksTotal, Help: "Routing decisions that fell back to the Service backend.",
	})
	m.predictionShadows = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricPredictionShadowsTotal, Help: "TTFT shadow prediction results without changing routing.",
	}, []string{labelStatus, labelPredictionOutcome})
	m.predictionErrors = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricPredictionAbsoluteErrorSeconds, Help: "Absolute error between actual first-event TTFT and the selected backend shadow estimate.",
		Buckets: firstSSEEventBuckets(),
	})
	m.registry.MustRegister(m.circuitOpen, m.circuitOpens, m.routingDecisions, m.routingReasons, m.sessionKeySpillovers, m.routeFallbacks, m.predictionShadows, m.predictionErrors)
}

// initializeObservationMetrics 初始化每个 backend 的负载、身份和声明 GPU 资源指标。
//
// queue/running 只有在 observation sample 有效时才写入；无效 sample 会删除旧值，
// 不能把上一轮陈旧数值继续伪装成当前负载。身份 label 由 backend ID、Pod 名和 Node
// 名组成，并在身份变化时先删除旧 tuple，避免一个 backend 同时留下两套身份。
func (m *Metrics) initializeObservationMetrics() {
	m.observationStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricBackendObservationStatus, Help: "Latest backend observation status (one active status has value 1).",
	}, []string{labelBackendID, labelStatus})
	m.observationFreshness = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricObservationFreshnessSeconds, Help: "Age of the latest backend observation.",
	}, []string{labelBackendID})
	m.observationQueue = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricObservationQueueLength, Help: "Latest vLLM queue length by backend.",
	}, []string{labelBackendID})
	m.observationRunning = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricObservationRunningRequests, Help: "Latest vLLM running request count by backend.",
	}, []string{labelBackendID})
	m.observationIdentity = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricBackendIdentityInfo, Help: "Kubernetes identity mapped to a backend address.",
	}, []string{labelBackendID, labelPodName, labelNodeName})
	m.observationGPU = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricBackendGPURequested, Help: "Declared GPU resources requested by the backend Pod.",
	}, []string{labelBackendID})
	m.registry.MustRegister(m.observationStatus, m.observationFreshness, m.observationQueue, m.observationRunning, m.observationIdentity, m.observationGPU)
}

// initializeDiscoveryMetrics 初始化 EndpointSlice membership 的状态、freshness 和 Ready 数量。
//
// status 使用“当前状态值为 1、其他已知状态为 0”的 gauge 表达，便于 Prometheus 直接
// 查询。状态为空时统一按 unavailable 处理，避免出现没有任何 active 状态的歧义。
func (m *Metrics) initializeDiscoveryMetrics() {
	m.discoveryStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricEndpointDiscoveryStatus, Help: "Endpoint discovery status (one active status has value 1).",
	}, []string{labelStatus})
	m.discoveryFreshness = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricDiscoveryFreshnessSeconds, Help: "Age of the last successful endpoint discovery snapshot.",
	})
	m.discoveryReady = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricEndpointDiscoveryReady, Help: "Number of Ready backends in the latest endpoint discovery snapshot.",
	})
	// 注册冲突属于编程错误，应在启动时直接暴露。
	m.registry.MustRegister(m.discoveryStatus, m.discoveryFreshness, m.discoveryReady)
}

// initializeKVCacheMetrics 初始化 KV-aware routing 所需的 KV 状态、事件和降级指标。
//
// Valid、freshness、sequence 和 replay/apply batch 用于判断本地 index 是否可信；
// kvAwareCachedPrefix 只记录 KVStatus=available 的选择，其中 cached prefix 为 0
// 是真实 miss。unknown/stale 不会被记录为零，从而保留“不可用”和“可用但未命中”的区别。
func (m *Metrics) initializeKVCacheMetrics() {
	m.kvCacheValid = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheInstanceValid, Help: "Whether a backend KV cache state is safe for KV-aware routing.",
	}, []string{labelBackendID})
	m.kvCacheFreshness = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheFreshnessSeconds, Help: "Age of the last replay-confirmed KV cache state.",
	}, []string{labelBackendID})
	m.kvCacheLastSequence = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheLastSequence, Help: "Last KV event sequence synchronously applied to the local index.",
	}, []string{labelBackendID})
	m.kvCacheAppliedBatches = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheAppliedBatches, Help: "KV event batches synchronously applied to the local index.",
	}, []string{labelBackendID})
	m.kvCacheReplayBatches = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheReplayBatches, Help: "KV event batches applied through replay.",
	}, []string{labelBackendID})
	m.kvCacheStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheStatus, Help: "Current typed KV cache state reason (one active reason has value 1).",
	}, []string{labelBackendID, labelKVCacheStatus})
	m.kvEventPublishToApply = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVEventPublishToApplySeconds,
		Help: "vLLM publisher timestamp to successful local KV index apply duration; it includes clock skew and is not network RTT.", Buckets: kvEventPublishToApplyBuckets(),
	}, []string{labelBackendID, labelKVEventSource})
	m.kvAwareRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVAwareRequestsTotal, Help: "KV routing selections by stable signal status.",
	}, []string{labelKVStatus})
	m.kvAwareDegradations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVAwareDegradationsTotal, Help: "KV routing selections that explicitly degraded to load-balanced routing.",
	}, []string{labelKVStatus})
	m.kvAwareCachedPrefix = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVAwareCachedPrefixTokens,
		Help: "Cached prefix tokens on an KV-aware selection with available KV signal; zero is a real cache miss, not unavailable state.", Buckets: kvAwareCachedPrefixTokenBuckets(),
	})
	m.registry.MustRegister(m.kvCacheValid, m.kvCacheFreshness, m.kvCacheLastSequence, m.kvCacheAppliedBatches, m.kvCacheReplayBatches, m.kvCacheStatus, m.kvEventPublishToApply, m.kvAwareRequests, m.kvAwareDegradations, m.kvAwareCachedPrefix)
}

// updateBackendObservations 只使用 backend ID 等有界标签，不写入请求 routing key。
//
// 此方法在 observationMu 保护下执行两步同步：先删除已经不在新 snapshot 中的 backend，
// 再更新仍存在的 backend。删除旧状态很重要，因为 Prometheus gauge 不会自动知道
// Kubernetes membership 已经变化；如果不显式删除，旧 Pod 的 queue、identity 或 GPU
// label 会永久残留。
func (m *Metrics) updateBackendObservations(states map[backend.ID]observation.Backend) {
	m.observationMu.Lock()
	defer m.observationMu.Unlock()
	for id := range m.observationIDs {
		if _, ok := states[backend.ID(id)]; ok {
			continue
		}
		m.deleteObservation(id)
	}
	for id, state := range states {
		backendID := string(id)
		m.observationStatus.WithLabelValues(backendID, string(state.Status)).Set(1)
		for _, status := range []observation.Status{observation.StatusOK, observation.StatusDegraded, observation.StatusUnavailable} {
			if status != state.Status {
				m.observationStatus.WithLabelValues(backendID, string(status)).Set(0)
			}
		}
		m.observationFreshness.WithLabelValues(backendID).Set(state.Freshness.Seconds())
		if state.QueueLength.Valid {
			m.observationQueue.WithLabelValues(backendID).Set(state.QueueLength.Value)
		} else {
			m.observationQueue.DeleteLabelValues(backendID)
		}
		if state.RunningRequests.Valid {
			m.observationRunning.WithLabelValues(backendID).Set(state.RunningRequests.Value)
		} else {
			m.observationRunning.DeleteLabelValues(backendID)
		}
		labels := [2]string{state.Identity.PodName, state.Identity.NodeName}
		if previous, ok := m.identityLabels[backendID]; ok && previous != labels {
			m.observationIdentity.DeleteLabelValues(backendID, previous[0], previous[1])
		}
		m.observationIdentity.WithLabelValues(backendID, labels[0], labels[1]).Set(1)
		m.observationGPU.WithLabelValues(backendID).Set(state.Identity.GPURequested)
		m.identityLabels[backendID] = labels
		m.observationIDs[backendID] = struct{}{}
	}
}

// deleteObservation 删除某个 backend 的所有 observation 和 identity label。
//
// 调用方必须已经持有 observationMu；该约束避免删除和 snapshot 更新并发修改内部
// label registry。identity 是二维 label tuple，因此不能只删除 backend_id 后假设
// Prometheus 会自动清理旧的 Pod/Node 组合。
func (m *Metrics) deleteObservation(id string) {
	for _, status := range []observation.Status{observation.StatusOK, observation.StatusDegraded, observation.StatusUnavailable} {
		m.observationStatus.DeleteLabelValues(id, string(status))
	}
	m.observationFreshness.DeleteLabelValues(id)
	m.observationQueue.DeleteLabelValues(id)
	m.observationRunning.DeleteLabelValues(id)
	m.observationGPU.DeleteLabelValues(id)
	if labels, ok := m.identityLabels[id]; ok {
		m.observationIdentity.DeleteLabelValues(id, labels[0], labels[1])
		delete(m.identityLabels, id)
	}
	delete(m.observationIDs, id)
}

// updateKVCache 将 requestpath 的不可变 KV 状态投影到 Prometheus，并回收已移除 backend。
//
// kvCacheStatuses 保存每个 backend 当前使用的 reason，状态变化时先删旧 reason，再
// 置新 reason 为 1。这样不会在同一个 backend 上同时报告 ready、unknown 和 stale，
// 也不会让已经离开 membership 的 KV label 继续占用 registry。
func (m *Metrics) updateKVCache(states map[backend.ID]requestpath.KVCacheState) {
	m.kvCacheMu.Lock()
	defer m.kvCacheMu.Unlock()
	for backendID := range m.kvCacheIDs {
		if _, exists := states[backend.ID(backendID)]; !exists {
			m.deleteKVCache(backendID)
		}
	}
	for backendID, state := range states {
		id := string(backendID)
		m.kvCacheValid.WithLabelValues(id).Set(boolMetric(state.Valid))
		m.kvCacheFreshness.WithLabelValues(id).Set(state.Freshness.Seconds())
		m.kvCacheLastSequence.WithLabelValues(id).Set(float64(state.LastSequence))
		m.kvCacheAppliedBatches.WithLabelValues(id).Set(float64(state.AppliedBatches))
		m.kvCacheReplayBatches.WithLabelValues(id).Set(float64(state.ReplayBatches))
		status := kvCacheStateReason(state)
		if previous, exists := m.kvCacheStatuses[id]; exists && previous != status {
			m.kvCacheStatus.DeleteLabelValues(id, previous)
		}
		m.kvCacheStatus.WithLabelValues(id, status).Set(1)
		m.kvCacheIDs[id] = struct{}{}
		m.kvCacheStatuses[id] = status
	}
}

// deleteKVCache 删除某个 backend 的 KV 状态、事件来源和 reason label。
//
// live/replay 是事件来源的固定低基数维度；两套 histogram label 都必须清理，否则
// endpoint 重建后旧 Pod 的事件延迟仍可能出现在新 backend 的观测中。
func (m *Metrics) deleteKVCache(backendID string) {
	m.kvCacheValid.DeleteLabelValues(backendID)
	m.kvCacheFreshness.DeleteLabelValues(backendID)
	m.kvCacheLastSequence.DeleteLabelValues(backendID)
	m.kvCacheAppliedBatches.DeleteLabelValues(backendID)
	m.kvCacheReplayBatches.DeleteLabelValues(backendID)
	m.kvEventPublishToApply.DeleteLabelValues(backendID, kvEventSourceLive)
	m.kvEventPublishToApply.DeleteLabelValues(backendID, kvEventSourceReplay)
	if status, exists := m.kvCacheStatuses[backendID]; exists {
		m.kvCacheStatus.DeleteLabelValues(backendID, status)
		delete(m.kvCacheStatuses, backendID)
	}
	delete(m.kvCacheIDs, backendID)
}

// kvCacheStateReason 把 KV 状态转换成一个稳定的 Prometheus reason label。
//
// 有效状态统一显示为 ready；无效但没有具体原因时显示 unknown。这里不把 unknown
// 映射成 cache miss，因为指标必须保留 KV-aware 信号是否可信这一层语义。
func kvCacheStateReason(state requestpath.KVCacheState) string {
	if state.Valid {
		return kvCacheStatusReady
	}
	if state.Reason == "" {
		return kvCacheStatusUnknown
	}
	return string(state.Reason)
}

// boolMetric 将布尔值转换为 Prometheus 约定的 0/1 gauge 值。
func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// updateCircuit 更新单个 backend 的 transport circuit 当前开闭状态。
//
// circuitOpen 是瞬时 gauge，不负责累计打开次数；累计次数由 circuitOpened 单独记录，
// 避免把状态变化和事件计数混在同一个 collector 中。
func (m *Metrics) updateCircuit(backendID string, open bool) {
	value := 0.0
	if open {
		value = 1
	}
	m.circuitOpen.WithLabelValues(backendID).Set(value)
}

// circuitOpened 记录 circuit 从 closed 转为 open 的事件，并同步当前状态 gauge。
func (m *Metrics) circuitOpened(backendID string) {
	m.circuitOpens.WithLabelValues(backendID).Inc()
	m.circuitOpen.WithLabelValues(backendID).Set(1)
}

// DeleteBackend 删除已离开 discovery membership 的有界 backend label。
func (m *Metrics) DeleteBackend(backendID, routingMode string) {
	m.routingDecisions.DeleteLabelValues(routingMode, backendID)
	m.circuitOpen.DeleteLabelValues(backendID)
	m.circuitOpens.DeleteLabelValues(backendID)
	// 删除 observation 时一并删除 Pod/Node identity label tuple，避免只留下孤立身份指标。
	m.observationMu.Lock()
	defer m.observationMu.Unlock()
	m.deleteObservation(backendID)
	m.kvCacheMu.Lock()
	m.deleteKVCache(backendID)
	m.kvCacheMu.Unlock()
}

// updateEndpointDiscovery 将 discovery resolver 的当前状态投影为固定状态 label、freshness
// 和 Ready backend 数量。
//
// 每次更新先把已知状态归零，再把当前状态置为 1，保证状态转换时不会出现两个状态同时
// 为 1。ready backends 是 snapshot 中的数量，不代表 Gateway 已经为每个 backend 成功
// 建立 upstream 连接。
func (m *Metrics) updateEndpointDiscovery(status discovery.ResolverStatus) {
	for _, value := range []discovery.Status{discovery.StatusOK, discovery.StatusDegraded, discovery.StatusUnavailable} {
		m.discoveryStatus.WithLabelValues(string(value)).Set(0)
	}
	current := string(status.Status)
	if current == "" {
		current = string(discovery.StatusUnavailable)
	}
	m.discoveryStatus.WithLabelValues(current).Set(1)
	m.discoveryFreshness.Set(status.Freshness.Seconds())
	m.discoveryReady.Set(float64(status.ReadyBackends))
}

// updateRequestPath 将协议无关的选择状态投影为 Gateway Prometheus 指标。
//
// State 是 requestpath 在同一时刻发布的只读快照；本方法按 observation、discovery、
// KV cache、circuit 的顺序更新，不反向修改任何路由状态。Metrics 只消费快照，因此
// 抓取 /metrics 不会触发重新发现、KV lookup 或策略选择。
func (m *Metrics) updateRequestPath(state requestpath.State) {
	m.updateBackendObservations(state.Observations)
	m.updateEndpointDiscovery(state.Discovery)
	m.updateKVCache(state.KVCache)
	for backendID, open := range state.CircuitOpen {
		m.updateCircuit(string(backendID), open)
	}
}

// observeSelection 记录一次已完成的选择，不把 routing key 写入 label。
//
// 选择指标必须在 upstream 请求开始前记录，因为即使连接随后失败，这次 selection
// 仍然是有效的路由事实。KV-aware 相关指标只在 KV-aware mode 下更新；available 的零 prefix
// 会进入 histogram，unavailable/stale 则只进入 degradation counter。
func (m *Metrics) observeSelection(mode routing.Mode, lease requestpath.Lease) {
	m.updateRequestPath(lease.State)
	decision := lease.Decision
	m.routingDecisions.WithLabelValues(string(mode), string(decision.Backend.ID)).Inc()
	m.routingReasons.WithLabelValues(string(decision.Reason)).Inc()
	if decision.SpilloverReason != "" {
		m.sessionKeySpillovers.WithLabelValues(string(decision.SpilloverReason)).Inc()
	}
	if decision.Policy == routing.PolicyServiceFallbackV1 {
		m.routeFallbacks.Inc()
	}
	if mode == routing.ModeKVAware {
		m.kvAwareRequests.WithLabelValues(string(lease.State.KV)).Inc()
		if lease.State.KV == requestpath.KVAvailable {
			m.kvAwareCachedPrefix.Observe(float64(lease.State.CachedPrefixTokens))
		}
		if lease.State.KV != requestpath.KVAvailable && lease.State.KV != requestpath.KVNotRequested {
			m.kvAwareDegradations.WithLabelValues(string(lease.State.KV)).Inc()
		}
	}
	shadow := lease.State.Prediction
	if shadow.Status != "" {
		outcome := "unavailable"
		if shadow.WouldSelect != "" {
			outcome = "different"
			if shadow.WouldSelect == lease.Decision.Backend.ID {
				outcome = "same"
			}
		}
		m.predictionShadows.WithLabelValues(string(shadow.Status), outcome).Inc()
	}
}

// observePrediction 只在首 SSE 事件存在且选中 backend 有可用影子估计时记录误差。
//
// requestpath 已经完成是否可用的判断，这里只记录绝对误差，不保存 prompt、Token IDs、
// backend URL 或 prediction feature。无首事件、取消或上游失败的请求不会产生伪造样本。
func (m *Metrics) observePrediction(_ requestpath.Lease, observation requestpath.FirstTokenObservation) {
	if !observation.Valid {
		return
	}
	errorSeconds := observation.Error.Seconds()
	if errorSeconds < 0 {
		errorSeconds = -errorSeconds
	}
	m.predictionErrors.Observe(errorSeconds)
}

// ObserveKVEvent 将组合根从 kvcache 接收的成功 apply 延迟投影为有界 Prometheus 标签。
// replayed=false/true 是唯一来源维度；缺失或未来 publisher timestamp 在 kvcache 内已经被拒绝，
// 因此此方法不会把 unknown event 改写成零延迟。
func (m *Metrics) ObserveKVEvent(backendID string, replayed bool, publishToApply time.Duration) {
	source := kvEventSourceLive
	if replayed {
		source = kvEventSourceReplay
	}
	m.kvEventPublishToApply.WithLabelValues(backendID, source).Observe(publishToApply.Seconds())
}

// observeKVEvent 保留内部调用点的统一命名，并转发到导出的事件观测入口。
//
// 事件是否合法、timestamp 是否存在以及是否已经成功 apply 由 kvcache owner 保证；
// Gateway metrics 不在这里重新解释事件，也不把非法事件转换为零延迟。
func (m *Metrics) observeKVEvent(backendID string, replayed bool, publishToApply time.Duration) {
	m.ObserveKVEvent(backendID, replayed, publishToApply)
}

// observeCompletion 只更新选择时确实属于 direct backend 的 circuit label。
//
// backend 可能在请求完成前离开 discovery membership。此时 completion 仍需完成 lease，
// 但不能把已删除 backend 的 circuit label 重新创建；因此先检查 BackendRemoved，再
// 检查选择快照中是否跟踪该 backend。
func (m *Metrics) observeCompletion(lease requestpath.Lease, completion requestpath.Completion) {
	backendID := lease.Decision.Backend.ID
	if completion.BackendRemoved {
		return
	}
	if _, tracked := lease.State.CircuitOpen[backendID]; !tracked {
		return
	}
	if completion.CircuitOpened {
		m.circuitOpened(string(backendID))
		return
	}
	m.updateCircuit(string(backendID), completion.CircuitOpen)
}

// observeRequest 记录一次 HTTP 请求的状态和耗时，method/status 是唯一的 label 维度。
func (m *Metrics) observeRequest(method string, status int, duration time.Duration) {
	statusLabel := strconv.Itoa(status)
	m.requests.WithLabelValues(method, statusLabel).Inc()
	m.requestSeconds.WithLabelValues(method, statusLabel).Observe(duration.Seconds())
}

// handler 返回已经绑定到本地 registry 的 Prometheus HTTP handler。
//
// 使用独立 registry 可以避免污染进程内其他组件的默认 registry；Prometheus handler
// 只负责 exposition，不负责主动刷新 requestpath 或外部观测。
func (m *Metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
