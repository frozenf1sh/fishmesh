package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
	if err := plan.Validate(); err != nil {
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

	attempts := make([]BenchmarkAttempt, 0, plannedRequests(plan))
	for _, scenario := range plan.Scenarios {
		for batch := 0; batch < scenario.Batches; batch++ {
			batchAttempts := c.runBenchmarkBatch(ctx, plan, scenario, batch)
			for _, attempt := range batchAttempts {
				if err := writeJSONL(writer, attempt); err != nil {
					return report, err
				}
				attempts = append(attempts, attempt)
			}
			if batch+1 < scenario.Batches {
				if err := waitBetweenBatches(ctx, scenario); err != nil {
					return finishBenchmarkReport(writer, report, attempts, err)
				}
			}
		}
	}

	return finishBenchmarkReport(writer, report, attempts, nil)
}

func (c *Client) runBenchmarkBatch(ctx context.Context, plan BenchmarkPlan, scenario BenchmarkScenario, batch int) []BenchmarkAttempt {
	jobs := make(chan benchmarkJob)
	attempts := make(chan BenchmarkAttempt, scenario.BatchSize)
	workers := scenario.Concurrency
	if workers > scenario.BatchSize {
		workers = scenario.BatchSize
	}

	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for job := range jobs {
				messages, group := benchmarkMessagesForScenario(scenario, job.Request, scenario.Batches*scenario.BatchSize)
				result, err := c.Send(ctx, Request{Messages: messages, MaxTokens: plan.MaxTokens})
				record := BenchmarkAttempt{
					RecordType: "request", RunID: plan.RunID, Scenario: scenario.Name, Pattern: scenario.Pattern,
					Batch: job.Batch, Request: job.Request, PrefixBytes: scenario.PrefixBytes, PromptBytes: promptBytes(messages),
					PrefixGroup: group, StatusCode: result.StatusCode, Headers: result.Headers,
					TTFTMS: durationMS(result.TTFT), DurationMS: durationMS(result.Duration), CachedSample: result.HasCachedPrefixSample,
				}
				if err != nil {
					record.Error = err.Error()
				}
				attempts <- record
			}
		}()
	}

	go func() {
		for request := 0; request < scenario.BatchSize; request++ {
			jobs <- benchmarkJob{Batch: batch, Request: batch*scenario.BatchSize + request}
		}
		close(jobs)
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
