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
