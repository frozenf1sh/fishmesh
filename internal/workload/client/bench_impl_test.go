package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	if report.ElapsedMS <= 0 || report.CompletionRateQPS <= 0 || report.Scenarios[0].ElapsedMS <= 0 || report.Scenarios[0].Batches[0].CompletionRateQPS <= 0 {
		t.Fatalf("missing client completion window: %+v", report)
	}
	if report.Scenarios[0].CachedPrefixSamples != 2 || report.Scenarios[0].CachedPrefixSum != 0 {
		t.Fatalf("scenario cache summary = %+v", report.Scenarios[0])
	}
	if records := strings.Count(output.String(), `"record_type"`); records != 5 {
		t.Fatalf("JSONL records=%d, output=%s", records, output.String())
	}
}

func TestConversationLadderCarriesActualAssistantHistoryAcrossTurns(t *testing.T) {
	var messageCounts []int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		messageCounts = append(messageCounts, len(body.Messages))
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderKVStatus, "available")
		writer.Header().Set(HeaderCachedPrefixTokens, "128")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"assistant-turn\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	service, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	plan := BenchmarkPlan{
		RunID: "ladder", MaxTokens: 8, RequestTimeoutMS: 1000, CacheMode: CacheSteadyWarm, RunNonce: "ladder-nonce", WorkloadSeed: 7, Treatment: "contract", CacheGeneration: "generation-a",
		Scenarios: []BenchmarkScenario{{Name: "ladder", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 2, ConversationTurnBytes: 128, Batches: 2, BatchSize: 2, Concurrency: 1}},
	}
	report, err := service.RunPlan(context.Background(), plan, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requested != 4 || report.Succeeded != 4 || report.Scenarios[0].CachedPrefixSamples != 4 {
		t.Fatalf("ladder report = %+v", report)
	}
	if len(messageCounts) != 4 || messageCounts[0] != 2 || messageCounts[1] != 2 || messageCounts[2] != 4 || messageCounts[3] != 4 {
		t.Fatalf("message counts = %v, want [2 2 4 4]", messageCounts)
	}
}

