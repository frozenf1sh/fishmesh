package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestProviderMapsPodNodeAndDeclaredGPU(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{"items":[{"metadata":{"name":"qwen-vllm-0"},"spec":{"nodeName":"fishmesh-gpu","containers":[{"resources":{"requests":{"nvidia.com/gpu":"1"}}}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`))
	}))
	defer server.Close()
	provider, err := NewKubernetes(Config{Namespace: "kubellm", BaseURL: server.URL, TokenFile: tokenFile, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	states, err := provider.Enrich(context.Background(), []backend.Backend{{ID: "endpoint-a", Metadata: map[string]string{backend.MetadataPodName: "qwen-vllm-0"}}})
	if err != nil {
		t.Fatal(err)
	}
	state := states["endpoint-a"]
	if state.Status != StatusOK || !state.Ready || state.PodName != "qwen-vllm-0" || state.NodeName != "fishmesh-gpu" || state.GPURequested != 1 {
		t.Fatalf("unexpected identity: %+v", state)
	}
}

func TestProviderReportsMissingPodAsUnavailable(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	provider, err := NewKubernetes(Config{Namespace: "kubellm", BaseURL: server.URL, TokenFile: tokenFile, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	states, err := provider.Enrich(context.Background(), []backend.Backend{{ID: "endpoint-a", Metadata: map[string]string{backend.MetadataPodName: "missing"}}})
	if err != nil {
		t.Fatal(err)
	}
	if states["endpoint-a"].Status != StatusUnavailable {
		t.Fatalf("expected unavailable identity: %+v", states["endpoint-a"])
	}
}
