package config

import (
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

const (
	envListenAddress             = "FISHMESH_LISTEN_ADDRESS"
	envUpstreamURL               = "FISHMESH_UPSTREAM_URL"
	envRoutingMode               = "FISHMESH_ROUTING_MODE"
	envExactModel                = "FISHMESH_EXACT_MODEL"
	envExactRenderURL            = "FISHMESH_EXACT_RENDER_URL"
	envExactQueueTokenPenalty    = "FISHMESH_EXACT_QUEUE_TOKEN_PENALTY"
	envExactRunningTokenPenalty  = "FISHMESH_EXACT_RUNNING_TOKEN_PENALTY"
	envExactInflightTokenPenalty = "FISHMESH_EXACT_INFLIGHT_TOKEN_PENALTY"
	envPredictionMode            = "FISHMESH_PREDICTION_MODE"
	envBackendEndpoints          = "FISHMESH_BACKEND_ENDPOINTS"
	envLegacyPrefixEndpoints     = "FISHMESH_PREFIX_ENDPOINTS"
	envEndpointDiscovery         = "FISHMESH_ENDPOINT_DISCOVERY"
	envEndpointService           = "FISHMESH_ENDPOINT_SERVICE"
	envEndpointNamespace         = "FISHMESH_ENDPOINT_NAMESPACE"
	envKubernetesAPIURL          = "FISHMESH_KUBERNETES_API_URL"
	envKubernetesTokenFile       = "FISHMESH_KUBERNETES_TOKEN_FILE"
	envKubernetesCAFile          = "FISHMESH_KUBERNETES_CA_FILE"
	envEndpointRefreshInterval   = "FISHMESH_ENDPOINT_REFRESH_INTERVAL"
	envEndpointMaxAge            = "FISHMESH_ENDPOINT_MAX_AGE"
	envObservationMode           = "FISHMESH_BACKEND_OBSERVATION_MODE"
	envObservationInterval       = "FISHMESH_BACKEND_OBSERVATION_INTERVAL"
	envObservationMaxAge         = "FISHMESH_BACKEND_OBSERVATION_MAX_AGE"
	envUpstreamKeepAlive         = "FISHMESH_UPSTREAM_KEEPALIVE"
	envRequestTimeout            = "FISHMESH_REQUEST_TIMEOUT"
	envMaxRequestBodyBytes       = "FISHMESH_MAX_REQUEST_BODY_BYTES"
	envShutdownTimeout           = "FISHMESH_SHUTDOWN_TIMEOUT"
	envAffinityTTL               = "FISHMESH_AFFINITY_TTL"
	envAffinityMaxEntries        = "FISHMESH_AFFINITY_MAX_ENTRIES"
	envAffinityInflightDelta     = "FISHMESH_AFFINITY_INFLIGHT_DELTA"
	envAffinityQueueDepthDelta   = "FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA"
	envMaxInflightRequests       = "FISHMESH_MAX_INFLIGHT_REQUESTS"
	envMaxConnsPerHost           = "FISHMESH_MAX_CONNS_PER_HOST"
	envCircuitEWMAAlpha          = "FISHMESH_CIRCUIT_EWMA_ALPHA"
	envCircuitErrorThreshold     = "FISHMESH_CIRCUIT_ERROR_THRESHOLD"
	envCircuitMinimumRequests    = "FISHMESH_CIRCUIT_MIN_REQUESTS"
	envCircuitOpenDuration       = "FISHMESH_CIRCUIT_OPEN_DURATION"

	serviceBackendID                     = "service"
	defaultListenAddress                 = ":8080"
	defaultUpstreamURL                   = "http://qwen-vllm-baseline.kubellm.svc.cluster.local:8000"
	defaultEndpointService               = "qwen-vllm"
	defaultEndpointNamespace             = "kubellm"
	defaultKubernetesTokenFile           = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultKubernetesCAFile              = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultEndpointRefresh               = 30 * time.Second
	defaultEndpointMaxAge                = 90 * time.Second
	defaultObservationInterval           = 15 * time.Second
	defaultObservationMaxAge             = 45 * time.Second
	defaultRequestTimeout                = 90 * time.Second
	defaultMaxRequestBodyBytes     int64 = 2 << 20
	defaultShutdownTimeout               = 30 * time.Second
	defaultReadHeaderTimeout             = 5 * time.Second
	defaultAffinityTTL                   = 5 * time.Minute
	defaultAffinityMaxEntries            = 10_000
	defaultAffinityInflightDelta   int64 = 2
	defaultAffinityQueueDepthDelta       = 1.0
	defaultMaxInflightRequests           = 128
	defaultMaxConnsPerHost               = 32
	defaultCircuitEWMAAlpha              = 0.5
	defaultCircuitErrorThreshold         = 0.6
	defaultCircuitMinimumRequests        = 3
	defaultCircuitOpenDuration           = 10 * time.Second
	defaultRoutingMode                   = routing.ModeService
	defaultExactModel                    = "qwen2.5-0.5b-instruct"
	defaultExactRenderURL                = "http://qwen-vllm-baseline.kubellm.svc.cluster.local:8000"
)