func TestWindowMetricsRejectsMissingOrNonPositiveWindow(t *testing.T) {
	started := time.Unix(10, 0)
	completed := started.Add(2 * time.Second)
	elapsed, rate := windowMetrics(4, started, completed)
	if elapsed != 2000 || rate != 2 {
		t.Fatalf("window metrics = %.2f/%.2f", elapsed, rate)
	}
	if elapsed, rate = windowMetrics(4, completed, started); elapsed != 0 || rate != 0 {
		t.Fatalf("reversed window metrics = %.2f/%.2f", elapsed, rate)
	}
	if elapsed, rate = windowMetrics(4, time.Time{}, completed); elapsed != 0 || rate != 0 {
		t.Fatalf("missing window metrics = %.2f/%.2f", elapsed, rate)
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
	plan.Scenarios[0].ArrivalRateQPS = 10
	if err := plan.Validate(); err != nil {
		t.Fatalf("positive arrival rate rejected: %v", err)
	}
	plan.Scenarios[0].ArrivalRateQPS = -1
	if err := plan.Validate(); err == nil {
		t.Fatal("negative arrival rate was accepted")
	}
	ladder := BenchmarkScenario{Name: "ladder", Pattern: PrefixSame, PrefixBytes: minimumPrefixBytes, PrefixGroups: 1, ConversationTurnBytes: minimumPrefixBytes, Batches: 2, BatchSize: 2, Concurrency: 2}
	if err := ladder.Validate(); err == nil {
		t.Fatal("ladder with mismatched prefix groups was accepted")
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

func TestBenchmarkMixedDistributionUsesScenarioSize(t *testing.T) {
	scenario := BenchmarkScenario{
		Name: "mixed", Pattern: PrefixMixed, PrefixBytes: 128, PrefixGroups: 4,
		Batches: 2, BatchSize: 24, MixedHotRatio: 60, MixedUniqueRatio: 20,
	}
	counts := map[string]int{}
	unique := 0
	for request := 0; request < scenario.Batches*scenario.BatchSize; request++ {
		group, isUnique := benchmarkPrefixGroupForScenario(scenario, request, scenario.Batches*scenario.BatchSize)
		counts[group]++
		if isUnique {
			unique++
		}
	}

	if counts["shared-0"] != 29 || unique != 10 || counts["shared-1"]+counts["shared-2"]+counts["shared-3"] != 9 {
		t.Fatalf("mixed distribution for 48 requests = counts=%v unique=%d", counts, unique)
	}
}

func TestPrepareBenchmarkPlanRecordsDeterministicSeededOrder(t *testing.T) {
	plan := BenchmarkPlan{
		MaxTokens: 8, RequestTimeoutMS: 1000, WorkloadSeed: 42,
		Scenarios: []BenchmarkScenario{
			{Name: "a", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: 1},
			{Name: "b", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: 1},
			{Name: "c", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: 1},
		},
	}
	first, err := PrepareBenchmarkPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareBenchmarkPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(first.ExecutionOrder, ",") != strings.Join(second.ExecutionOrder, ",") || len(first.ExecutionOrder) != 3 {
		t.Fatalf("execution orders = %v / %v", first.ExecutionOrder, second.ExecutionOrder)
	}
}

func TestBenchmarkRequestOrderIsDeterministicAndSeeded(t *testing.T) {
	plan := BenchmarkPlan{WorkloadSeed: 42, RandomizeRequestOrder: true}
	scenario := BenchmarkScenario{Name: "long", Batches: 2, BatchSize: 8}
	first := benchmarkRequestOrder(plan, scenario)
	second := benchmarkRequestOrder(plan, scenario)
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("request order is not deterministic: %v / %v", first, second)
	}
	if fmt.Sprint(first) == "[0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15]" {
		t.Fatalf("request order was not shuffled: %v", first)
	}
	other := benchmarkRequestOrder(BenchmarkPlan{WorkloadSeed: 43, RandomizeRequestOrder: true}, scenario)
	if fmt.Sprint(first) == fmt.Sprint(other) {
		t.Fatalf("different seeds reused request order: %v / %v", first, other)
	}
	seen := map[int]bool{}
	for _, value := range first {
		seen[value] = true
	}
	if len(seen) != 16 {
		t.Fatalf("request order lost inputs: %v", first)
	}
}

func TestCacheSaltIsolationScopesDoNotEnterAttemptRecords(t *testing.T) {
	scenario := BenchmarkScenario{Name: "same", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1}
	cold := BenchmarkPlan{CacheMode: CacheCold, RunNonce: "cold-run"}
	first, scope := benchmarkCacheSalt(cold, scenario, 0, 0, "shared-0")
	second, _ := benchmarkCacheSalt(cold, scenario, 0, 1, "shared-0")
	if first == second || scope != "request" {
		t.Fatalf("cold salts/scopes = %q %q %q", first, second, scope)
	}
	warm := BenchmarkPlan{CacheMode: CacheControlledWarm, RunNonce: "warm-run"}
	first, scope = benchmarkCacheSalt(warm, scenario, -1, 0, "shared-0")
	second, _ = benchmarkCacheSalt(warm, scenario, 1, 9, "shared-0")
	if first != second || scope != "prefix-group" {
		t.Fatalf("warm salts/scopes = %q %q %q", first, second, scope)
	}
	body, err := json.Marshal(BenchmarkAttempt{CacheMode: CacheCold, CacheScope: "request", CacheGeneration: "generation-a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), first) || strings.Contains(string(body), "cache_salt") {
		t.Fatalf("attempt leaked derived cache salt: %s", body)
	}
}

func TestControlledWarmAndFormalPlansRequireAuditableMetadata(t *testing.T) {
	base := BenchmarkPlan{
		MaxTokens: 8, RequestTimeoutMS: 1000, WorkloadSeed: 7, Treatment: "static", CacheMode: CacheControlledWarm,
		RunNonce: "run-a", CacheGeneration: "pods-a",
		Scenarios: []BenchmarkScenario{{Name: "same", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: 1}},
	}
	if _, err := PrepareBenchmarkPlan(base); err == nil || !strings.Contains(err.Error(), "warmup") {
		t.Fatalf("controlled-warm plan without warmup error = %v", err)
	}
	base.Scenarios[0].WarmupRequests = 1
	base.Formal = true
	if _, err := PrepareBenchmarkPlan(base); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("formal plan without provenance error = %v", err)
	}
	base.Provenance = BenchmarkProvenance{
		GitSHA: "abc", GatewayImage: "image@sha256:abc", GatewayPods: []string{"gateway-a/uid"}, VLLMVersion: "0.23.0",
		Model: "qwen", ConfigDigest: "sha256:config", EstimatorProfile: "static-v1",
	}
	if _, err := PrepareBenchmarkPlan(base); err != nil {
		t.Fatalf("complete formal plan rejected: %v", err)
	}
}

func TestRunPlanReportsActualPromptTokenCompliance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderPromptTokens, "1018")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	service, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	plan := BenchmarkPlan{
		RunID: "tokens", MaxTokens: 8, RequestTimeoutMS: 1000,
		Scenarios: []BenchmarkScenario{{Name: "tokens", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 2, Concurrency: 1, TargetPromptTokens: 1024, PromptTokenTolerance: 16}},
	}
	report, err := service.RunPlan(context.Background(), plan, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if report.PromptTokenMissing != 0 || report.PromptTokenViolations != 0 || report.Scenarios[0].ActualPromptTokenP50 != 1018 {
		t.Fatalf("token report = %+v", report)
	}
}

func TestSkipPromptTokenEvidenceKeepsMissingCountWithoutFailingRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	service, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	plan := BenchmarkPlan{
		RunID: "skip-token-evidence", MaxTokens: 8, RequestTimeoutMS: 1000, SkipPromptTokenEvidence: true,
		Scenarios: []BenchmarkScenario{{Name: "capacity", Pattern: PrefixSame, PrefixBytes: 128, PrefixGroups: 1, Batches: 1, BatchSize: 1, Concurrency: 1, TargetPromptTokens: 1024, PromptTokenTolerance: 16}},
	}
	report, err := service.RunPlan(context.Background(), plan, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if report.PromptTokenMissing != 1 || report.Succeeded != 1 {
		t.Fatalf("token evidence report = %+v", report)
	}
}
