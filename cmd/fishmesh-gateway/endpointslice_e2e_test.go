package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
	"github.com/frozenf1sh/fishmesh/internal/simulator"
)

const (
	endpointSliceRefresh = 20 * time.Millisecond
	endpointSliceMaxAge  = 80 * time.Millisecond
)

type endpointSliceAPI struct {
	mu          sync.RWMutex
	ports       []int
	version     int
	unavailable bool
}

type dynamicGatewayFixture struct {
	api          *endpointSliceAPI
	gateway      *httptest.Server
	first        simulatorEndpoint
	second       simulatorEndpoint
	fallback     *simulator.Backend
	fallbackPort int
}

type simulatorEndpoint struct {
	backend *simulator.Backend
	port    int
}

type endpointSliceListResponse struct {
	Metadata endpointSliceMetadata   `json:"metadata"`
	Items    []endpointSliceResponse `json:"items"`
}

type endpointSliceResponse struct {
	Metadata    endpointSliceMetadata   `json:"metadata"`
	AddressType string                  `json:"addressType"`
	Ports       []endpointSlicePort     `json:"ports"`
	Endpoints   []endpointSliceEndpoint `json:"endpoints"`
}

type endpointSliceMetadata struct {
	Name            string `json:"name,omitempty"`
	ResourceVersion string `json:"resourceVersion"`
}

type endpointSlicePort struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

type endpointSliceEndpoint struct {
	Addresses  []string                     `json:"addresses"`
	Conditions endpointSliceConditions      `json:"conditions"`
	TargetRef  endpointSliceTargetReference `json:"targetRef"`
}

type endpointSliceConditions struct {
	Ready bool `json:"ready"`
}

type endpointSliceTargetReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func TestEndpointSliceE2ERemovesBackendsAndFallsBackWhenStale(t *testing.T) {
	fixture := newDynamicGatewayFixture(t)
	fixture.assertRemovedBackendStopsReceiving(t)
	fixture.assertStaleDiscoveryFallsBack(t)
}

func newDynamicGatewayFixture(t testing.TB) *dynamicGatewayFixture {
	t.Helper()
	firstBackend, firstServer := startSimulatorServer(t)
	secondBackend, secondServer := startSimulatorServer(t)
	fallbackBackend, fallbackServer := startSimulatorServer(t)
	firstPort := serverPort(t, firstServer.URL)
	secondPort := serverPort(t, secondServer.URL)
	api := &endpointSliceAPI{ports: []int{firstPort, secondPort}, version: 1}
	apiServer := httptest.NewTLSServer(api)
	t.Cleanup(apiServer.Close)

	config := endpointSliceGatewayConfig(t, fallbackServer.URL, apiServer)
	return &dynamicGatewayFixture{
		api: api, gateway: newSimulatorGateway(t, config),
		first:    simulatorEndpoint{backend: firstBackend, port: firstPort},
		second:   simulatorEndpoint{backend: secondBackend, port: secondPort},
		fallback: fallbackBackend, fallbackPort: serverPort(t, fallbackServer.URL),
	}
}

func (f *dynamicGatewayFixture) assertRemovedBackendStopsReceiving(t testing.TB) {
	t.Helper()
	initial := completedGatewayRequest(t, f.gateway.URL)
	selectedPort := upstreamPort(t, initial.Header.Get("X-FishMesh-Upstream"))
	selected, remaining := f.first, f.second
	if selectedPort == f.second.port {
		selected, remaining = f.second, f.first
	}
	f.api.setPorts([]int{remaining.port})
	waitForGatewayHeader(t, f.gateway.URL, func(header http.Header) bool {
		return upstreamPort(t, header.Get("X-FishMesh-Upstream")) == remaining.port
	})

	requestsAfterRemoval := selected.backend.Snapshot().Requests
	for range 3 {
		completedGatewayRequest(t, f.gateway.URL)
	}
	if selected.backend.Snapshot().Requests != requestsAfterRemoval {
		t.Fatalf("removed backend received another request: before=%d after=%d", requestsAfterRemoval, selected.backend.Snapshot().Requests)
	}
}

