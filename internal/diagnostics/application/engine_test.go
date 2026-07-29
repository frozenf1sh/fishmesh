package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/adapters"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

func TestDemoRegistryReportsPrefixLocalityDegraded(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
	registry, err := adapters.NewDemoRegistry(clock)
	if err != nil {
		t.Fatalf("NewDemoRegistry() error = %v", err)
	}
	engine, err := application.NewEngine(registry, domain.DefaultRulePolicy(), clock)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	report := engine.Analyze(context.Background(), domain.Incident{Metric: "ttft_p99", Description: "TTFT P99 increased"})
	if report.Diagnosis.Code != "prefix_locality_degraded" {
		t.Fatalf("diagnosis code = %q, want prefix_locality_degraded", report.Diagnosis.Code)
	}
	if len(report.Tools) != 5 {
		t.Fatalf("tool count = %d, want 5", len(report.Tools))
	}
	if report.Diagnosis.Recommendation.Code != "enable_bounded_prefix_affinity" {
		t.Fatalf("recommendation = %q", report.Diagnosis.Recommendation.Code)
	}
}

func TestNewRegistryRejectsDuplicateTool(t *testing.T) {
	tool := adapters.StaticTool{DescriptorValue: domain.ToolDescriptor{Name: "same"}}
	if _, err := application.NewRegistry(tool, tool); err == nil {
		t.Fatal("NewRegistry() should reject duplicate tool names")
	}
}
