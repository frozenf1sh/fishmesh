package client

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

type benchmarkMetadata struct {
	RecordType string        `json:"record_type"`
	RunID      string        `json:"run_id"`
	Plan       BenchmarkPlan `json:"plan"`
}

type benchmarkJob struct {
	Batch   int
	Request int
}

// RunPlan executes sequential batches and writes metadata, every attempt, and the final report as JSONL.
func (c *Client) RunPlan(ctx context.Context, plan BenchmarkPlan, output io.Writer) (BenchmarkReport, error) {
	return c.runPlan(ctx, plan, output, nil, 0)
}

// RunPlanWithMetrics executes a benchmark and optionally samples Gateway metrics during the run.
// Metrics collection is evidence-only: a reader failure is recorded in the report and does not abort the workload.
func (c *Client) RunPlanWithMetrics(ctx context.Context, plan BenchmarkPlan, output io.Writer, reader GatewayMetricsReader, interval time.Duration) (BenchmarkReport, error) {
	return c.runPlan(ctx, plan, output, reader, interval)
}

func (c *Client) runPlan(ctx context.Context, plan BenchmarkPlan, output io.Writer, reader GatewayMetricsReader, metricsInterval time.Duration) (BenchmarkReport, error) {
	var err error
	plan, err = PrepareBenchmarkPlan(plan)
	if err != nil {
		return BenchmarkReport{}, err
	}
	if output == nil {
		return BenchmarkReport{}, fmt.Errorf("benchmark output must not be nil")
	}
	if plan.RunID == "" {
		plan.RunID = time.Now().UTC().Format("20060102T150405.000000000Z")
	}

	startedAt := time.Now().UTC()
	report := BenchmarkReport{RecordType: "report", RunID: plan.RunID, StartedAt: startedAt, Plan: plan}
	writer := bufio.NewWriter(output)
	if err := writeJSONL(writer, benchmarkMetadata{RecordType: "metadata", RunID: plan.RunID, Plan: plan}); err != nil {
		return report, err
	}
	var metricsRun *gatewayMetricsRun
	metricsWindows := make([]GatewayMetricsWindow, 0, len(plan.Scenarios))
	metricsByScenarioBatch := make(map[string]map[int]GatewayMetricsWindow)
	attempts := make([]BenchmarkAttempt, 0, plannedRequests(plan))
	activeScenario, activeBatch := "", -1
	stopMetrics := func() {
		if metricsRun == nil {
			return
		}
		window := metricsRun.stop(ctx)
		metricsWindows = append(metricsWindows, window)
		if activeScenario != "" {
			if metricsByScenarioBatch[activeScenario] == nil {
				metricsByScenarioBatch[activeScenario] = make(map[int]GatewayMetricsWindow)
			}
			metricsByScenarioBatch[activeScenario][activeBatch] = window
		}
		metricsRun = nil
	}
	finish := func(runErr error) (BenchmarkReport, error) {
		stopMetrics()
		if reader != nil {
			window := combineGatewayMetricsWindows(metricsWindows)
			window.WarmupRequests = benchmarkWarmupRequests(plan)
			window.WarmupExcluded = true
			report.GatewayMetrics = &window
		}
		return finishBenchmarkReport(writer, report, attempts, metricsByScenarioBatch, runErr)
	}
	for _, scenarioName := range plan.ExecutionOrder {
		scenario := benchmarkScenarioByName(plan.Scenarios, scenarioName)
		warmupBackends := make(map[string]string)
		for warmup := 0; warmup < scenario.WarmupRequests; warmup++ {
			attempt := c.runBenchmarkRequest(ctx, plan, scenario, -1, warmup, warmup, "warmup")
			if err := writeJSONL(writer, attempt); err != nil {
				return finish(err)
			}
			if attempt.Error != "" {
				return finish(fmt.Errorf("scenario %q warmup %d failed: %s", scenario.Name, warmup, attempt.Error))
			}
			if plan.CacheMode == CacheControlledWarm {
				if attempt.Headers.BackendID == "" {
					return finish(fmt.Errorf("scenario %q warmup %d lacks backend provenance", scenario.Name, warmup))
				}
				if previous := warmupBackends[attempt.PrefixGroup]; previous != "" && previous != attempt.Headers.BackendID {
					return finish(fmt.Errorf("scenario %q prefix group %q warmed on multiple backends", scenario.Name, attempt.PrefixGroup))
				}
				warmupBackends[attempt.PrefixGroup] = attempt.Headers.BackendID
			}
		}
		for batch := 0; batch < scenario.Batches; batch++ {
			activeScenario, activeBatch = scenario.Name, batch
			if reader != nil {
				metricsRun = beginGatewayMetricsRun(ctx, reader, metricsInterval)
			}
			batchAttempts := c.runBenchmarkBatch(ctx, plan, scenario, batch)
			for _, attempt := range batchAttempts {
				if err := writeJSONL(writer, attempt); err != nil {
					return finish(err)
				}
				attempts = append(attempts, attempt)
			}
			stopMetrics()
			if batch+1 < scenario.Batches {
				if err := waitBetweenBatches(ctx, scenario); err != nil {
					return finish(err)
				}
			}
		}
	}
	if plan.SkipPromptTokenEvidence {
		return finish(nil)
	}
	return finish(benchmarkTokenEvidenceError(attempts))
}

