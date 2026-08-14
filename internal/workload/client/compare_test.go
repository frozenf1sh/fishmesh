package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareBenchmarkFilesPoolsRunsAndReportsEstimatorError(t *testing.T) {
	directory := t.TempDir()
	baseline := writeComparisonRun(t, directory, "baseline.jsonl", []BenchmarkAttempt{
		comparisonAttempt(100, 90), comparisonAttempt(200, 180), comparisonAttempt(300, 270), comparisonAttempt(400, 360),
	})
	treatment := writeComparisonRun(t, directory, "treatment.jsonl", []BenchmarkAttempt{
		comparisonAttempt(50, 55), comparisonAttempt(100, 90), comparisonAttempt(150, 140), comparisonAttempt(200, 210),
	})
	report, err := CompareBenchmarkFiles([]string{baseline}, []string{treatment}, 200, 7)
	if err != nil {
		t.Fatal(err)
	}
	if report.Baseline.Succeeded != 4 || report.Treatment.Succeeded != 4 || report.TTFTP95DeltaPercent >= 0 || report.Treatment.EstimatorSamples != 4 {
		t.Fatalf("report = %+v", report)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "Bootstrap 95% CI") || !strings.Contains(markdown, "Static MAE") || !strings.Contains(markdown, "Learned MAE") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestCompareGatewayReportFilesAggregatesValidWindows(t *testing.T) {
	directory := t.TempDir()
	baseline := writeGatewayReport(t, directory, "baseline-report.json", 4, 3, 1, 2)
	treatment := writeGatewayReport(t, directory, "treatment-report.json", 6, 5, 0.5, 3)
	baselineArm, treatmentArm, err := CompareGatewayReportFiles([]string{baseline}, []string{treatment})
	if err != nil {
		t.Fatal(err)
	}
	if baselineArm.ValidRuns != 1 || baselineArm.AcceptedRateQPS != 4 || baselineArm.CompletedRateQPS != 3 || baselineArm.RejectionRateQPS != 1 || baselineArm.LittleLawWaitMS != 2 {
		t.Fatalf("baseline Gateway arm = %+v", baselineArm)
	}
	if treatmentArm.ValidRuns != 1 || treatmentArm.AcceptedRateQPS != 6 || treatmentArm.CompletedRateQPS != 5 || treatmentArm.RejectionRateQPS != 0.5 || treatmentArm.LittleLawWaitMS != 3 {
		t.Fatalf("treatment Gateway arm = %+v", treatmentArm)
	}
}

func writeGatewayReport(t *testing.T, directory, name string, accepted, completed, rejected, waitMS float64) string {
	t.Helper()
	path := filepath.Join(directory, name)
	body, err := json.Marshal(BenchmarkReport{RecordType: "report", GatewayMetrics: &GatewayMetricsWindow{
		Valid: true, AcceptedRateQPS: accepted, CompletedRateQPS: completed, RejectionRateQPS: rejected, LittleLawWaitMS: waitMS,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeComparisonRun(t *testing.T, directory, name string, attempts []BenchmarkAttempt) string {
	t.Helper()
	path := filepath.Join(directory, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		body, err := json.Marshal(attempt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(body, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func comparisonAttempt(ttft, estimate float64) BenchmarkAttempt {
	return BenchmarkAttempt{
		RecordType: "request", StatusCode: 200, TTFTMS: ttft,
		Headers: DecisionHeaders{EstimatorValid: true, EstimatedTTFTMS: estimate},
	}
}
