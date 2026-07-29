package domain

import (
	"fmt"
	"strings"
	"time"
)

// DiagnosisPolicy 将多源 Signal 映射为可解释结论。它不执行任何变更，
// 也不依赖 LLM；未来可用 LLMNarrator 将此结构翻译成自然语言，而不改变事实层。
type DiagnosisPolicy interface {
	Name() string
	Evaluate(Incident, []Signal, time.Time) Diagnosis
}

// RulePolicy 是 MVP 的确定性诊断策略，优先输出网络、GPU、队列和局部性
// 四类高价值故障。阈值集中在这里，便于实验中版本化和单元测试。
type RulePolicy struct {
	NetworkRTTMillis      float64
	NetworkRetransmission float64
	GPUSaturationPercent  float64
	QueuePressure         float64
	PrefixCacheHitMinimum float64
}

func DefaultRulePolicy() RulePolicy {
	return RulePolicy{NetworkRTTMillis: 100, NetworkRetransmission: 0.01, GPUSaturationPercent: 90, QueuePressure: 8, PrefixCacheHitMinimum: 0.5}
}

func (p RulePolicy) Name() string { return "rules-v1" }

func (p RulePolicy) Evaluate(incident Incident, signals []Signal, now time.Time) Diagnosis {
	values := flattenSignalValues(signals)
	if values["tcp_rtt_ms"] >= p.NetworkRTTMillis || values["retransmission_rate"] >= p.NetworkRetransmission {
		return diagnosis("network_degraded", "网络信号异常，TTFT 变慢更可能由传输层退化导致", 0.92,
			[]Evidence{{Signal: "query_ebpf_network", Observation: fmt.Sprintf("RTT=%.1fms, retransmission=%.4f", values["tcp_rtt_ms"], values["retransmission_rate"]), Impact: "网络重传或高 RTT 会放大流式响应首包延迟"}},
			Recommendation{Code: "inspect_network_path", Summary: "检查节点间 RTT、重传和连接生命周期，不要先调整 Prefix Affinity", Risk: "network symptoms may be transient", ExpiresAt: now.Add(10 * time.Minute)}, now)
	}
	if values["gpu_utilization_percent"] >= p.GPUSaturationPercent || values["gpu_memory_percent"] >= p.GPUSaturationPercent {
		return diagnosis("gpu_saturated", "GPU 资源接近饱和，局部性优化可能进一步放大热点", 0.9,
			[]Evidence{{Signal: "query_gpu_status", Observation: fmt.Sprintf("utilization=%.1f%%, memory=%.1f%%", values["gpu_utilization_percent"], values["gpu_memory_percent"]), Impact: "GPU headroom 不足时应优先控制排队和并发"}},
			Recommendation{Code: "reduce_hotspot_pressure", Summary: "降低热点后端并发或扩充可用 backend，再评估路由策略", Risk: "changing affinity during saturation may increase tail latency", ExpiresAt: now.Add(10 * time.Minute)}, now)
	}
	if values["queue_length"] >= p.QueuePressure {
		return diagnosis("serving_queue_pressure", "推理队列压力升高，当前主要矛盾是服务端排队", 0.88,
			[]Evidence{{Signal: "query_llm_metrics", Observation: fmt.Sprintf("queue_length=%.0f, running_requests=%.0f", values["queue_length"], values["running_requests"]), Impact: "排队时间会直接抬高 TTFT，单纯改变 prefix 路由未必有效"}},
			Recommendation{Code: "rebalance_load", Summary: "优先使用负载感知路由并检查并发上限", Risk: "aggressive rebalancing can reduce prefix locality", ExpiresAt: now.Add(10 * time.Minute)}, now)
	}
	if hit, present := values["prefix_cache_hit_rate"]; present && hit < p.PrefixCacheHitMinimum && signalUsable(signals, "query_ebpf_network") && signalUsable(signals, "query_gpu_status") {
		return diagnosis("prefix_locality_degraded", "Prefix Cache 命中率偏低，但当前没有明显网络或 GPU 饱和信号", 0.82,
			[]Evidence{{Signal: "query_llm_metrics", Observation: fmt.Sprintf("prefix_cache_hit_rate=%.2f", hit), Impact: "共享前缀未稳定落到同一 backend，prefill 工作可能增加"}, {Signal: "query_ebpf_network", Observation: "RTT 与重传在正常范围", Impact: "网络退化不是当前首要解释"}},
			Recommendation{Code: "enable_bounded_prefix_affinity", Summary: "在有界并发和 TTL 约束下启用 Prefix Affinity，并持续观察热点后端", Risk: "hot prefix can overload a single backend", ExpiresAt: now.Add(10 * time.Minute)}, now)
	}
	if missing := unavailableSignals(signals); len(missing) > 0 {
		return diagnosis("insufficient_observability", "当前观测链路不完整，无法对集群异常做出可靠归因", 0.95,
			[]Evidence{{Signal: "observation_pipeline", Observation: fmt.Sprintf("unavailable=%s", strings.Join(missing, ",")), Impact: "缺失信号不能被当作健康信号，暂不自动修改路由策略"}},
			Recommendation{Code: "complete_observability", Summary: "先接入缺失的 vLLM、GPU、Kubernetes 或网络采集器，再重新分析", Risk: "partial telemetry can produce false attribution", ExpiresAt: now.Add(5 * time.Minute)}, now)
	}
	return diagnosis("no_actionable_anomaly", "当前采集信号未发现达到阈值的明确异常", 0.6,
		[]Evidence{{Signal: "incident", Observation: strings.TrimSpace(incident.Description), Impact: "保留观测，等待更多时间窗口数据"}},
		Recommendation{Code: "continue_observing", Summary: "继续采集当前窗口，暂不自动修改路由或集群资源", Risk: "short windows can hide intermittent failures", ExpiresAt: now.Add(5 * time.Minute)}, now)
}

func signalUsable(signals []Signal, name string) bool {
	for _, signal := range signals {
		if signal.Name == name {
			return signal.Status == SignalOK || signal.Status == SignalDegraded
		}
	}
	return false
}

func unavailableSignals(signals []Signal) []string {
	missing := make([]string, 0)
	for _, signal := range signals {
		if signal.Status == SignalUnavailable {
			missing = append(missing, signal.Name)
		}
	}
	return missing
}

func diagnosis(code, summary string, confidence float64, evidence []Evidence, recommendation Recommendation, now time.Time) Diagnosis {
	if recommendation.ExpiresAt.IsZero() {
		recommendation.ExpiresAt = now.Add(10 * time.Minute)
	}
	return Diagnosis{Code: code, Summary: summary, Confidence: confidence, Evidence: evidence, Recommendation: recommendation}
}

func flattenSignalValues(signals []Signal) map[string]float64 {
	values := make(map[string]float64)
	for _, signal := range signals {
		for key, value := range signal.Values {
			values[key] = value
		}
	}
	return values
}