func (f *dynamicGatewayFixture) assertStaleDiscoveryFallsBack(t testing.TB) {
	t.Helper()
	f.api.setUnavailable(true)
	header := waitForGatewayHeader(t, f.gateway.URL, func(header http.Header) bool {
		return header.Get("X-FishMesh-Route-Reason") == "discovery-fallback"
	})
	if upstreamPort(t, header.Get("X-FishMesh-Upstream")) != f.fallbackPort || header.Get("X-FishMesh-Backend-ID") != "service" {
		t.Fatalf("stale fallback backend=%q upstream=%q", header.Get("X-FishMesh-Backend-ID"), header.Get("X-FishMesh-Upstream"))
	}
	ready, err := http.Get(f.gateway.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	ready.Body.Close()
	if ready.StatusCode != http.StatusServiceUnavailable || f.fallback.Snapshot().Requests == 0 {
		t.Fatalf("stale readiness=%d fallback requests=%d", ready.StatusCode, f.fallback.Snapshot().Requests)
	}
}

func endpointSliceGatewayConfig(t testing.TB, fallbackURL string, apiServer *httptest.Server) servingconfig.Config {
	t.Helper()
	t.Setenv("FISHMESH_UPSTREAM_URL", fallbackURL)
	t.Setenv("FISHMESH_ROUTING_MODE", "prefix-affinity")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	t.Setenv("FISHMESH_ENDPOINT_REFRESH_INTERVAL", endpointSliceRefresh.String())
	t.Setenv("FISHMESH_ENDPOINT_MAX_AGE", endpointSliceMaxAge.String())
	t.Setenv("FISHMESH_BACKEND_OBSERVATION_MODE", "none")
	config, err := servingconfig.LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	config.Discovery.EndpointSlice.BaseURL = apiServer.URL
	config.Discovery.EndpointSlice.TokenFile = writeEndpointSliceToken(t)
	config.Discovery.EndpointSlice.CAFile = ""
	config.Discovery.EndpointSlice.HTTPClient = apiServer.Client()
	config.Identity.CAFile = ""
	config.Identity.HTTPClient = apiServer.Client()
	return config
}

func (a *endpointSliceAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/apis/discovery.k8s.io/v1/namespaces/kubellm/endpointslices" {
		http.NotFound(writer, request)
		return
	}
	if request.Header.Get("Authorization") != "Bearer simulator-token" {
		http.Error(writer, "missing simulator token", http.StatusUnauthorized)
		return
	}
	a.mu.RLock()
	unavailable := a.unavailable
	ports := append([]int(nil), a.ports...)
	version := a.version
	a.mu.RUnlock()
	if unavailable {
		http.Error(writer, "simulated Kubernetes API outage", http.StatusServiceUnavailable)
		return
	}
	if request.URL.Query().Get("watch") == "true" {
		<-request.Context().Done()
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(endpointSliceResponseFor(ports, version))
}

func (a *endpointSliceAPI) setPorts(ports []int) {
	a.mu.Lock()
	a.ports = append([]int(nil), ports...)
	a.version++
	a.mu.Unlock()
}

func (a *endpointSliceAPI) setUnavailable(unavailable bool) {
	a.mu.Lock()
	a.unavailable = unavailable
	a.mu.Unlock()
}

func endpointSliceResponseFor(ports []int, version int) endpointSliceListResponse {
	resourceVersion := strconv.Itoa(version)
	response := endpointSliceListResponse{Metadata: endpointSliceMetadata{ResourceVersion: resourceVersion}}
	for _, port := range ports {
		response.Items = append(response.Items, endpointSliceResponse{
			Metadata:    endpointSliceMetadata{Name: "simulator-" + strconv.Itoa(port), ResourceVersion: resourceVersion},
			AddressType: "IPv4", Ports: []endpointSlicePort{{Name: "http", Port: port, Protocol: "TCP"}},
			Endpoints: []endpointSliceEndpoint{{Addresses: []string{"127.0.0.1"}, Conditions: endpointSliceConditions{Ready: true}, TargetRef: endpointSliceTargetReference{Kind: "Pod", Name: "simulator"}}},
		})
	}
	return response
}

func startSimulatorServer(t testing.TB) (*simulator.Backend, *httptest.Server) {
	t.Helper()
	backend := newSimulatorBackend(t, simulator.Behavior{})
	server := httptest.NewServer(backend.Handler())
	t.Cleanup(server.Close)
	return backend, server
}

func completedGatewayRequest(t testing.TB, gatewayURL string) *http.Response {
	t.Helper()
	response := gatewayRequest(t, gatewayURL, t.Context())
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	return response
}

func waitForGatewayHeader(t testing.TB, gatewayURL string, ready func(http.Header) bool) http.Header {
	t.Helper()
	deadline := time.Now().Add(simulatorE2ETimeout)
	var last http.Header
	for time.Now().Before(deadline) {
		response := completedGatewayRequest(t, gatewayURL)
		last = response.Header.Clone()
		if ready(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for Gateway headers: %v", last)
	return nil
}

func serverPort(t testing.TB, rawURL string) int {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return portNumber(t, parsed.Host)
}

func upstreamPort(t testing.TB, host string) int {
	t.Helper()
	return portNumber(t, host)
}

func portNumber(t testing.TB, host string) int {
	t.Helper()
	_, rawPort, err := net.SplitHostPort(host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func writeEndpointSliceToken(t testing.TB) string {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("simulator-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tokenFile
}
