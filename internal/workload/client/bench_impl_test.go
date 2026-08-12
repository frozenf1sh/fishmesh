package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunPlanWritesAttemptsAndSeparatesUnavailable(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 2 {
			http.Error(writer, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderKVStatus, "available")
		writer.Header().Set(HeaderCachedPrefixTokens, "0")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	service, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	output := &bytes.Buffer{}
	plan := BenchmarkPlan{RunID: "contract", MaxTokens: 8, RequestTimeoutMS: 1000, Scenarios: []BenchmarkScenario{{Name: "same", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 3, BatchSize: 1, Concurrency: 1, BatchPauseMS: 1}}}
	report, err := service.RunPlan(context.Background(), plan, output)
	if err != nil {
		t.Fatalf("run plan: %v", err)
	}
	if report.Completed != 3 || report.Succeeded != 2 || report.Failed != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Scenarios[0].CachedPrefixSamples != 2 || report.Scenarios[0].CachedPrefixSum != 0 {
		t.Fatalf("scenario cache summary = %+v", report.Scenarios[0])
	}
	if records := strings.Count(output.String(), `"record_type"`); records != 5 {
		t.Fatalf("JSONL records=%d, output=%s", records, output.String())
	}
}

func TestBenchmarkPlanValidatesPatternsAndLoadDiscipline(t *testing.T) {
	for _, pattern := range []PrefixPattern{PrefixSame, PrefixDifferent, PrefixMixed} {
		scenario := BenchmarkScenario{Name: string(pattern), Pattern: pattern, PrefixBytes: minimumPrefixBytes, PrefixGroups: 2, Batches: 2, BatchSize: 2, Concurrency: defaultBenchmarkConcurrency}
		if err := scenario.Validate(); err != nil {
			t.Fatalf("pattern %q rejected: %v", pattern, err)
		}
	}
	plan := BenchmarkPlan{MaxTokens: 8, RequestTimeoutMS: 1000, Scenarios: []BenchmarkScenario{{Name: "high", Pattern: PrefixSame, PrefixBytes: minimumPrefixBytes, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: defaultBenchmarkConcurrency + 1}}}
	if err := plan.Validate(); err == nil {
		t.Fatal("high concurrency was accepted without explicit opt-in")
	}
	plan.AllowHighConcurrency = true
	if err := plan.Validate(); err != nil {
		t.Fatalf("explicit high concurrency opt-in rejected: %v", err)
	}
}

func TestBenchmarkPrefixShapes(t *testing.T) {
	same := BenchmarkScenario{Name: "same", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1}
	first, _ := benchmarkMessages(same, 0)
	second, _ := benchmarkMessages(same, 1)
	if first[0].Content != second[0].Content {
		t.Fatal("same-prefix scenario did not reuse the system prefix")
	}

	different := same
	different.Pattern = PrefixDifferent
	first, _ = benchmarkMessages(different, 0)
	second, _ = benchmarkMessages(different, 1)
	if first[0].Content == second[0].Content {
		t.Fatal("different-prefix scenario reused a system prefix")
	}

	mixed := BenchmarkScenario{Name: "mixed", Pattern: PrefixMixed, PrefixBytes: 128, PrefixGroups: 3, MixedHotRatio: 60, MixedUniqueRatio: 20}
	group0, _ := benchmarkPrefixGroup(mixed, 0)
	group70, unique70 := benchmarkPrefixGroup(mixed, 70)
	group90, unique90 := benchmarkPrefixGroup(mixed, 90)
	if group0 != "shared-0" || group70 == "" || !unique70 || unique90 || group90 == group0 {
		t.Fatalf("mixed prefix groups are incorrect: %q/%t %q/%t %q/%t", group0, false, group70, unique70, group90, unique90)
	}
}
