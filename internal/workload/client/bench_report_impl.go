package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func finishBenchmarkReport(writer *bufio.Writer, report BenchmarkReport, attempts []BenchmarkAttempt, gatewayByScenarioBatch map[string]map[int]GatewayMetricsWindow, runErr error) (BenchmarkReport, error) {
	report.CompletedAt = time.Now().UTC()
	finalizeBenchmarkReport(&report, attempts)
	applyGatewayMetricsToReport(&report, gatewayByScenarioBatch)
	if err := writeJSONL(writer, report); err != nil {
		return report, err
	}
	if err := writer.Flush(); err != nil {
		return report, fmt.Errorf("flush benchmark JSONL: %w", err)
	}
	return report, runErr
}

func applyGatewayMetricsToReport(report *BenchmarkReport, gatewayByScenarioBatch map[string]map[int]GatewayMetricsWindow) {
	for scenarioIndex := range report.Scenarios {
		scenario := &report.Scenarios[scenarioIndex]
		batchWindows := gatewayByScenarioBatch[scenario.Name]
		if len(batchWindows) == 0 {
			continue
		}
		windows := make([]GatewayMetricsWindow, 0, len(batchWindows))
		for batchIndex := range scenario.Batches {
			window, ok := batchWindows[batchIndex]
			if !ok {
				continue
			}
			window.WarmupExcluded = true
			scenario.Batches[batchIndex].GatewayMetrics = &window
			windows = append(windows, window)
		}
		if len(windows) == 0 {
			continue
		}
		combined := combineGatewayMetricsWindows(windows)
		combined.WarmupExcluded = true
		scenario.GatewayMetrics = &combined
	}
}

func finalizeBenchmarkReport(report *BenchmarkReport, attempts []BenchmarkAttempt) {
	byScenario := make(map[string][]BenchmarkAttempt, len(report.Plan.Scenarios))
	for _, scenario := range report.Plan.Scenarios {
		byScenario[scenario.Name] = nil
	}
	for _, attempt := range attempts {
		byScenario[attempt.Scenario] = append(byScenario[attempt.Scenario], attempt)
	}
	report.Scenarios = make([]BenchmarkScenarioReport, 0, len(report.Plan.Scenarios))
	for _, scenario := range report.Plan.Scenarios {
		items := byScenario[scenario.Name]
		summary := summarizeAttempts(items)
		scenarioReport := BenchmarkScenarioReport{
			Name: scenario.Name, Pattern: scenario.Pattern, PrefixBytes: scenario.PrefixBytes, PrefixGroups: scenario.PrefixGroups,
			ArrivalRateQPS:     scenario.ArrivalRateQPS,
			TargetPromptTokens: scenario.TargetPromptTokens, PromptTokenTolerance: scenario.PromptTokenTolerance,
			Requested: scenario.Batches * scenario.BatchSize, Batches: make([]BenchmarkBatchReport, 0, scenario.Batches),
		}
		applyScenarioMetrics(&scenarioReport, summary)
		for batch := 0; batch < scenario.Batches; batch++ {
			batchItems := make([]BenchmarkAttempt, 0, scenario.BatchSize)
			for _, item := range items {
				if item.Batch == batch {
					batchItems = append(batchItems, item)
				}
			}
			batchSummary := summarizeAttempts(batchItems)
			batchReport := BenchmarkBatchReport{Batch: batch, Requested: scenario.BatchSize}
			applyBatchMetrics(&batchReport, batchSummary)
			scenarioReport.Batches = append(scenarioReport.Batches, batchReport)
		}
		report.Scenarios = append(report.Scenarios, scenarioReport)
	}
	global := summarizeAttempts(attempts)
	report.Requested = plannedRequests(report.Plan)
	report.Completed = global.completed
	report.Succeeded = global.succeeded
	report.Failed = global.failed
	report.TTFTP50MS = percentile(global.ttfts, 50)
	report.TTFTP95MS = percentile(global.ttfts, 95)
	report.DurationP50MS = percentile(global.durations, 50)
	report.DurationP95MS = percentile(global.durations, 95)
	report.PromptTokenMissing = global.promptTokenMissing
	report.PromptTokenViolations = global.promptTokenViolations
	applyReportWindow(report, global)
}

type benchmarkMetrics struct {
	completed, succeeded, failed       int
	ttfts, durations                   []float64
	kvStatuses, routeReasons           map[string]int
	backends                           map[string]int
	windowStartedAt, windowCompletedAt time.Time
	cachedSamples, cachedSum           int
	promptTokens                       []int
	promptTokenMissing                 int
	promptTokenViolations              int
}

