package gateway

import (
	"sync"

	"github.com/frozenf1sh/fishmesh/internal/serving/endpoint"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/prometheus/client_golang/prometheus"
)

// metrics 集中管理 Gateway 暴露的全部 Prometheus 指标。
//
// 指标命名遵循 Prometheus 惯例：命名空间 fishmesh + 子系统 gateway +
// 指标名，语义后缀 _total（counter）/ _seconds（时间直方图）。
//
// 第一轮实验的设计决定：指标直接以 Prometheus exposition 格式暴露在
// /metrics，不依赖独立的 Prometheus 服务。抓取方（或人工 curl）拿到
// 数据后自行聚合，减少实验环境的运行面。
type metrics struct {
	inflight             prometheus.Gauge         // 当前正在代理中的请求数（反映瞬时并发）
	requests             *prometheus.CounterVec   // 完成的请求总数，按 method/status 分桶
	requestSeconds       *prometheus.HistogramVec // 端到端请求耗时，按 method/status 分桶
	ttftSeconds          prometheus.Histogram     // 首 token 延迟（TTFT），项目的核心测量指标
	upstreamErrors       prometheus.Counter       // 上游传输层失败次数（区别于业务层 4xx/5xx）
	routingDecisions     *prometheus.CounterVec   // 路由模式和有限 backend ID 维度
	routeFallbacks       prometheus.Counter       // 路由策略失败后的 Service fallback
	observationStatus    *prometheus.GaugeVec
	observationFreshness *prometheus.GaugeVec
	observationQueue     *prometheus.GaugeVec
	observationRunning   *prometheus.GaugeVec
	observationIdentity  *prometheus.GaugeVec
	observationGPU       *prometheus.GaugeVec
	discoveryStatus      *prometheus.GaugeVec
	discoveryFreshness   prometheus.Gauge
	discoveryReady       prometheus.Gauge
	observationMu        sync.Mutex
	observationIDs       map[string]struct{}
	identityLabels       map[string][2]string
}

func newMetrics(registry *prometheus.Registry) *metrics {
	m := &metrics{
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "inflight_requests", Help: "Requests currently proxied by the gateway.",
		}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "requests_total", Help: "Completed gateway requests.",
		}, []string{"method", "status"}),
		requestSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "request_duration_seconds", Help: "End-to-end gateway request duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "status"}),
		// TTFT 直方图的 bucket 是特意选的：10ms 起步、覆盖到 60s 的超时上限。
		// 在 0.1s~1s 区间（本项目实测 TTFT 所在量级）有细粒度采样，
		// 后期实现 prefix-aware routing 后，warm-prefix 与 cold-prefix 的
		// 分布差异能在这个区间被分辨出来。
		ttftSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "first_sse_event_seconds", Help: "Time from request receipt until the first non-terminal SSE event.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
		upstreamErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "upstream_errors_total", Help: "Upstream transport failures.",
		}),
		routingDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "routing_decisions_total", Help: "Routing decisions by mode and bounded backend identity.",
		}, []string{"mode", "backend_id"}),
		routeFallbacks: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "route_fallbacks_total", Help: "Routing decisions that fell back to the Service backend.",
		}),
		observationStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "backend_observation_status", Help: "Latest backend observation status (one active status has value 1).",
		}, []string{"backend_id", "status"}),
		observationFreshness: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "backend_observation_freshness_seconds", Help: "Age of the latest backend observation.",
		}, []string{"backend_id"}),
		observationQueue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "backend_observation_queue_length", Help: "Latest vLLM queue length by backend.",
		}, []string{"backend_id"}),
		observationRunning: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "backend_observation_running_requests", Help: "Latest vLLM running request count by backend.",
		}, []string{"backend_id"}),
		observationIdentity: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "backend_identity_info", Help: "Kubernetes identity mapped to a backend address.",
		}, []string{"backend_id", "pod_name", "node_name"}),
		observationGPU: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "backend_gpu_requested", Help: "Declared GPU resources requested by the backend Pod.",
		}, []string{"backend_id"}),
		discoveryStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "endpoint_discovery_status", Help: "Endpoint discovery status (one active status has value 1).",
		}, []string{"status"}),
		discoveryFreshness: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "endpoint_discovery_freshness_seconds", Help: "Age of the last successful endpoint discovery snapshot.",
		}),
		discoveryReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "endpoint_discovery_ready_backends", Help: "Number of Ready backends in the latest endpoint discovery snapshot.",
		}),
		observationIDs: make(map[string]struct{}),
		identityLabels: make(map[string][2]string),
	}
	// MustRegister：注册冲突会 panic。对运行时指标注册来说这是正确的行为——
	// 指标定义错误属于编程错误，应当立即暴露而不是静默吞掉。
	registry.MustRegister(m.inflight, m.requests, m.requestSeconds, m.ttftSeconds, m.upstreamErrors, m.routingDecisions, m.routeFallbacks, m.observationStatus, m.observationFreshness, m.observationQueue, m.observationRunning, m.observationIdentity, m.observationGPU, m.discoveryStatus, m.discoveryFreshness, m.discoveryReady)
	return m
}

