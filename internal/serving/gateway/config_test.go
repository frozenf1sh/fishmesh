package gateway

import (
	"strings"
	"testing"
	"time"
)

func TestEndpointSliceConfigDoesNotRequireStaticEndpoints(t *testing.T) {
	config := Config{
		ListenAddress: ":8080", UpstreamURL: "http://service.default.svc:8000",
		RoutingMode: "prefix-affinity", EndpointDiscovery: "endpointslice",
		EndpointService: "qwen-vllm", EndpointNamespace: "kubellm",
		EndpointRefresh: time.Second, EndpointMaxAge: time.Minute, RequestTimeout: time.Second, ShutdownTimeout: time.Second,
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("EndpointSlice configuration should not require static URLs: %v", err)
	}
}

func TestLoadConfigRejectsMalformedDuration(t *testing.T) {
	t.Setenv("FISHMESH_REQUEST_TIMEOUT", "ninety-seconds")
	_, err := LoadConfigFromEnvironment()
	if err == nil || !strings.Contains(err.Error(), "FISHMESH_REQUEST_TIMEOUT") {
		t.Fatalf("expected named duration error, got %v", err)
	}
}

func TestLoadConfigRejectsMalformedBoolean(t *testing.T) {
	t.Setenv("FISHMESH_UPSTREAM_KEEPALIVE", "sometimes")
	_, err := LoadConfigFromEnvironment()
	if err == nil || !strings.Contains(err.Error(), "FISHMESH_UPSTREAM_KEEPALIVE") {
		t.Fatalf("expected named boolean error, got %v", err)
	}
}

func TestLoadConfigReadsBoundedAffinitySettings(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "bounded-affinity")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	t.Setenv("FISHMESH_AFFINITY_TTL", "2m")
	t.Setenv("FISHMESH_AFFINITY_MAX_ENTRIES", "2048")
	t.Setenv("FISHMESH_AFFINITY_INFLIGHT_DELTA", "3")
	t.Setenv("FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA", "2.5")
	t.Setenv("FISHMESH_MAX_INFLIGHT_REQUESTS", "64")
	t.Setenv("FISHMESH_MAX_CONNS_PER_HOST", "12")
	t.Setenv("FISHMESH_CIRCUIT_EWMA_ALPHA", "0.4")
	t.Setenv("FISHMESH_CIRCUIT_ERROR_THRESHOLD", "0.7")
	t.Setenv("FISHMESH_CIRCUIT_MIN_REQUESTS", "5")
	t.Setenv("FISHMESH_CIRCUIT_OPEN_DURATION", "15s")
	config, err := LoadConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.AffinityTTL != 2*time.Minute || config.AffinityMaxEntries != 2048 || config.AffinityInflightDelta != 3 || config.AffinityQueueDepthDelta != 2.5 || config.MaxInflightRequests != 64 || config.MaxConnsPerHost != 12 || config.CircuitEWMAAlpha != 0.4 || config.CircuitErrorThreshold != 0.7 || config.CircuitMinimumRequests != 5 || config.CircuitOpenDuration != 15*time.Second {
		t.Fatalf("unexpected bounded affinity config: %+v", config)
	}
}

func TestLoadConfigRejectsUnboundedReliabilityLimits(t *testing.T) {
	t.Setenv("FISHMESH_MAX_INFLIGHT_REQUESTS", "0")
	_, err := LoadConfigFromEnvironment()
	if err == nil || !strings.Contains(err.Error(), "FISHMESH_MAX_INFLIGHT_REQUESTS") {
		t.Fatalf("expected positive admission limit error, got %v", err)
	}
}

func TestLoadConfigRejectsMalformedAffinityThreshold(t *testing.T) {
	t.Setenv("FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA", "low")
	_, err := LoadConfigFromEnvironment()
	if err == nil || !strings.Contains(err.Error(), "FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA") {
		t.Fatalf("expected named threshold error, got %v", err)
	}
}

func TestEndpointSliceConfigRequiresServiceIdentity(t *testing.T) {
	config := Config{
		ListenAddress: ":8080", UpstreamURL: "http://service.default.svc:8000",
		RoutingMode: "service", EndpointDiscovery: "endpointslice",
		EndpointRefresh: time.Second, RequestTimeout: time.Second, ShutdownTimeout: time.Second,
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing EndpointSlice service identity to fail validation")
	}
}
