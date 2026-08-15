package gateway

const (
	metricNamespace = "fishmesh"
	metricSubsystem = "gateway"

	metricInflightRequests               = "inflight_requests"
	metricAdmittedRequestsTotal          = "admitted_requests_total"
	metricRequestsTotal                  = "requests_total"
	metricRequestDurationSeconds         = "request_duration_seconds"
	metricFirstSSEEventSeconds           = "first_sse_event_seconds"
	metricUpstreamErrorsTotal            = "upstream_errors_total"
	metricUpstreamStreamErrorsTotal      = "upstream_stream_errors_total"
	metricAdmissionRejectionsTotal       = "admission_rejections_total"
	metricAdmissionSoftRejectionsTotal   = "admission_soft_rejections_total"
	metricAdmissionHardRejectionsTotal   = "admission_hard_rejections_total"
	metricAdmissionTarget                = "admission_target"
	metricAdmissionHardLimit             = "admission_hard_limit"
	metricAdmissionSuggestedTarget       = "admission_suggested_target"
	metricAdmissionTuningActionsTotal    = "admission_tuning_actions_total"
	metricAdmissionTuningSignalValid     = "admission_tuning_signal_valid"
	metricAdmissionTuningLastChange      = "admission_tuning_last_change_timestamp_seconds"
	metricAdmissionTuningMode            = "admission_tuning_mode"
	metricAdmissionTuningReason          = "admission_tuning_reason"
	metricBackendCircuitOpen             = "backend_circuit_open"
	metricBackendCircuitOpensTotal       = "backend_circuit_opens_total"
	metricRoutingDecisionsTotal          = "routing_decisions_total"
	metricRoutingReasonsTotal            = "routing_reasons_total"
	metricSessionKeySpilloversTotal      = "session_key_spillovers_total"
	metricRouteFallbacksTotal            = "route_fallbacks_total"
	metricBackendObservationStatus       = "backend_observation_status"
	metricObservationFreshnessSeconds    = "backend_observation_freshness_seconds"
	metricObservationQueueLength         = "backend_observation_queue_length"
	metricObservationRunningRequests     = "backend_observation_running_requests"
	metricBackendIdentityInfo            = "backend_identity_info"
	metricBackendGPURequested            = "backend_gpu_requested"
	metricBackendCPUUsageCores           = "backend_cpu_usage_cores"
	metricBackendMemoryUsageBytes        = "backend_memory_usage_bytes"
	metricBackendGPUUtilizationPercent   = "backend_gpu_utilization_percent"
	metricBackendGPUMemoryUsedBytes      = "backend_gpu_memory_used_bytes"
	metricBackendGPUTemperatureCelsius   = "backend_gpu_temperature_celsius"
	metricEndpointDiscoveryStatus        = "endpoint_discovery_status"
	metricDiscoveryFreshnessSeconds      = "endpoint_discovery_freshness_seconds"
	metricEndpointDiscoveryReady         = "endpoint_discovery_ready_backends"
	metricKVCacheInstanceValid           = "kv_cache_instance_valid"
	metricKVCacheFreshnessSeconds        = "kv_cache_freshness_seconds"
	metricKVCacheLastSequence            = "kv_cache_last_sequence"
	metricKVCacheAppliedBatches          = "kv_cache_applied_batches"
	metricKVCacheReplayBatches           = "kv_cache_replay_batches"
	metricKVCacheStatus                  = "kv_cache_status"
	metricKVEventPublishToApplySeconds   = "kv_event_publish_to_apply_seconds"
	metricKVAwareRequestsTotal           = "kv_aware_requests_total"
	metricKVAwareDegradationsTotal       = "kv_aware_degradations_total"
	metricKVAwareBypassesTotal           = "kv_aware_bypasses_total"
	metricKVAwareCachedPrefixTokens      = "kv_aware_cached_prefix_tokens"
	metricPredictionShadowsTotal         = "prediction_shadows_total"
	metricPredictionAbsoluteErrorSeconds = "prediction_absolute_error_seconds"
	metricStaticEstimatorSelectionsTotal = "static_estimator_selections_total"
	metricStaticEstimatedTTFTSeconds     = "static_estimated_ttft_seconds"
	metricStaticEstimatorErrorSeconds    = "static_estimator_absolute_error_seconds"
	metricHardOverloadSelectionsTotal    = "hard_overload_selections_total"

	labelMethod            = "method"
	labelStatus            = "status"
	labelBackendID         = "backend_id"
	labelMode              = "mode"
	labelReason            = "reason"
	labelPodName           = "pod_name"
	labelNodeName          = "node_name"
	labelKVStatus          = "status"
	labelKVCacheStatus     = "reason"
	labelKVEventSource     = "source"
	labelPredictionOutcome = "outcome"
	labelConfidence        = "confidence"
	labelEstimatorReason   = "estimator_reason"
	labelOutcome           = "outcome"
	labelTuningMode        = "mode"
	labelTuningReason      = "reason"

	kvCacheStatusReady   = "ready"
	kvCacheStatusUnknown = "unknown"
	kvEventSourceLive    = "live"
	kvEventSourceReplay  = "replay"
)

func firstSSEEventBuckets() []float64 {
	// bucket 覆盖 10ms 到 60s，并在常见的首 token 区间保留较细粒度。
	// 返回新 slice 而不是共享可变全局 slice，避免调用方意外修改后影响其他 registry。
	return []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60}
}

func kvEventPublishToApplyBuckets() []float64 {
	// 事件延迟 bucket 从 0.5ms 开始，覆盖实时 live event 和 replay 可能出现的秒级延迟。
	// 该指标表达 publisher timestamp 到本地成功 apply 的时间，不等同于网络 RTT。
	return []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10}
}

func kvAwareCachedPrefixTokenBuckets() []float64 {
	// KV-aware prefix 以 token 数为单位；0 是有效的真实 miss，不能用缺失样本代替。
	// 末端 bucket 为长 system prompt 预留空间，Prometheus 会自动提供 +Inf bucket。
	return []float64{0, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384}
}
