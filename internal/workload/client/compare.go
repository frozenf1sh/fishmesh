package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
)

const maxComparisonJSONLLineBytes = 1 << 20

// ComparisonArm 汇总一个 treatment 的 pooled 与跨轮证据。
type ComparisonArm struct {
	Runs                                int     `json:"runs"`
	Succeeded                           int     `json:"succeeded"`
	Failed                              int     `json:"failed"`
	TTFTP50MS                           float64 `json:"ttft_p50_ms"`
	TTFTP95MS                           float64 `json:"ttft_p95_ms"`
	TTFTP99MS                           float64 `json:"ttft_p99_ms"`
	RunMedianTTFTP95MS                  float64 `json:"run_median_ttft_p95_ms"`
	EstimatorSamples                    int     `json:"estimator_samples"`
	EstimatorMAEMS                      float64 `json:"estimator_mae_ms"`
	EstimatorAbsoluteP95MS              float64 `json:"estimator_absolute_p95_ms"`
	PredictionSamples                   int     `json:"prediction_samples"`
	PredictionMAEMS                     float64 `json:"prediction_mae_ms"`
	PredictionAbsoluteP95MS             float64 `json:"prediction_absolute_p95_ms"`
	PredictionAgreementSamples          int     `json:"prediction_agreement_samples"`
	PredictionAgreementPercent          float64 `json:"prediction_agreement_percent"`
	PairedSamples                       int     `json:"paired_samples"`
	PairedPredictionMinusEstimatorMAEMS float64 `json:"paired_prediction_minus_estimator_mae_ms"`
}

// ComparisonReport 是多轮 A/B 的机器可读汇总。
type ComparisonReport struct {
	Baseline             ComparisonArm        `json:"baseline"`
	Treatment            ComparisonArm        `json:"treatment"`
	BaselineGateway      GatewayComparisonArm `json:"baseline_gateway,omitempty"`
	TreatmentGateway     GatewayComparisonArm `json:"treatment_gateway,omitempty"`
	TTFTP95DeltaPercent  float64              `json:"ttft_p95_delta_percent"`
	BootstrapLowPercent  float64              `json:"bootstrap_ci_low_percent"`
	BootstrapHighPercent float64              `json:"bootstrap_ci_high_percent"`
	BootstrapSamples     int                  `json:"bootstrap_samples"`
	Seed                 int64                `json:"seed"`
}

// GatewayComparisonArm summarizes report-level Gateway evidence across runs.
type GatewayComparisonArm struct {
	Runs             int     `json:"runs"`
	ValidRuns        int     `json:"valid_runs"`
	AcceptedRateQPS  float64 `json:"accepted_rate_qps"`
	CompletedRateQPS float64 `json:"completed_rate_qps"`
	RejectionRateQPS float64 `json:"rejection_rate_qps"`
	AverageInflight  float64 `json:"average_inflight"`
	LittleLawWaitMS  float64 `json:"little_law_wait_ms"`
}

// CompareBenchmarkFiles 汇总 request-level JSONL；不读取 prompt 或任意非 allowlist header。
func CompareBenchmarkFiles(baselinePaths, treatmentPaths []string, bootstrapSamples int, seed int64) (ComparisonReport, error) {
	if len(baselinePaths) == 0 || len(treatmentPaths) == 0 {
		return ComparisonReport{}, fmt.Errorf("comparison requires at least one baseline and treatment run")
	}
	if bootstrapSamples < 100 || bootstrapSamples > 100_000 {
		return ComparisonReport{}, fmt.Errorf("bootstrap samples must be between 100 and 100000")
	}
	baselineRuns, err := loadComparisonRuns(baselinePaths)
	if err != nil {
		return ComparisonReport{}, fmt.Errorf("baseline: %w", err)
	}
	treatmentRuns, err := loadComparisonRuns(treatmentPaths)
	if err != nil {
		return ComparisonReport{}, fmt.Errorf("treatment: %w", err)
	}
	report := ComparisonReport{
		Baseline: summarizeComparisonArm(baselineRuns), Treatment: summarizeComparisonArm(treatmentRuns),
		BootstrapSamples: bootstrapSamples, Seed: seed,
	}
	if report.Baseline.TTFTP95MS <= 0 || report.Treatment.TTFTP95MS <= 0 {
		return ComparisonReport{}, fmt.Errorf("comparison requires successful TTFT samples in both arms")
	}
	report.TTFTP95DeltaPercent = percentDelta(report.Baseline.TTFTP95MS, report.Treatment.TTFTP95MS)
	baseline := pooledTTFT(baselineRuns)
	treatment := pooledTTFT(treatmentRuns)
	report.BootstrapLowPercent, report.BootstrapHighPercent = bootstrapP95DeltaCI(baseline, treatment, bootstrapSamples, seed)
	return report, nil
}