// UpdateBackendObservations projects the structured snapshot into bounded
// Prometheus gauges. Backend IDs come from EndpointSlice hashes, not request
// data, so the label cardinality remains tied to replica count.
func (m *metrics) UpdateBackendObservations(states map[string]routing.BackendObservation) {
	m.observationMu.Lock()
	defer m.observationMu.Unlock()
	for id := range m.observationIDs {
		if _, ok := states[id]; ok {
			continue
		}
		m.deleteObservation(id)
	}
	for id, state := range states {
		m.observationStatus.WithLabelValues(id, string(state.Status)).Set(1)
		for _, status := range []routing.ObservationStatus{routing.ObservationOK, routing.ObservationDegraded, routing.ObservationUnavailable} {
			if status != state.Status {
				m.observationStatus.WithLabelValues(id, string(status)).Set(0)
			}
		}
		m.observationFreshness.WithLabelValues(id).Set(state.Freshness.Seconds())
		m.observationQueue.WithLabelValues(id).Set(state.QueueLength)
		m.observationRunning.WithLabelValues(id).Set(state.RunningRequests)
		labels := [2]string{state.Identity.PodName, state.Identity.NodeName}
		if previous, ok := m.identityLabels[id]; ok && previous != labels {
			m.observationIdentity.DeleteLabelValues(id, previous[0], previous[1])
		}
		m.observationIdentity.WithLabelValues(id, labels[0], labels[1]).Set(1)
		m.observationGPU.WithLabelValues(id).Set(state.Identity.GPURequested)
		m.identityLabels[id] = labels
		m.observationIDs[id] = struct{}{}
	}
}

func (m *metrics) deleteObservation(id string) {
	for _, status := range []routing.ObservationStatus{routing.ObservationOK, routing.ObservationDegraded, routing.ObservationUnavailable} {
		m.observationStatus.DeleteLabelValues(id, string(status))
	}
	m.observationFreshness.DeleteLabelValues(id)
	m.observationQueue.DeleteLabelValues(id)
	m.observationRunning.DeleteLabelValues(id)
	if labels, ok := m.identityLabels[id]; ok {
		m.observationIdentity.DeleteLabelValues(id, labels[0], labels[1])
		delete(m.identityLabels, id)
	}
	delete(m.observationIDs, id)
}

func (m *metrics) UpdateEndpointDiscovery(status endpoint.ResolverStatus) {
	for _, value := range []string{"ok", "degraded", "unavailable"} {
		m.discoveryStatus.WithLabelValues(value).Set(0)
	}
	current := string(status.Status)
	if current == "" {
		current = "unavailable"
	}
	m.discoveryStatus.WithLabelValues(current).Set(1)
	m.discoveryFreshness.Set(status.Freshness.Seconds())
	m.discoveryReady.Set(float64(status.ReadyBackends))
}
