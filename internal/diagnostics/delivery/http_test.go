package delivery_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/adapters"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/delivery"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

func TestHTTPServerAnalyzeAndTools(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
	registry, err := adapters.NewDemoRegistry(clock)
	if err != nil {
		t.Fatalf("NewDemoRegistry() error = %v", err)
	}
	engine, err := application.NewEngine(registry, domain.DefaultRulePolicy(), clock)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	server, err := delivery.NewHTTPServer(engine, registry.Descriptors(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPServer() error = %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/v1/tools")
	if err != nil {
		t.Fatalf("GET /v1/tools error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/tools status = %d", response.StatusCode)
	}

	body, _ := json.Marshal(domain.Incident{Metric: "ttft_p99", Description: "TTFT P99 increased"})
	response, err = http.Post(httpServer.URL+"/v1/analyze", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/analyze error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/analyze status = %d", response.StatusCode)
	}
	var report domain.AnalysisReport
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Diagnosis.Code != "prefix_locality_degraded" {
		t.Fatalf("diagnosis code = %q", report.Diagnosis.Code)
	}
}