func summarizeAttempts(attempts []BenchmarkAttempt) benchmarkMetrics {
	metrics := benchmarkMetrics{kvStatuses: map[string]int{}, routeReasons: map[string]int{}, backends: map[string]int{}}
	for _, attempt := range attempts {
		if !attempt.StartedAt.IsZero() && (metrics.windowStartedAt.IsZero() || attempt.StartedAt.Before(metrics.windowStartedAt)) {
			metrics.windowStartedAt = attempt.StartedAt
		}
		if !attempt.CompletedAt.IsZero() && (metrics.windowCompletedAt.IsZero() || attempt.CompletedAt.After(metrics.windowCompletedAt)) {
			metrics.windowCompletedAt = attempt.CompletedAt
		}
		metrics.completed++
		if attempt.Error == "" {
			metrics.succeeded++
		} else {
			metrics.failed++
		}
		if attempt.TTFTMS > 0 {
			metrics.ttfts = append(metrics.ttfts, attempt.TTFTMS)
		}
		if attempt.DurationMS > 0 {
			metrics.durations = append(metrics.durations, attempt.DurationMS)
		}
		if attempt.Headers.KVStatus != "" {
			metrics.kvStatuses[attempt.Headers.KVStatus]++
		}
		if attempt.Headers.RouteReason != "" {
			metrics.routeReasons[attempt.Headers.RouteReason]++
		}
		if attempt.Headers.BackendID != "" {
			metrics.backends[attempt.Headers.BackendID]++
		}
		if attempt.CachedSample {
			metrics.cachedSamples++
			metrics.cachedSum += attempt.Headers.CachedPrefixTokens
		}
		if attempt.TargetPromptTokens > 0 {
			if attempt.Headers.PromptTokens == 0 {
				metrics.promptTokenMissing++
			} else {
				metrics.promptTokens = append(metrics.promptTokens, attempt.Headers.PromptTokens)
				if absoluteInt(attempt.PromptTokenDelta) > attempt.PromptTokenTolerance {
					metrics.promptTokenViolations++
				}
			}
		}
	}
	return metrics
}

func applyBatchMetrics(report *BenchmarkBatchReport, metrics benchmarkMetrics) {
	applyBatchWindow(report, metrics)
	report.Completed, report.Succeeded, report.Failed = metrics.completed, metrics.succeeded, metrics.failed
	report.TTFTP50MS, report.TTFTP95MS = percentile(metrics.ttfts, 50), percentile(metrics.ttfts, 95)
	report.DurationP50MS, report.DurationP95MS = percentile(metrics.durations, 50), percentile(metrics.durations, 95)
	report.KVStatuses, report.RouteReasons = metrics.kvStatuses, metrics.routeReasons
	report.CachedPrefixSamples, report.CachedPrefixSum = metrics.cachedSamples, metrics.cachedSum
}

func applyScenarioMetrics(report *BenchmarkScenarioReport, metrics benchmarkMetrics) {
	applyScenarioWindow(report, metrics)
	report.Completed, report.Succeeded, report.Failed = metrics.completed, metrics.succeeded, metrics.failed
	report.TTFTP50MS, report.TTFTP95MS = percentile(metrics.ttfts, 50), percentile(metrics.ttfts, 95)
	report.DurationP50MS, report.DurationP95MS = percentile(metrics.durations, 50), percentile(metrics.durations, 95)
	report.KVStatuses, report.RouteReasons, report.Backends = metrics.kvStatuses, metrics.routeReasons, metrics.backends
	report.CachedPrefixSamples, report.CachedPrefixSum = metrics.cachedSamples, metrics.cachedSum
	if len(metrics.promptTokens) > 0 {
		report.ActualPromptTokenMin = minimumInt(metrics.promptTokens)
		report.ActualPromptTokenP50 = percentileInt(metrics.promptTokens, 50)
		report.ActualPromptTokenP95 = percentileInt(metrics.promptTokens, 95)
		report.ActualPromptTokenMax = maximumInt(metrics.promptTokens)
	}
	report.PromptTokenMissing = metrics.promptTokenMissing
	report.PromptTokenViolations = metrics.promptTokenViolations
}

func applyBatchWindow(report *BenchmarkBatchReport, metrics benchmarkMetrics) {
	report.WindowStartedAt = metrics.windowStartedAt
	report.WindowCompletedAt = metrics.windowCompletedAt
	report.ElapsedMS, report.CompletionRateQPS = windowMetrics(metrics.completed, metrics.windowStartedAt, metrics.windowCompletedAt)
}

func applyScenarioWindow(report *BenchmarkScenarioReport, metrics benchmarkMetrics) {
	report.WindowStartedAt = metrics.windowStartedAt
	report.WindowCompletedAt = metrics.windowCompletedAt
	report.ElapsedMS, report.CompletionRateQPS = windowMetrics(metrics.completed, metrics.windowStartedAt, metrics.windowCompletedAt)
}

func applyReportWindow(report *BenchmarkReport, metrics benchmarkMetrics) {
	report.ElapsedMS, report.CompletionRateQPS = windowMetrics(metrics.completed, metrics.windowStartedAt, metrics.windowCompletedAt)
}

func windowMetrics(completed int, startedAt, completedAt time.Time) (float64, float64) {
	if completed <= 0 || startedAt.IsZero() || completedAt.IsZero() || !completedAt.After(startedAt) {
		return 0, 0
	}
	elapsed := completedAt.Sub(startedAt)
	return durationMS(elapsed), float64(completed) / elapsed.Seconds()
}

func plannedRequests(plan BenchmarkPlan) int {
	requests := 0
	for _, scenario := range plan.Scenarios {
		requests += scenario.Batches * scenario.BatchSize
	}
	return requests
}

func writeJSONL(writer *bufio.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode benchmark JSONL: %w", err)
	}
	if _, err := writer.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write benchmark JSONL: %w", err)
	}
	return nil
}

func durationMS(duration time.Duration) float64 { return float64(duration) / float64(time.Millisecond) }

func percentile(values []float64, point int) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	index := (len(values) - 1) * point / 100
	return values[index]
}

func percentileInt(values []int, point int) int {
	if len(values) == 0 {
		return 0
	}
	copied := append([]int(nil), values...)
	sort.Ints(copied)
	return copied[(len(copied)-1)*point/100]
}

func minimumInt(values []int) int {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func maximumInt(values []int) int {
	maximum := values[0]
	for _, value := range values[1:] {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}
