package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultBenchmarkConcurrency = 4
	defaultBenchmarkTimeoutMS   = 90_000
	defaultBatchPauseMS         = 250
	minimumPrefixBytes          = 128
)

// PrefixPattern controls how a scenario reuses prompt prefixes.
type PrefixPattern string

const (
	PrefixSame      PrefixPattern = "same-prefix"
	PrefixDifferent PrefixPattern = "different-prefix"
	PrefixMixed     PrefixPattern = "mixed-prefix"
)

// BenchmarkPlan describes one deterministic final pressure-test run.
// Prompt text is generated locally from this plan and is never written to the report.
type BenchmarkPlan struct {
	RunID                string              `json:"run_id,omitempty"`
	MaxTokens            int                 `json:"max_tokens"`
	RequestTimeoutMS     int                 `json:"request_timeout_ms"`
	AllowHighConcurrency bool                `json:"allow_high_concurrency"`
	Scenarios            []BenchmarkScenario `json:"scenarios"`
}

// BenchmarkScenario is one point in the length/prefix/quantity matrix.
// Batches are sequential; requests inside one batch may run concurrently.
type BenchmarkScenario struct {
	Name             string        `json:"name"`
	Pattern          PrefixPattern `json:"prefix_pattern"`
	PrefixBytes      int           `json:"prefix_bytes"`
	PrefixGroups     int           `json:"prefix_groups"`
	Batches          int           `json:"batches"`
	BatchSize        int           `json:"batch_size"`
	Concurrency      int           `json:"concurrency"`
	MixedHotRatio    int           `json:"mixed_hot_ratio,omitempty"`
	MixedUniqueRatio int           `json:"mixed_unique_ratio,omitempty"`
	BatchPauseMS     int           `json:"batch_pause_ms,omitempty"`
}

// BenchmarkAttempt is one request evidence record. It contains shape metadata, not prompt content.
type BenchmarkAttempt struct {
	RecordType   string          `json:"record_type"`
	RunID        string          `json:"run_id"`
	Scenario     string          `json:"scenario"`
	Pattern      PrefixPattern   `json:"prefix_pattern"`
	Batch        int             `json:"batch"`
	Request      int             `json:"request"`
	PrefixBytes  int             `json:"prefix_bytes"`
	PromptBytes  int             `json:"prompt_bytes"`
	PrefixGroup  string          `json:"prefix_group"`
	StatusCode   int             `json:"status_code"`
	Headers      DecisionHeaders `json:"headers"`
	TTFTMS       float64         `json:"ttft_ms,omitempty"`
	DurationMS   float64         `json:"duration_ms"`
	CachedSample bool            `json:"cached_prefix_sample"`
	Error        string          `json:"error,omitempty"`
}

// BenchmarkBatchReport summarizes one sequential batch.
type BenchmarkBatchReport struct {
	Batch               int            `json:"batch"`
	Requested           int            `json:"requested"`
	Completed           int            `json:"completed"`
	Succeeded           int            `json:"succeeded"`
	Failed              int            `json:"failed"`
	TTFTP50MS           float64        `json:"ttft_p50_ms"`
	TTFTP95MS           float64        `json:"ttft_p95_ms"`
	DurationP50MS       float64        `json:"duration_p50_ms"`
	DurationP95MS       float64        `json:"duration_p95_ms"`
	KVStatuses          map[string]int `json:"kv_statuses"`
	RouteReasons        map[string]int `json:"route_reasons"`
	CachedPrefixSamples int            `json:"cached_prefix_samples"`
	CachedPrefixSum     int            `json:"cached_prefix_sum"`
}

