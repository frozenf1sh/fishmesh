package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEndpointSliceResolverSelectsReadyHTTPAddresses(t *testing.T) {
	ready := true
	notReady := false
	httpName := "http"
	metricsName := "metrics"
	port := int32(8000)
	metricsPort := int32(9000)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(writer, "missing token", http.StatusUnauthorized)
			return
		}
		if request.URL.Query().Get("watch") == "true" {
			<-request.Context().Done()
			return
		}
		if request.URL.Path != "/apis/discovery.k8s.io/v1/namespaces/kubellm/endpointslices" {
			http.NotFound(writer, request)
			return
		}
		response := endpointSliceList{Items: []endpointSliceResource{
			{Metadata: metadata("slice-a", "7"), AddressType: "IPv4", Ports: []endpointSlicePort{{Name: &metricsName, Port: &metricsPort}, {Name: &httpName, Port: &port}}, Endpoints: []endpointEntry{{Addresses: []string{"10.0.0.2"}, Conditions: endpointConditions(&ready)}}},
			{Metadata: metadata("slice-b", "7"), AddressType: "IPv4", Ports: []endpointSlicePort{{Name: &httpName, Port: &port}}, Endpoints: []endpointEntry{{Addresses: []string{"10.0.0.3"}, Conditions: endpointConditions(&notReady)}}},
		}}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	resolver, err := NewEndpointSlice(EndpointSliceConfig{
		Namespace: "kubellm", ServiceName: "qwen-vllm", BaseURL: server.URL,
		TokenFile: tokenFile, HTTPClient: server.Client(), RefreshInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	backends, err := resolver.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(backends) != 1 || backends[0].URL != "http://10.0.0.2:8000" {
		t.Fatalf("unexpected ready backends: %#v", backends)
	}
	if backends[0].ID == "" {
		t.Fatal("expected stable backend ID")
	}
	status := resolver.Status()
	if status.Status != StatusOK || status.ReadyBackends != 1 || status.LastSuccess.IsZero() {
		t.Fatalf("unexpected resolver status: %+v", status)
	}
}

func TestBuildBackendsDeduplicatesAndSortsAddresses(t *testing.T) {
	ready := true
	port := int32(8000)
	items := []endpointSliceResource{{
		AddressType: "IPv4", Ports: []endpointSlicePort{{Port: &port}},
		Endpoints: []endpointEntry{{Addresses: []string{"10.0.0.3", "10.0.0.2"}, Conditions: endpointConditions(&ready)}},
	}, {
		AddressType: "IPv4", Ports: []endpointSlicePort{{Port: &port}},
		Endpoints: []endpointEntry{{Addresses: []string{"10.0.0.2"}, Conditions: endpointConditions(&ready)}},
	}}
	backends := buildBackends(items)
	if len(backends) != 2 || backends[0].URL != "http://10.0.0.2:8000" || backends[1].URL != "http://10.0.0.3:8000" {
		t.Fatalf("unexpected backends: %#v", backends)
	}
}

func metadata(name, version string) struct {
	Name            string `json:"name"`
	ResourceVersion string `json:"resourceVersion"`
} {
	return struct {
		Name            string `json:"name"`
		ResourceVersion string `json:"resourceVersion"`
	}{Name: name, ResourceVersion: version}
}

func endpointConditions(ready *bool) struct {
	Ready       *bool `json:"ready"`
	Serving     *bool `json:"serving"`
	Terminating *bool `json:"terminating"`
} {
	return struct {
		Ready       *bool `json:"ready"`
		Serving     *bool `json:"serving"`
		Terminating *bool `json:"terminating"`
	}{Ready: ready}
}
