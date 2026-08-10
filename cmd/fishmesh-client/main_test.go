package main

import (
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/workload/client"
)

func TestDiagnosticEvidenceColorModes(t *testing.T) {
	result := client.Result{TTFT: 12 * time.Millisecond, Duration: 34 * time.Millisecond, Headers: client.DecisionHeaders{RoutingMode: "bounded-affinity", RouteReason: "missing-key-least-loaded", Policy: "bounded-affinity-v1", ExactStatus: "not-requested", BackendID: "backend-a"}}
	plain := formatDiagnosticEvidence(result, false)
	if strings.Contains(plain, ansiEscape) || !strings.Contains(plain, "policy=bounded-affinity-v1") {
		t.Fatalf("plain diagnostics = %q", plain)
	}
	colored := formatDiagnosticEvidence(result, true)
	if !strings.Contains(colored, ansiEscape) || !strings.Contains(colored, "backend-a") {
		t.Fatalf("colored diagnostics = %q", colored)
	}
}

func TestParseColorModeRejectsUnknownValue(t *testing.T) {
	if _, err := parseColorMode("vivid"); err == nil {
		t.Fatal("unknown color mode was accepted")
	}
}
