package gateway

import (
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