// BenchmarkScenarioReport summarizes one length/prefix/quantity point and its batches.
type BenchmarkScenarioReport struct {
	Name                string                 `json:"name"`
	Pattern             PrefixPattern          `json:"prefix_pattern"`
	PrefixBytes         int                    `json:"prefix_bytes"`
	PrefixGroups        int                    `json:"prefix_groups"`
	Requested           int                    `json:"requested"`
	Completed           int                    `json:"completed"`
	Succeeded           int                    `json:"succeeded"`
	Failed              int                    `json:"failed"`
	TTFTP50MS           float64                `json:"ttft_p50_ms"`
	TTFTP95MS           float64                `json:"ttft_p95_ms"`
	DurationP50MS       float64                `json:"duration_p50_ms"`
	DurationP95MS       float64                `json:"duration_p95_ms"`
	KVStatuses          map[string]int         `json:"kv_statuses"`
	RouteReasons        map[string]int         `json:"route_reasons"`
	Backends            map[string]int         `json:"backends"`
	CachedPrefixSamples int                    `json:"cached_prefix_samples"`
	CachedPrefixSum     int                    `json:"cached_prefix_sum"`
	Batches             []BenchmarkBatchReport `json:"batches"`
}

// BenchmarkReport is the machine-readable report generated after every plan run.
type BenchmarkReport struct {
	RecordType    string                    `json:"record_type"`
	RunID         string                    `json:"run_id"`
	StartedAt     time.Time                 `json:"started_at"`
	CompletedAt   time.Time                 `json:"completed_at"`
	Plan          BenchmarkPlan             `json:"plan"`
	Requested     int                       `json:"requested"`
	Completed     int                       `json:"completed"`
	Succeeded     int                       `json:"succeeded"`
	Failed        int                       `json:"failed"`
	TTFTP50MS     float64                   `json:"ttft_p50_ms"`
	TTFTP95MS     float64                   `json:"ttft_p95_ms"`
	DurationP50MS float64                   `json:"duration_p50_ms"`
	DurationP95MS float64                   `json:"duration_p95_ms"`
	Scenarios     []BenchmarkScenarioReport `json:"scenarios"`
}

// DefaultBenchmarkPlan returns a bounded matrix covering short/medium/long prompts and all prefix patterns.
func DefaultBenchmarkPlan() BenchmarkPlan {
	return BenchmarkPlan{
		MaxTokens:        DefaultMaxTokens,
		RequestTimeoutMS: defaultBenchmarkTimeoutMS,
		Scenarios: []BenchmarkScenario{
			{Name: "same-short", Pattern: PrefixSame, PrefixBytes: 256, PrefixGroups: 2, Batches: 2, BatchSize: 8, Concurrency: 2},
			{Name: "same-medium", Pattern: PrefixSame, PrefixBytes: 2048, PrefixGroups: 2, Batches: 3, BatchSize: 8, Concurrency: 2},
			{Name: "same-long", Pattern: PrefixSame, PrefixBytes: 8192, PrefixGroups: 1, Batches: 2, BatchSize: 12, Concurrency: 2},
			{Name: "different-short", Pattern: PrefixDifferent, PrefixBytes: 256, PrefixGroups: 1, Batches: 2, BatchSize: 8, Concurrency: 2},
			{Name: "different-medium", Pattern: PrefixDifferent, PrefixBytes: 2048, PrefixGroups: 1, Batches: 3, BatchSize: 8, Concurrency: 2},
			{Name: "different-long", Pattern: PrefixDifferent, PrefixBytes: 8192, PrefixGroups: 1, Batches: 2, BatchSize: 12, Concurrency: 2},
			{Name: "mixed-short", Pattern: PrefixMixed, PrefixBytes: 256, PrefixGroups: 4, Batches: 2, BatchSize: 8, Concurrency: 4, MixedHotRatio: 60, MixedUniqueRatio: 20},
			{Name: "mixed-medium", Pattern: PrefixMixed, PrefixBytes: 2048, PrefixGroups: 4, Batches: 3, BatchSize: 8, Concurrency: 4, MixedHotRatio: 60, MixedUniqueRatio: 20},
			{Name: "mixed-long", Pattern: PrefixMixed, PrefixBytes: 8192, PrefixGroups: 4, Batches: 2, BatchSize: 12, Concurrency: 4, MixedHotRatio: 60, MixedUniqueRatio: 20},
		},
	}
}