func (c *Client) runBenchmarkBatch(ctx context.Context, plan BenchmarkPlan, scenario BenchmarkScenario, batch int) []BenchmarkAttempt {
	jobs := make(chan benchmarkJob, scenario.BatchSize)
	attempts := make(chan BenchmarkAttempt, scenario.BatchSize)
	workers := scenario.Concurrency
	if workers > scenario.BatchSize {
		workers = scenario.BatchSize
	}
	inputOrder := benchmarkRequestOrder(plan, scenario)

	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for job := range jobs {
				attempts <- c.runBenchmarkRequest(ctx, plan, scenario, job.Batch, job.Request, inputOrder[job.Request], "request")
			}
		}()
	}

	go func() {
		defer close(jobs)
		var next time.Time
		var period time.Duration
		if scenario.ArrivalRateQPS > 0 {
			next = time.Now()
			period = time.Duration(float64(time.Second) / scenario.ArrivalRateQPS)
		}
		for request := 0; request < scenario.BatchSize; request++ {
			if scenario.ArrivalRateQPS > 0 && request > 0 {
				next = next.Add(period)
				timer := time.NewTimer(time.Until(next))
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-timer.C:
				}
			}
			jobs <- benchmarkJob{Batch: batch, Request: batch*scenario.BatchSize + request}
		}
	}()
	wait.Wait()
	close(attempts)

	result := make([]BenchmarkAttempt, 0, scenario.BatchSize)
	for attempt := range attempts {
		result = append(result, attempt)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Request < result[j].Request })
	return result
}

func (c *Client) runBenchmarkRequest(ctx context.Context, plan BenchmarkPlan, scenario BenchmarkScenario, batch, request, inputSequence int, recordType string) BenchmarkAttempt {
	messages, group := benchmarkMessagesForScenario(scenario, inputSequence, scenario.Batches*scenario.BatchSize)
	cacheSalt, cacheScope := benchmarkCacheSalt(plan, scenario, batch, inputSequence, group)
	startedAt := time.Now().UTC()
	result, err := c.Send(ctx, Request{Messages: messages, MaxTokens: plan.MaxTokens, CacheSalt: cacheSalt, IgnoreEOS: plan.IgnoreEOS})
	completedAt := time.Now().UTC()
	record := BenchmarkAttempt{
		RecordType: recordType, RunID: plan.RunID, Scenario: scenario.Name, Pattern: scenario.Pattern,
		Batch: batch, Request: request, InputSequence: inputSequence, PrefixBytes: scenario.PrefixBytes, PromptBytes: promptBytes(messages),
		TargetPromptTokens: scenario.TargetPromptTokens, PromptTokenTolerance: scenario.PromptTokenTolerance,
		PrefixGroup: group, CacheMode: plan.CacheMode,
		CacheScope: cacheScope, CacheGeneration: plan.CacheGeneration, StartedAt: startedAt, CompletedAt: completedAt,
		StatusCode: result.StatusCode, Headers: result.Headers,
		TTFTMS: durationMS(result.TTFT), DurationMS: durationMS(result.Duration), CachedSample: result.HasCachedPrefixSample,
	}
	if result.Headers.PromptTokens > 0 && scenario.TargetPromptTokens > 0 {
		record.PromptTokenDelta = result.Headers.PromptTokens - scenario.TargetPromptTokens
	}
	if err != nil {
		record.Error = err.Error()
	}
	return record
}

// benchmarkRequestOrder returns the logical input sequence for one scenario.
//
// Request is the send position, while InputSequence identifies the generated
// prefix/suffix sample. Keeping the two separate lets both arms receive the
// same shuffled input stream without writing prompt content to artifacts.
func benchmarkRequestOrder(plan BenchmarkPlan, scenario BenchmarkScenario) []int {
	total := scenario.Batches * scenario.BatchSize
	order := make([]int, total)
	for index := range order {
		order[index] = index
	}
	if !plan.RandomizeRequestOrder || plan.WorkloadSeed == 0 || total < 2 {
		return order
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(scenario.Name))
	seed := int64(hasher.Sum64()) ^ plan.WorkloadSeed
	rand.New(rand.NewSource(seed)).Shuffle(len(order), func(i, j int) {
		order[i], order[j] = order[j], order[i]
	})
	return order
}

