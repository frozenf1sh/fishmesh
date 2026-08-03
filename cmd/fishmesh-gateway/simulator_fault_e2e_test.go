package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
	"github.com/frozenf1sh/fishmesh/internal/simulator"
)

const simulatorE2ETimeout = 2 * time.Second

func TestSimulatorE2EPreservesSlowSSE(t *testing.T) {
	backend := newSimulatorBackend(t, simulator.Behavior{FirstEventDelay: 40 * time.Millisecond})
	upstream := httptest.NewServer(backend.Handler())
	defer upstream.Close()
	gatewayServer := newSimulatorGateway(t, simulatorGatewayConfig(t, upstream.URL, 8))

	startedAt := time.Now()
	response := gatewayRequest(t, gatewayServer.URL, context.Background())
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	response.Body.Close()
	if err != nil || !strings.HasPrefix(line, "data:") {
		t.Fatalf("first SSE line = %q, error = %v", line, err)
	}
	if elapsed := time.Since(startedAt); elapsed < 35*time.Millisecond {
		t.Fatalf("simulated first-event delay was not preserved: %s", elapsed)
	}
}

func TestSimulatorE2ERejectsOverloadAndKeepsCancellationNeutral(t *testing.T) {
	backend := newSimulatorBackend(t, simulator.Behavior{Hold: true})
	upstream := httptest.NewServer(backend.Handler())
	defer upstream.Close()
	gatewayServer := newSimulatorGateway(t, simulatorGatewayConfig(t, upstream.URL, 1))

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := gatewayRequest(t, gatewayServer.URL, requestContext)
	defer first.Body.Close()
	waitForSimulator(t, backend, func(state simulator.State) bool { return state.Active == 1 })
	second := gatewayRequest(t, gatewayServer.URL, context.Background())
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests || second.Header.Get("X-FishMesh-Route-Reason") != "admission-capacity" {
		t.Fatalf("overload response = %d, reason = %q", second.StatusCode, second.Header.Get("X-FishMesh-Route-Reason"))
	}

	cancel()
	_, _ = io.ReadAll(first.Body)
	first.Body.Close()
	waitForSimulator(t, backend, func(state simulator.State) bool { return state.Active == 0 && state.Cancellations == 1 })
	metrics := readURL(t, gatewayServer.URL+"/metrics")
	if !strings.Contains(metrics, `fishmesh_gateway_backend_circuit_open{backend_id="backend-0"} 0`) {
		t.Fatalf("client cancellation changed circuit state:\n%s", metrics)
	}
}

func TestSimulatorE2EStreamFailureOpensCircuitAndFallsBack(t *testing.T) {
	backend := newSimulatorBackend(t, simulator.Behavior{Events: 2, AbortAfterEvents: 1})
	upstream := httptest.NewServer(backend.Handler())
	defer upstream.Close()
	config := simulatorGatewayConfig(t, upstream.URL, 8)
	config.Circuit.MinimumRequests = 1
	gatewayServer := newSimulatorGateway(t, config)

	failed := gatewayRequest(t, gatewayServer.URL, context.Background())
	failedBody, readErr := io.ReadAll(failed.Body)
	failed.Body.Close()
	if readErr != nil || strings.Contains(string(failedBody), "[DONE]") {
		t.Fatalf("Gateway should end the downstream stream without inventing a terminal event: body=%q error=%v", failedBody, readErr)
	}
	if err := backend.SetBehavior(simulator.Behavior{}); err != nil {
		t.Fatal(err)
	}

	fallback := gatewayRequest(t, gatewayServer.URL, context.Background())
	_, _ = io.ReadAll(fallback.Body)
	fallback.Body.Close()
	if fallback.Header.Get("X-FishMesh-Route-Reason") != "circuit-fallback" || fallback.Header.Get("X-FishMesh-Backend-ID") != "service" {
		t.Fatalf("fallback backend = %q, reason = %q", fallback.Header.Get("X-FishMesh-Backend-ID"), fallback.Header.Get("X-FishMesh-Route-Reason"))
	}
}

func simulatorGatewayConfig(t testing.TB, upstreamURL string, maxInflight int) servingconfig.Config {
	t.Helper()
	t.Setenv("FISHMESH_UPSTREAM_URL", upstreamURL)
	t.Setenv("FISHMESH_ROUTING_MODE", "prefix-affinity")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "static")
	t.Setenv("FISHMESH_BACKEND_ENDPOINTS", upstreamURL)
	t.Setenv("FISHMESH_BACKEND_OBSERVATION_MODE", "none")
	t.Setenv("FISHMESH_REQUEST_TIMEOUT", "1s")
	t.Setenv("FISHMESH_CIRCUIT_MIN_REQUESTS", "3")
	t.Setenv("FISHMESH_MAX_INFLIGHT_REQUESTS", strconv.Itoa(maxInflight))
	config, err := servingconfig.LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.Admission.MaxInflight != maxInflight {
		t.Fatalf("admission max inflight = %d, want %d", config.Admission.MaxInflight, maxInflight)
	}
	return config
}

func newSimulatorGateway(t testing.TB, config servingconfig.Config) *httptest.Server {
	t.Helper()
	runtime, err := buildRuntime(config, newDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(runtime.handler)
	t.Cleanup(func() {
		server.Close()
		runtime.Close()
	})
	return server
}

func gatewayRequest(t testing.TB, gatewayURL string, ctx context.Context) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-FishMesh-Prefix-Key", "simulator-e2e")
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func newSimulatorBackend(t testing.TB, behavior simulator.Behavior) *simulator.Backend {
	t.Helper()
	backend, err := simulator.New(behavior)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func waitForSimulator(t testing.TB, backend *simulator.Backend, ready func(simulator.State) bool) {
	t.Helper()
	deadline := time.Now().Add(simulatorE2ETimeout)
	for time.Now().Before(deadline) {
		if ready(backend.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for simulator state: %+v", backend.Snapshot())
}

func readURL(t testing.TB, target string) string {
	t.Helper()
	response, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
