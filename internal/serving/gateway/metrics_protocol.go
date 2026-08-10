package gateway

const (
	metricNamespace = "fishmesh"
	metricSubsystem = "gateway"

	metricInflightRequests               = "inflight_requests"
	metricRequestsTotal                  = "requests_total"
	metricRequestDurationSeconds         = "request_duration_seconds"
	metricFirstSSEEventSeconds           = "first_sse_event_seconds"
	metricUpstreamErrorsTotal            = "upstream_errors_total"
	metricUpstreamStreamErrorsTotal      = "upstream_stream_errors_total"
	metricAdmissionRejectionsTotal       = "admission_rejections_total"
	metricBackendCircuitOpen             = "backend_circuit_open"
	metricBackendCircuitOpensTotal       = "backend_circuit_opens_total"
	metricRoutingDecisionsTotal          = "routing_decisions_total"
	metricRoutingReasonsTotal            = "routing_reasons_total"
	metricAffinitySpilloversTotal        = "affinity_spillovers_total"
	metricRouteFallbacksTotal            = "route_fallbacks_total"
	metricBackendObservationStatus       = "backend_observation_status"
	metricObservationFreshnessSeconds    = "backend_observation_freshness_seconds"
	metricObservationQueueLength         = "backend_observation_queue_length"
	metricObservationRunningRequests     = "backend_observation_running_requests"
	metricBackendIdentityInfo            = "backend_identity_info"
	metricBackendGPURequested            = "backend_gpu_requested"
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
	metricExactRequestsTotal             = "exact_requests_total"
	metricExactDegradationsTotal         = "exact_degradations_total"
	metricExactCachedPrefixTokens        = "exact_cached_prefix_tokens"
	metricPredictionShadowsTotal         = "prediction_shadows_total"
	metricPredictionAbsoluteErrorSeconds = "prediction_absolute_error_seconds"

	labelMethod            = "method"
	labelStatus            = "status"
	labelBackendID         = "backend_id"
	labelMode              = "mode"
	labelReason            = "reason"
	labelPodName           = "pod_name"
	labelNodeName          = "node_name"
	labelExactStatus       = "status"
	labelKVCacheStatus     = "reason"
	labelKVEventSource     = "source"
	labelPredictionOutcome = "outcome"

	kvCacheStatusReady   = "ready"
	kvCacheStatusUnknown = "unknown"
	kvEventSourceLive    = "live"
	kvEventSourceReplay  = "replay"
)

func firstSSEEventBuckets() []float64 {
	return []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60}
}

func kvEventPublishToApplyBuckets() []float64 {
	return []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10}
}

func exactCachedPrefixTokenBuckets() []float64 {
	return []float64{0, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384}
}
