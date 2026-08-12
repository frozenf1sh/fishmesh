package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/workload/client"
)

func TestDiagnosticEvidenceColorModes(t *testing.T) {
	result := client.Result{TTFT: 12 * time.Millisecond, Duration: 34 * time.Millisecond, Headers: client.DecisionHeaders{RoutingMode: "session-key", RouteReason: "missing-session-key-load-balanced", Policy: "session-key-v1", KVStatus: "not-requested", BackendID: "backend-a"}}
	plain := formatDiagnosticEvidence(result, false)
	if strings.Contains(plain, ansiEscape) || !strings.Contains(plain, "policy=session-key-v1") {
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

func TestResolveHistoryPathUsesTimestampedPrivateStateFile(t *testing.T) {
	now := time.Date(2026, time.August, 15, 1, 2, 3, 456000000, time.FixedZone("CST", 8*60*60))
	path, err := resolveHistoryPath("", now)
	if err != nil {
		t.Fatalf("resolve history path: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	want := filepath.Join(home, ".local/state/fishmesh", "20260814T170203.456000000Z.json")
	if path != want {
		t.Fatalf("history path = %q, want %q", path, want)
	}
}

func TestChatUsesDefaultHistoryWhenFlagIsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []client.Message `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != client.RoleSystem || body.Messages[1].Content != "hello" {
			t.Fatalf("request messages = %+v", body.Messages)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(client.HeaderKVStatus, "available")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	var output, diagnostics bytes.Buffer
	err := runChat([]string{"--endpoint", server.URL, "--model", "qwen", "--timeout", "1s", "--color", "never", "--system", "system"}, strings.NewReader("hello\n"), &output, &diagnostics)
	if err != nil {
		t.Fatalf("run chat: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(home, ".local/state/fishmesh", "*.json"))
	if err != nil {
		t.Fatalf("find history: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("history files = %v", paths)
	}
	messages, err := client.LoadHistory(paths[0])
	if err != nil {
		t.Fatalf("load default history: %v", err)
	}
	if len(messages) != 3 || messages[2].Role != client.RoleAssistant || messages[2].Content != "world" {
		t.Fatalf("default history = %+v", messages)
	}
	if !strings.Contains(diagnostics.String(), "using default history=") {
		t.Fatalf("diagnostics did not identify default history: %q", diagnostics.String())
	}
}

func TestBenchWritesMatrixReports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(client.HeaderKVStatus, "available")
		writer.Header().Set(client.HeaderCachedPrefixTokens, "12")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	root := t.TempDir()
	planPath := filepath.Join(root, "plan.json")
	plan := `{"run_id":"cli-contract","max_tokens":8,"request_timeout_ms":1000,"scenarios":[{"name":"same","prefix_pattern":"same-prefix","prefix_bytes":128,"prefix_groups":1,"batches":1,"batch_size":2,"concurrency":1}]}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(root, "report")
	var diagnostics bytes.Buffer
	if err := runBenchmark([]string{"--endpoint", server.URL, "--model", "qwen", "--plan", planPath, "--output-dir", outputDir}, &diagnostics); err != nil {
		t.Fatalf("run benchmark: %v", err)
	}
	for _, name := range []string{"plan.json", "requests.jsonl", "report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(outputDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	report, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), `"succeeded": 2`) || !strings.Contains(diagnostics.String(), "report=") {
		t.Fatalf("report/diagnostics = %s / %s", report, diagnostics.String())
	}
}
