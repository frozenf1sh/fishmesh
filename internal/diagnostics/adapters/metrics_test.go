package adapters

import (
	"strings"
	"testing"
)

func TestParseGatewayMetrics(t *testing.T) {
	values, err := parseGatewayMetrics(strings.NewReader(`# HELP fishmesh_gateway_inflight_requests Requests
# TYPE fishmesh_gateway_inflight_requests gauge
fishmesh_gateway_inflight_requests 2
# TYPE fishmesh_gateway_route_fallbacks_total counter
fishmesh_gateway_route_fallbacks_total 3
# TYPE fishmesh_gateway_requests_total counter
fishmesh_gateway_requests_total{method="POST",status="200"} 4
fishmesh_gateway_requests_total{method="POST",status="200"} 5
`))
	if err != nil {
		t.Fatalf("parseGatewayMetrics() error = %v", err)
	}
	if values["inflight_requests"] != 2 || values["route_fallbacks_total"] != 3 || values["requests_total"] != 9 {
		t.Fatalf("unexpected values: %#v", values)
	}
}
