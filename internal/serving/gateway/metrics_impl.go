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
	affinitySpillovers    *prometheus.CounterVec   // bounded affinity spillover trigger
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
	exactRequests         *prometheus.CounterVec
	exactDegradations     *prometheus.CounterVec
	exactCachedPrefix     prometheus.Histogram
	kvCacheMu             sync.Mutex
	kvCacheIDs            map[string]struct{}
	kvCacheStatuses       map[string]string
	observationMu         sync.Mutex
	observationIDs        map[string]struct{}
	identityLabels        map[string][2]string
}

// NewMetrics 创建 standalone Gateway 自己的隔离 registry。
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
	m.affinitySpillovers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricAffinitySpilloversTotal, Help: "Bounded affinity spillovers by pressure signal.",
	}, []string{labelReason})
	m.routeFallbacks = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricRouteFallbacksTotal, Help: "Routing decisions that fell back to the Service backend.",
	})
	m.registry.MustRegister(m.circuitOpen, m.circuitOpens, m.routingDecisions, m.routingReasons, m.affinitySpillovers, m.routeFallbacks)
}

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

func (m *Metrics) initializeKVCacheMetrics() {
	m.kvCacheValid = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricKVCacheInstanceValid, Help: "Whether a backend KV cache state is safe for exact routing.",
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
	m.exactRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricExactRequestsTotal, Help: "Exact routing selections by stable signal status.",
	}, []string{labelExactStatus})
	m.exactDegradations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricExactDegradationsTotal, Help: "Exact routing selections that explicitly degraded to load-aware routing.",
	}, []string{labelExactStatus})
	m.exactCachedPrefix = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace, Subsystem: metricSubsystem, Name: metricExactCachedPrefixTokens,
		Help: "Cached prefix tokens on an exact selection with available KV signal; zero is a real cache miss, not unavailable state.", Buckets: exactCachedPrefixTokenBuckets(),
	})
	m.registry.MustRegister(m.kvCacheValid, m.kvCacheFreshness, m.kvCacheLastSequence, m.kvCacheAppliedBatches, m.kvCacheReplayBatches, m.kvCacheStatus, m.kvEventPublishToApply, m.exactRequests, m.exactDegradations, m.exactCachedPrefix)
}

// updateBackendObservations 只使用 backend ID 等有界标签，不写入请求 routing key。
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

func kvCacheStateReason(state requestpath.KVCacheState) string {
	if state.Valid {
		return kvCacheStatusReady
	}
	if state.Reason == "" {
		return kvCacheStatusUnknown
	}
	return string(state.Reason)
}

func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func (m *Metrics) updateCircuit(backendID string, open bool) {
	value := 0.0
	if open {
		value = 1
	}
	m.circuitOpen.WithLabelValues(backendID).Set(value)
}

func (m *Metrics) circuitOpened(backendID string) {
	m.circuitOpens.WithLabelValues(backendID).Inc()
	m.circuitOpen.WithLabelValues(backendID).Set(1)
}

// DeleteBackend 删除已离开 discovery membership 的有界 backend label。
func (m *Metrics) DeleteBackend(backendID, routingMode string) {
	m.routingDecisions.DeleteLabelValues(routingMode, backendID)
	m.circuitOpen.DeleteLabelValues(backendID)
	m.circuitOpens.DeleteLabelValues(backendID)
	// Observation deletion also handles identity label tuples.
	m.observationMu.Lock()
	defer m.observationMu.Unlock()
	m.deleteObservation(backendID)
	m.kvCacheMu.Lock()
	m.deleteKVCache(backendID)
	m.kvCacheMu.Unlock()
}

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
func (m *Metrics) updateRequestPath(state requestpath.State) {
	m.updateBackendObservations(state.Observations)
	m.updateEndpointDiscovery(state.Discovery)
	m.updateKVCache(state.KVCache)
	for backendID, open := range state.CircuitOpen {
		m.updateCircuit(string(backendID), open)
	}
}

// observeSelection 记录一次已完成的选择，不把 routing key 写入 label。
func (m *Metrics) observeSelection(mode routing.Mode, lease requestpath.Lease) {
	m.updateRequestPath(lease.State)
	decision := lease.Decision
	m.routingDecisions.WithLabelValues(string(mode), string(decision.Backend.ID)).Inc()
	m.routingReasons.WithLabelValues(string(decision.Reason)).Inc()
	if decision.SpilloverReason != "" {
		m.affinitySpillovers.WithLabelValues(string(decision.SpilloverReason)).Inc()
	}
	if decision.Policy == routing.PolicyServiceFallbackV1 {
		m.routeFallbacks.Inc()
	}
	if mode == routing.ModeExactCacheLoad {
		m.exactRequests.WithLabelValues(string(lease.State.Exact)).Inc()
		if lease.State.Exact == requestpath.ExactAvailable {
			m.exactCachedPrefix.Observe(float64(lease.State.CachedPrefixTokens))
		}
		if lease.State.Exact != requestpath.ExactAvailable && lease.State.Exact != requestpath.ExactNotRequested {
			m.exactDegradations.WithLabelValues(string(lease.State.Exact)).Inc()
		}
	}
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

func (m *Metrics) observeKVEvent(backendID string, replayed bool, publishToApply time.Duration) {
	m.ObserveKVEvent(backendID, replayed, publishToApply)
}

// observeCompletion 只更新选择时确实属于 direct backend 的 circuit label。
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

func (m *Metrics) observeRequest(method string, status int, duration time.Duration) {
	statusLabel := strconv.Itoa(status)
	m.requests.WithLabelValues(method, statusLabel).Inc()
	m.requestSeconds.WithLabelValues(method, statusLabel).Observe(duration.Seconds())
}

func (m *Metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