// CompareGatewayReportFiles adds accepted/completed/rejected and Little's Law
// evidence from the machine-readable benchmark reports. Invalid windows are
// counted but excluded from the arithmetic, never silently treated as zero.
func CompareGatewayReportFiles(baselinePaths, treatmentPaths []string) (GatewayComparisonArm, GatewayComparisonArm, error) {
	if len(baselinePaths) == 0 || len(treatmentPaths) == 0 {
		return GatewayComparisonArm{}, GatewayComparisonArm{}, fmt.Errorf("gateway comparison requires baseline and treatment reports")
	}
	baseline, err := loadGatewayReports(baselinePaths)
	if err != nil {
		return GatewayComparisonArm{}, GatewayComparisonArm{}, fmt.Errorf("baseline gateway reports: %w", err)
	}
	treatment, err := loadGatewayReports(treatmentPaths)
	if err != nil {
		return GatewayComparisonArm{}, GatewayComparisonArm{}, fmt.Errorf("treatment gateway reports: %w", err)
	}
	return summarizeGatewayReports(baseline), summarizeGatewayReports(treatment), nil
}

// Markdown renders a compact comparison suitable for tracked reports.
func (r ComparisonReport) Markdown() string {
	var output strings.Builder
	output.WriteString("# FishMesh benchmark comparison\n\n")
	fmt.Fprintf(&output, "- TTFT P95 delta: %.2f%%\n- Bootstrap 95%% CI: [%.2f%%, %.2f%%] (%d samples, seed %d)\n\n",
		r.TTFTP95DeltaPercent, r.BootstrapLowPercent, r.BootstrapHighPercent, r.BootstrapSamples, r.Seed)
	output.WriteString("| Arm | Runs | Success | Failed | P50 | P95 | P99 | Run median P95 | Static MAE | Static abs P95 | Learned MAE | Learned abs P95 | Agree | Paired learned-static |\n")
	output.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	writeComparisonArm := func(name string, arm ComparisonArm) {
		fmt.Fprintf(&output, "| %s | %d | %d | %d | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.1f%% | %.2f |\n",
			name, arm.Runs, arm.Succeeded, arm.Failed, arm.TTFTP50MS, arm.TTFTP95MS, arm.TTFTP99MS,
			arm.RunMedianTTFTP95MS, arm.EstimatorMAEMS, arm.EstimatorAbsoluteP95MS, arm.PredictionMAEMS,
			arm.PredictionAbsoluteP95MS, arm.PredictionAgreementPercent, arm.PairedPredictionMinusEstimatorMAEMS)
	}
	writeComparisonArm("baseline", r.Baseline)
	writeComparisonArm("treatment", r.Treatment)
	if r.BaselineGateway.Runs > 0 || r.TreatmentGateway.Runs > 0 {
		output.WriteString("\n## Gateway capacity evidence\n\n")
		output.WriteString("| Arm | Reports | Valid windows | Accepted QPS | Completed QPS | Rejection QPS | Average in-flight | Little's Law W ms |\n|---|---:|---:|---:|---:|---:|---:|---:|\n")
		fmt.Fprintf(&output, "| baseline | %d | %d | %.3f | %.3f | %.3f | %.3f | %.2f |\n", r.BaselineGateway.Runs, r.BaselineGateway.ValidRuns, r.BaselineGateway.AcceptedRateQPS, r.BaselineGateway.CompletedRateQPS, r.BaselineGateway.RejectionRateQPS, r.BaselineGateway.AverageInflight, r.BaselineGateway.LittleLawWaitMS)
		fmt.Fprintf(&output, "| treatment | %d | %d | %.3f | %.3f | %.3f | %.3f | %.2f |\n", r.TreatmentGateway.Runs, r.TreatmentGateway.ValidRuns, r.TreatmentGateway.AcceptedRateQPS, r.TreatmentGateway.CompletedRateQPS, r.TreatmentGateway.RejectionRateQPS, r.TreatmentGateway.AverageInflight, r.TreatmentGateway.LittleLawWaitMS)
	}
	return output.String()
}