// LoadBenchmarkPlan reads a JSON plan and applies only safe zero-value defaults.
func LoadBenchmarkPlan(path string) (BenchmarkPlan, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return BenchmarkPlan{}, fmt.Errorf("read benchmark plan: %w", err)
	}
	var plan BenchmarkPlan
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return BenchmarkPlan{}, fmt.Errorf("decode benchmark plan: %w", err)
	}
	if plan.MaxTokens == 0 {
		plan.MaxTokens = DefaultMaxTokens
	}
	if plan.RequestTimeoutMS == 0 {
		plan.RequestTimeoutMS = defaultBenchmarkTimeoutMS
	}
	if plan.RunID == "" {
		plan.RunID = time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return plan, plan.Validate()
}

// Validate rejects unbounded or ambiguous final-test plans before any request is sent.
func (p BenchmarkPlan) Validate() error {
	if p.MaxTokens <= 0 || p.RequestTimeoutMS <= 0 || len(p.Scenarios) == 0 {
		return fmt.Errorf("benchmark max tokens, timeout and scenarios must be positive")
	}
	if p.RequestTimeoutMS > int((30*time.Minute)/time.Millisecond) {
		return fmt.Errorf("benchmark request timeout must not exceed 30 minutes")
	}
	seen := make(map[string]struct{}, len(p.Scenarios))
	for _, scenario := range p.Scenarios {
		if err := scenario.validate(p.AllowHighConcurrency); err != nil {
			return fmt.Errorf("scenario %q: %w", scenario.Name, err)
		}
		if _, ok := seen[scenario.Name]; ok {
			return fmt.Errorf("duplicate scenario name %q", scenario.Name)
		}
		seen[scenario.Name] = struct{}{}
	}
	return nil
}

// Validate rejects zero-sized batches and unsafe concurrency without an explicit acknowledgement.
func (s BenchmarkScenario) Validate() error {
	return s.validate(false)
}

func (s BenchmarkScenario) validate(allowHighConcurrency bool) error {
	if strings.TrimSpace(s.Name) == "" || s.PrefixBytes < minimumPrefixBytes || s.PrefixGroups <= 0 || s.Batches <= 0 || s.BatchSize <= 0 || s.Concurrency <= 0 {
		return fmt.Errorf("name, prefix bytes, groups, batches, batch size and concurrency are invalid")
	}
	switch s.Pattern {
	case PrefixSame, PrefixDifferent, PrefixMixed:
	default:
		return fmt.Errorf("unsupported prefix pattern %q", s.Pattern)
	}
	if s.Concurrency > defaultBenchmarkConcurrency && !allowHighConcurrency {
		return fmt.Errorf("scenario concurrency %d exceeds safe default %d; set allow_high_concurrency in the plan", s.Concurrency, defaultBenchmarkConcurrency)
	}
	if s.Pattern == PrefixMixed && (s.MixedHotRatio < 0 || s.MixedUniqueRatio < 0 || s.MixedHotRatio+s.MixedUniqueRatio > 100) {
		return fmt.Errorf("mixed prefix ratios must be non-negative and sum to at most 100")
	}
	if s.BatchPauseMS < 0 {
		return fmt.Errorf("batch pause must not be negative")
	}
	return nil
}

// Markdown renders the report as a compact human-readable summary.
func (r BenchmarkReport) Markdown() string {
	var output strings.Builder
	fmt.Fprintf(&output, "# FishMesh benchmark report\n\n- Run: `%s`\n- Requests: %d (success %d, failed %d)\n- TTFT P50/P95: %.2f / %.2f ms\n- Duration P50/P95: %.2f / %.2f ms\n\n", r.RunID, r.Requested, r.Succeeded, r.Failed, r.TTFTP50MS, r.TTFTP95MS, r.DurationP50MS, r.DurationP95MS)
	output.WriteString("| Scenario | Pattern | Prefix bytes | Requests | Success | TTFT P50 | TTFT P95 | Cached samples | Cached tokens |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, scenario := range r.Scenarios {
		fmt.Fprintf(&output, "| %s | %s | %d | %d | %d | %.2f | %.2f | %d | %d |\n", scenario.Name, scenario.Pattern, scenario.PrefixBytes, scenario.Requested, scenario.Succeeded, scenario.TTFTP50MS, scenario.TTFTP95MS, scenario.CachedPrefixSamples, scenario.CachedPrefixSum)
	}
	output.WriteString("\nUnavailable KV status is reported separately from an available zero-token cache miss. Prompt text and API credentials are not included.\n")
	return output.String()
}
