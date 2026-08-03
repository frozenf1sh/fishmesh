package simulator

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testWaitTimeout = time.Second

func TestBackendServesOpenAIStreamAndVLLMMetrics(t *testing.T) {
	backend := newTestBackend(t, Behavior{Events: 2, QueueDepth: 3, RunningRequests: 1})
	server := httptest.NewServer(backend.Handler())
	defer server.Close()

	response := doRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", nil)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !strings.Contains(string(body), sseDone) || strings.Count(string(body), "token-") != 2 {
		t.Fatalf("unexpected SSE body %q, error %v", body, err)
	}
	metrics := doRequest(t, http.MethodGet, server.URL+"/metrics", nil)
	metricsBody, _ := io.ReadAll(metrics.Body)
	metrics.Body.Close()
	if !strings.Contains(string(metricsBody), metricRequestsWaiting+" 3") || !strings.Contains(string(metricsBody), metricRequestsRunning+" 1") {
		t.Fatalf("unexpected metrics: %s", metricsBody)
	}
}

func TestControlAPIReplacesBehavior(t *testing.T) {
	backend := newTestBackend(t, Behavior{})
	server := httptest.NewServer(backend.Handler())
	defer server.Close()
	payload := strings.NewReader(`{"status_code":503,"events":1,"queue_depth":4}`)
	response := doRequest(t, http.MethodPut, server.URL+"/control/behavior", payload)
	response.Body.Close()

	failure := doRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", nil)
	failure.Body.Close()
	if failure.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", failure.StatusCode)
	}
	state := backend.Snapshot()
	if state.ForcedErrors != 1 || state.Behavior.QueueDepth != 4 {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestBackendTracksCancellation(t *testing.T) {
	backend := newTestBackend(t, Behavior{Hold: true})
	server := httptest.NewServer(backend.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, backend, func(state State) bool { return state.Active == 1 })
	cancel()
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	waitForState(t, backend, func(state State) bool { return state.Active == 0 && state.Cancellations == 1 })
}

func TestBackendAbortsStreamAfterHeaders(t *testing.T) {
	backend := newTestBackend(t, Behavior{Events: 2, AbortAfterEvents: 1})
	server := httptest.NewServer(backend.Handler())
	defer server.Close()
	response := doRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", nil)
	_, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err == nil {
		t.Fatal("expected stream abort to surface as a body read error")
	}
	if backend.Snapshot().StreamAborts != 1 {
		t.Fatalf("unexpected state: %+v", backend.Snapshot())
	}
}

func TestBehaviorRejectsAmbiguousValues(t *testing.T) {
	tests := map[string]Behavior{
		"redirect":        {StatusCode: http.StatusFound},
		"negative delay":  {FirstEventDelay: -time.Millisecond},
		"negative events": {Events: -1},
		"abort past end":  {Events: 1, AbortAfterEvents: 2},
		"negative queue":  {QueueDepth: -1},
	}
	for name, behavior := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(behavior); err == nil {
				t.Fatal("expected invalid behavior to be rejected")
			}
		})
	}
}

func newTestBackend(t testing.TB, behavior Behavior) *Backend {
	t.Helper()
	backend, err := New(behavior)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func doRequest(t testing.TB, method, target string, body io.Reader) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForState(t testing.TB, backend *Backend, ready func(State) bool) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		if ready(backend.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for simulator state: %+v", backend.Snapshot())
}