func loadGatewayReports(paths []string) ([]BenchmarkReport, error) {
	reports := make([]BenchmarkReport, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var report BenchmarkReport
		if err := json.Unmarshal(body, &report); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func summarizeGatewayReports(reports []BenchmarkReport) GatewayComparisonArm {
	arm := GatewayComparisonArm{Runs: len(reports)}
	for _, report := range reports {
		if report.GatewayMetrics == nil || !report.GatewayMetrics.Valid {
			continue
		}
		arm.ValidRuns++
		arm.AcceptedRateQPS += report.GatewayMetrics.AcceptedRateQPS
		arm.CompletedRateQPS += report.GatewayMetrics.CompletedRateQPS
		arm.RejectionRateQPS += report.GatewayMetrics.RejectionRateQPS
		arm.AverageInflight += report.GatewayMetrics.AverageInflight
		arm.LittleLawWaitMS += report.GatewayMetrics.LittleLawWaitMS
	}
	if arm.ValidRuns == 0 {
		return arm
	}
	count := float64(arm.ValidRuns)
	arm.AcceptedRateQPS /= count
	arm.CompletedRateQPS /= count
	arm.RejectionRateQPS /= count
	arm.AverageInflight /= count
	arm.LittleLawWaitMS /= count
	return arm
}

func loadComparisonRuns(paths []string) ([][]BenchmarkAttempt, error) {
	runs := make([][]BenchmarkAttempt, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), maxComparisonJSONLLineBytes)
		var attempts []BenchmarkAttempt
		for scanner.Scan() {
			var envelope struct {
				RecordType string `json:"record_type"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
			if envelope.RecordType != "request" {
				continue
			}
			var attempt BenchmarkAttempt
			if err := json.Unmarshal(scanner.Bytes(), &attempt); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("decode request in %s: %w", path, err)
			}
			attempts = append(attempts, attempt)
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(attempts) == 0 {
			return nil, fmt.Errorf("%s has no request records", path)
		}
		runs = append(runs, attempts)
	}
	return runs, nil
}

func summarizeComparisonArm(runs [][]BenchmarkAttempt) ComparisonArm {
	arm := ComparisonArm{Runs: len(runs)}
	var ttfts, runP95, estimatorErrors, predictionErrors, pairedDeltas []float64
	for _, run := range runs {
		var current []float64
		for _, attempt := range run {
			if attempt.Error != "" || attempt.StatusCode < 200 || attempt.StatusCode >= 300 || attempt.TTFTMS <= 0 {
				arm.Failed++
				continue
			}
			arm.Succeeded++
			current = append(current, attempt.TTFTMS)
			ttfts = append(ttfts, attempt.TTFTMS)
			if attempt.Headers.EstimatorValid && attempt.Headers.EstimatedTTFTMS > 0 {
				estimatorErrors = append(estimatorErrors, math.Abs(attempt.TTFTMS-attempt.Headers.EstimatedTTFTMS))
			}
			if attempt.Headers.PredictionSelectedMS > 0 {
				arm.PredictionSamples++
				predictionErrors = append(predictionErrors, math.Abs(attempt.TTFTMS-attempt.Headers.PredictionSelectedMS))
				if attempt.Headers.PredictionWouldSelect == attempt.Headers.BackendID {
					arm.PredictionAgreementSamples++
				}
				if attempt.Headers.EstimatorValid && attempt.Headers.EstimatedTTFTMS > 0 {
					pairedDeltas = append(pairedDeltas, math.Abs(attempt.TTFTMS-attempt.Headers.PredictionSelectedMS)-math.Abs(attempt.TTFTMS-attempt.Headers.EstimatedTTFTMS))
				}
			}
		}
		if len(current) > 0 {
			runP95 = append(runP95, percentileCopy(current, 95))
		}
	}
	arm.TTFTP50MS, arm.TTFTP95MS, arm.TTFTP99MS = percentileCopy(ttfts, 50), percentileCopy(ttfts, 95), percentileCopy(ttfts, 99)
	arm.RunMedianTTFTP95MS = percentileCopy(runP95, 50)
	arm.EstimatorSamples = len(estimatorErrors)
	if len(estimatorErrors) > 0 {
		for _, value := range estimatorErrors {
			arm.EstimatorMAEMS += value
		}
		arm.EstimatorMAEMS /= float64(len(estimatorErrors))
		arm.EstimatorAbsoluteP95MS = percentileCopy(estimatorErrors, 95)
	}
	if len(predictionErrors) > 0 {
		for _, value := range predictionErrors {
			arm.PredictionMAEMS += value
		}
		arm.PredictionMAEMS /= float64(len(predictionErrors))
		arm.PredictionAbsoluteP95MS = percentileCopy(predictionErrors, 95)
		arm.PredictionAgreementPercent = float64(arm.PredictionAgreementSamples) / float64(len(predictionErrors)) * 100
	}
	arm.PairedSamples = len(pairedDeltas)
	if arm.PairedSamples > 0 {
		for _, value := range pairedDeltas {
			arm.PairedPredictionMinusEstimatorMAEMS += value
		}
		arm.PairedPredictionMinusEstimatorMAEMS /= float64(arm.PairedSamples)
	}
	return arm
}

func pooledTTFT(runs [][]BenchmarkAttempt) []float64 {
	var values []float64
	for _, run := range runs {
		for _, attempt := range run {
			if attempt.Error == "" && attempt.StatusCode >= 200 && attempt.StatusCode < 300 && attempt.TTFTMS > 0 {
				values = append(values, attempt.TTFTMS)
			}
		}
	}
	return values
}

func bootstrapP95DeltaCI(baseline, treatment []float64, samples int, seed int64) (float64, float64) {
	random := rand.New(rand.NewSource(seed))
	deltas := make([]float64, samples)
	baselineSample := make([]float64, len(baseline))
	treatmentSample := make([]float64, len(treatment))
	for sample := 0; sample < samples; sample++ {
		for index := range baselineSample {
			baselineSample[index] = baseline[random.Intn(len(baseline))]
		}
		for index := range treatmentSample {
			treatmentSample[index] = treatment[random.Intn(len(treatment))]
		}
		deltas[sample] = percentDelta(percentileCopy(baselineSample, 95), percentileCopy(treatmentSample, 95))
	}
	sort.Float64s(deltas)
	return quantile(deltas, 0.025), quantile(deltas, 0.975)
}

func percentileCopy(values []float64, point int) float64 {
	copyValues := append([]float64(nil), values...)
	return percentile(copyValues, point)
}

func percentDelta(baseline, treatment float64) float64 {
	if baseline == 0 {
		return 0
	}
	return (treatment - baseline) / baseline * 100
}

func quantile(sorted []float64, probability float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Round(probability * float64(len(sorted)-1)))
	return sorted[index]
}