func benchmarkScenarioByName(scenarios []BenchmarkScenario, name string) BenchmarkScenario {
	for _, scenario := range scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	return BenchmarkScenario{}
}

func benchmarkCacheSalt(plan BenchmarkPlan, scenario BenchmarkScenario, batch, request int, group string) (string, string) {
	var scope, identity string
	switch plan.CacheMode {
	case CacheCold:
		scope, identity = "request", fmt.Sprintf("%s/%d/%d", scenario.Name, batch, request)
	case CacheControlledWarm, CacheSteadyWarm:
		scope, identity = "prefix-group", fmt.Sprintf("%s/%s", scenario.Name, group)
	default:
		return "", ""
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("fishmesh-bench-v1\x00%s\x00%s\x00%s\x00%s", plan.RunNonce, plan.CacheMode, scope, identity)))
	return fmt.Sprintf("%x", digest[:]), scope
}

func benchmarkTokenEvidenceError(attempts []BenchmarkAttempt) error {
	missing, violations := 0, 0
	for _, attempt := range attempts {
		if attempt.TargetPromptTokens == 0 {
			continue
		}
		if attempt.Headers.PromptTokens == 0 {
			missing++
			continue
		}
		if absoluteInt(attempt.PromptTokenDelta) > attempt.PromptTokenTolerance {
			violations++
		}
	}
	if missing > 0 || violations > 0 {
		return fmt.Errorf("prompt token evidence failed: missing=%d outside_tolerance=%d", missing, violations)
	}
	return nil
}

func absoluteInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func benchmarkMessages(scenario BenchmarkScenario, request int) ([]Message, string) {
	return benchmarkMessagesForScenario(scenario, request, 100)
}

func benchmarkMessagesForScenario(scenario BenchmarkScenario, request, totalRequests int) ([]Message, string) {
	group, unique := benchmarkPrefixGroupForScenario(scenario, request, totalRequests)
	prefix := generatedPrefix(scenario, group)
	suffix := fmt.Sprintf("request=%d; give a concise answer and preserve the streaming response.", request)
	if unique {
		// A unique suffix makes the request distinguishable without changing the shared-prefix boundary.
		suffix += " unique-prefix-request."
	}
	return []Message{{Role: RoleSystem, Content: prefix}, {Role: RoleUser, Content: suffix}}, group
}

func benchmarkPrefixGroup(scenario BenchmarkScenario, request int) (string, bool) {
	return benchmarkPrefixGroupForScenario(scenario, request, 100)
}

func benchmarkPrefixGroupForScenario(scenario BenchmarkScenario, request, totalRequests int) (string, bool) {
	switch scenario.Pattern {
	case PrefixDifferent:
		return fmt.Sprintf("unique-%d", request), true
	case PrefixSame:
		return fmt.Sprintf("shared-%d", request%scenario.PrefixGroups), false
	case PrefixMixed:
		hotRatio, uniqueRatio := scenario.MixedHotRatio, scenario.MixedUniqueRatio
		if hotRatio == 0 && uniqueRatio == 0 {
			hotRatio, uniqueRatio = 60, 20
		}
		if totalRequests <= 0 {
			totalRequests = 100
		}
		position := request % totalRequests
		if position*100 < hotRatio*totalRequests {
			return "shared-0", false
		}
		if position*100 < (hotRatio+uniqueRatio)*totalRequests {
			return fmt.Sprintf("unique-%d", request), true
		}
		groups := scenario.PrefixGroups - 1
		if groups <= 0 {
			return "shared-0", false
		}
		return fmt.Sprintf("shared-%d", 1+(request%groups)), false
	default:
		return "invalid", false
	}
}

func generatedPrefix(scenario BenchmarkScenario, group string) string {
	header := fmt.Sprintf("FishMesh final pressure test scenario=%s prefix-group=%s. ", scenario.Name, group)
	if len(header) >= scenario.PrefixBytes {
		return header[:scenario.PrefixBytes]
	}
	filler := "The gateway must preserve streaming semantics, route by real prompt locality, and expose an explicit degradation state. "
	remaining := scenario.PrefixBytes - len(header)
	return header + strings.Repeat(filler, remaining/len(filler)+1)[:remaining]
}

func promptBytes(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	return total
}

func waitBetweenBatches(ctx context.Context, scenario BenchmarkScenario) error {
	pause := scenario.BatchPauseMS
	if pause == 0 {
		pause = defaultBatchPauseMS
	}
	timer := time.NewTimer(time.Duration(pause) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
