package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
)

func TestBuildRuntimeComposesStaticGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	t.Setenv("FISHMESH_UPSTREAM_URL", upstream.URL)

	config, err := servingconfig.LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := buildRuntime(config, newDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://gateway/v1/models", nil)
	runtime.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("gateway status = %d", response.Code)
	}
	runtime.Close()
}

func TestBuildRuntimeRejectsInvalidConfigBeforeCreatingResources(t *testing.T) {
	_, err := buildRuntime(servingconfig.Config{}, newDiscardLogger())
	if err == nil {
		t.Fatal("expected invalid config to fail before composition")
	}
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
