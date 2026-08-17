package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	defaultBenchmarkConcurrency = 4
	defaultBenchmarkTimeoutMS   = 90_000
	defaultBatchPauseMS         = 250
	minimumPrefixBytes          = 128
	maximumPrefixBytes          = 1 << 20
	maximumWarmupRequests       = 64
	maximumRunNonceLength       = 128
)

// PrefixPattern controls how a scenario reuses prompt prefixes.
type PrefixPattern string

const (
	PrefixSame      PrefixPattern = "same-prefix"
	PrefixDifferent PrefixPattern = "different-prefix"
	PrefixMixed     PrefixPattern = "mixed-prefix"
)

// CacheMode defines the KV-cache isolation contract for a benchmark run.
type CacheMode string

const (
	CacheCold           CacheMode = "cold"
	CacheControlledWarm CacheMode = "controlled-warm"
	CacheSteadyWarm     CacheMode = "steady-warm"
)

// BenchmarkProvenance identifies the runtime used by a formal experiment without retaining request data.
type BenchmarkProvenance struct {
	GitSHA           string   `json:"git_sha,omitempty"`
	GatewayImage     string   `json:"gateway_image,omitempty"`
	GatewayPods      []string `json:"gateway_pods,omitempty"`
	VLLMVersion      string   `json:"vllm_version,omitempty"`
	Model            string   `json:"model,omitempty"`
	ConfigDigest     string   `json:"config_digest,omitempty"`
	EstimatorProfile string   `json:"estimator_profile,omitempty"`
}

// BenchmarkPlan describes one deterministic final pressure-test run.
// Prompt text is generated locally from this plan and is never written to the report.
type BenchmarkPlan struct {
	RunID                   string              `json:"run_id,omitempty"`
	MaxTokens               int                 `json:"max_tokens"`
	IgnoreEOS               bool                `json:"ignore_eos,omitempty"`
	RequestTimeoutMS        int                 `json:"request_timeout_ms"`
	AllowHighConcurrency    bool                `json:"allow_high_concurrency"`
	Formal                  bool                `json:"formal,omitempty"`
	SkipPromptTokenEvidence bool                `json:"skip_prompt_token_evidence,omitempty"`
	Treatment               string              `json:"treatment,omitempty"`
	WorkloadSeed            int64               `json:"workload_seed,omitempty"`
	RandomizeRequestOrder   bool                `json:"randomize_request_order,omitempty"`
	ExecutionOrder          []string            `json:"execution_order,omitempty"`
	CacheMode               CacheMode           `json:"cache_mode,omitempty"`
	RunNonce                string              `json:"run_nonce,omitempty"`
	CacheGeneration         string              `json:"cache_generation,omitempty"`
	Provenance              BenchmarkProvenance `json:"provenance,omitempty"`
	Scenarios               []BenchmarkScenario `json:"scenarios"`
}

// BenchmarkScenario is one point in the length/prefix/quantity matrix.
// Batches are sequential; requests inside one batch may run concurrently.
type BenchmarkScenario struct {
	Name                  string        `json:"name"`
	Pattern               PrefixPattern `json:"prefix_pattern"`
	PrefixBytes           int           `json:"prefix_bytes"`
	PrefixGroups          int           `json:"prefix_groups"`
	ConversationTurnBytes int           `json:"conversation_turn_bytes,omitempty"`
	Batches               int           `json:"batches"`
	BatchSize             int           `json:"batch_size"`
	Concurrency           int           `json:"concurrency"`
	ArrivalRateQPS        float64       `json:"arrival_rate_qps,omitempty"`
	MixedHotRatio         int           `json:"mixed_hot_ratio,omitempty"`
	MixedUniqueRatio      int           `json:"mixed_unique_ratio,omitempty"`
	BatchPauseMS          int           `json:"batch_pause_ms,omitempty"`
	WarmupRequests        int           `json:"warmup_requests,omitempty"`
	TargetPromptTokens    int           `json:"target_prompt_tokens,omitempty"`
	PromptTokenTolerance  int           `json:"prompt_token_tolerance,omitempty"`
}

// BenchmarkAttempt is one request evidence record. It contains shape metadata, not prompt content.
type BenchmarkAttempt struct {
	RecordType           string          `json:"record_type"`
	RunID                string          `json:"run_id"`
	Scenario             string          `json:"scenario"`
	Pattern              PrefixPattern   `json:"prefix_pattern"`
	Batch                int             `json:"batch"`
	Request              int             `json:"request"`
	InputSequence        int             `json:"input_sequence"`
	PrefixBytes          int             `json:"prefix_bytes"`
	PromptBytes          int             `json:"prompt_bytes"`
	TargetPromptTokens   int             `json:"target_prompt_tokens,omitempty"`
	PromptTokenTolerance int             `json:"prompt_token_tolerance,omitempty"`
	PromptTokenDelta     int             `json:"prompt_token_delta,omitempty"`
	PrefixGroup          string          `json:"prefix_group"`
	CacheMode            CacheMode       `json:"cache_mode,omitempty"`
	CacheScope           string          `json:"cache_scope,omitempty"`
	CacheGeneration      string          `json:"cache_generation,omitempty"`
	StartedAt            time.Time       `json:"started_at"`
	CompletedAt          time.Time       `json:"completed_at"`
	StatusCode           int             `json:"status_code"`
	Headers              DecisionHeaders `json:"headers"`
	TTFTMS               float64         `json:"ttft_ms,omitempty"`
	DurationMS           float64         `json:"duration_ms"`
	CachedSample         bool            `json:"cached_prefix_sample"`
	Error                string          `json:"error,omitempty"`
}

// BenchmarkBatchReport summarizes one sequential batch.
type BenchmarkBatchReport struct {
	Batch               int                   `json:"batch"`
	WindowStartedAt     time.Time             `json:"window_started_at,omitempty"`
	WindowCompletedAt   time.Time             `json:"window_completed_at,omitempty"`
	ElapsedMS           float64               `json:"elapsed_ms,omitempty"`
	CompletionRateQPS   float64               `json:"completion_rate_qps,omitempty"`
	Requested           int                   `json:"requested"`
	Completed           int                   `json:"completed"`
	Succeeded           int                   `json:"succeeded"`
	Failed              int                   `json:"failed"`
	TTFTP50MS           float64               `json:"ttft_p50_ms"`
	TTFTP95MS           float64               `json:"ttft_p95_ms"`
	DurationP50MS       float64               `json:"duration_p50_ms"`
	DurationP95MS       float64               `json:"duration_p95_ms"`
	KVStatuses          map[string]int        `json:"kv_statuses"`
	RouteReasons        map[string]int        `json:"route_reasons"`
	CachedPrefixSamples int                   `json:"cached_prefix_samples"`
	CachedPrefixSum     int                   `json:"cached_prefix_sum"`
	GatewayMetrics      *GatewayMetricsWindow `json:"gateway_metrics,omitempty"`
}

// BenchmarkScenarioReport summarizes one length/prefix/quantity point and its batches.
type BenchmarkScenarioReport struct {
	Name                  string                 `json:"name"`
	Pattern               PrefixPattern          `json:"prefix_pattern"`
	PrefixBytes           int                    `json:"prefix_bytes"`
	PrefixGroups          int                    `json:"prefix_groups"`
	ArrivalRateQPS        float64                `json:"arrival_rate_qps,omitempty"`
	WindowStartedAt       time.Time              `json:"window_started_at,omitempty"`
	WindowCompletedAt     time.Time              `json:"window_completed_at,omitempty"`
	ElapsedMS             float64                `json:"elapsed_ms,omitempty"`
	CompletionRateQPS     float64                `json:"completion_rate_qps,omitempty"`
	TargetPromptTokens    int                    `json:"target_prompt_tokens,omitempty"`
	PromptTokenTolerance  int                    `json:"prompt_token_tolerance,omitempty"`
	ActualPromptTokenMin  int                    `json:"actual_prompt_token_min,omitempty"`
	ActualPromptTokenP50  int                    `json:"actual_prompt_token_p50,omitempty"`
	ActualPromptTokenP95  int                    `json:"actual_prompt_token_p95,omitempty"`
	ActualPromptTokenMax  int                    `json:"actual_prompt_token_max,omitempty"`
	PromptTokenMissing    int                    `json:"prompt_token_missing,omitempty"`
	PromptTokenViolations int                    `json:"prompt_token_violations,omitempty"`
	Requested             int                    `json:"requested"`
	Completed             int                    `json:"completed"`
	Succeeded             int                    `json:"succeeded"`
	Failed                int                    `json:"failed"`
	TTFTP50MS             float64                `json:"ttft_p50_ms"`
	TTFTP95MS             float64                `json:"ttft_p95_ms"`
	DurationP50MS         float64                `json:"duration_p50_ms"`
	DurationP95MS         float64                `json:"duration_p95_ms"`
	KVStatuses            map[string]int         `json:"kv_statuses"`
	RouteReasons          map[string]int         `json:"route_reasons"`
	Backends              map[string]int         `json:"backends"`
	CachedPrefixSamples   int                    `json:"cached_prefix_samples"`
	CachedPrefixSum       int                    `json:"cached_prefix_sum"`
	Batches               []BenchmarkBatchReport `json:"batches"`
	GatewayMetrics        *GatewayMetricsWindow  `json:"gateway_metrics,omitempty"`
}

// BenchmarkReport is the machine-readable report generated after every plan run.
type BenchmarkReport struct {
	RecordType            string                    `json:"record_type"`
	RunID                 string                    `json:"run_id"`
	StartedAt             time.Time                 `json:"started_at"`
	CompletedAt           time.Time                 `json:"completed_at"`
	ElapsedMS             float64                   `json:"elapsed_ms,omitempty"`
	CompletionRateQPS     float64                   `json:"completion_rate_qps,omitempty"`
	Plan                  BenchmarkPlan             `json:"plan"`
	Requested             int                       `json:"requested"`
	Completed             int                       `json:"completed"`
	Succeeded             int                       `json:"succeeded"`
	Failed                int                       `json:"failed"`
	TTFTP50MS             float64                   `json:"ttft_p50_ms"`
	TTFTP95MS             float64                   `json:"ttft_p95_ms"`
	DurationP50MS         float64                   `json:"duration_p50_ms"`
	DurationP95MS         float64                   `json:"duration_p95_ms"`
	PromptTokenMissing    int                       `json:"prompt_token_missing,omitempty"`
	PromptTokenViolations int                       `json:"prompt_token_violations,omitempty"`
	GatewayMetrics        *GatewayMetricsWindow     `json:"gateway_metrics,omitempty"`
	Scenarios             []BenchmarkScenarioReport `json:"scenarios"`
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
	plan, err = PrepareBenchmarkPlan(plan)
	return plan, err
}

// PrepareBenchmarkPlan derives and records deterministic scenario order before artifacts are written.
func PrepareBenchmarkPlan(plan BenchmarkPlan) (BenchmarkPlan, error) {
	if len(plan.ExecutionOrder) == 0 {
		indices := make([]int, len(plan.Scenarios))
		for index := range indices {
			indices[index] = index
		}
		if plan.WorkloadSeed != 0 {
			rand.New(rand.NewSource(plan.WorkloadSeed)).Shuffle(len(indices), func(i, j int) {
				indices[i], indices[j] = indices[j], indices[i]
			})
		}
		plan.ExecutionOrder = make([]string, 0, len(indices))
		for _, index := range indices {
			plan.ExecutionOrder = append(plan.ExecutionOrder, plan.Scenarios[index].Name)
		}
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
	if len(p.ExecutionOrder) != 0 && len(p.ExecutionOrder) != len(p.Scenarios) {
		return fmt.Errorf("execution order must contain every scenario exactly once")
	}
	switch p.CacheMode {
	case "":
	case CacheCold, CacheControlledWarm, CacheSteadyWarm:
		if p.WorkloadSeed == 0 || strings.TrimSpace(p.Treatment) == "" || strings.TrimSpace(p.RunNonce) == "" || strings.TrimSpace(p.CacheGeneration) == "" {
			return fmt.Errorf("isolated benchmark requires workload seed, treatment, run nonce and cache generation")
		}
		if len(p.RunNonce) > maximumRunNonceLength {
			return fmt.Errorf("run nonce exceeds %d bytes", maximumRunNonceLength)
		}
	default:
		return fmt.Errorf("unsupported cache mode %q", p.CacheMode)
	}
	if p.Formal {
		if p.SkipPromptTokenEvidence {
			return fmt.Errorf("formal benchmark must not skip prompt token evidence")
		}
		if p.CacheMode == "" {
			return fmt.Errorf("formal benchmark requires an explicit cache mode")
		}
		if err := p.Provenance.validate(); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{}, len(p.Scenarios))
	for _, scenario := range p.Scenarios {
		if err := scenario.validate(p.AllowHighConcurrency, p.CacheMode); err != nil {
			return fmt.Errorf("scenario %q: %w", scenario.Name, err)
		}
		if _, ok := seen[scenario.Name]; ok {
			return fmt.Errorf("duplicate scenario name %q", scenario.Name)
		}
		seen[scenario.Name] = struct{}{}
	}
	orderSeen := make(map[string]struct{}, len(p.ExecutionOrder))
	for _, name := range p.ExecutionOrder {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("execution order contains unknown scenario %q", name)
		}
		if _, ok := orderSeen[name]; ok {
			return fmt.Errorf("execution order repeats scenario %q", name)
		}
		orderSeen[name] = struct{}{}
	}
	return nil
}

func (p BenchmarkProvenance) validate() error {
	if strings.TrimSpace(p.GitSHA) == "" || strings.TrimSpace(p.GatewayImage) == "" || len(p.GatewayPods) == 0 ||
		strings.TrimSpace(p.VLLMVersion) == "" || strings.TrimSpace(p.Model) == "" || strings.TrimSpace(p.ConfigDigest) == "" ||
		strings.TrimSpace(p.EstimatorProfile) == "" {
		return fmt.Errorf("formal benchmark requires git, image, pods, vLLM, model, config and estimator provenance")
	}
	if slices.ContainsFunc(p.GatewayPods, func(value string) bool { return strings.TrimSpace(value) == "" }) {
		return fmt.Errorf("formal benchmark gateway pod provenance must not be empty")
	}
	return nil
}

// Validate rejects zero-sized batches and unsafe concurrency without an explicit acknowledgement.
func (s BenchmarkScenario) Validate() error {
	return s.validate(false, "")
}

func (s BenchmarkScenario) validate(allowHighConcurrency bool, cacheMode CacheMode) error {
	if strings.TrimSpace(s.Name) == "" || s.PrefixBytes < minimumPrefixBytes || s.PrefixBytes > maximumPrefixBytes || s.PrefixGroups <= 0 || s.Batches <= 0 || s.BatchSize <= 0 || s.Concurrency <= 0 {
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
	if s.ConversationTurnBytes < 0 || s.ConversationTurnBytes > maximumPrefixBytes {
		return fmt.Errorf("conversation turn bytes must be between 0 and %d", maximumPrefixBytes)
	}
	if s.ConversationTurnBytes > 0 && (s.Pattern != PrefixSame || s.PrefixGroups != s.BatchSize) {
		return fmt.Errorf("conversation ladder requires same-prefix and prefix groups equal batch size")
	}
	if s.BatchPauseMS < 0 || math.IsNaN(s.ArrivalRateQPS) || math.IsInf(s.ArrivalRateQPS, 0) || s.ArrivalRateQPS < 0 {
		return fmt.Errorf("batch pause and arrival rate must not be negative or non-finite")
	}
	if s.WarmupRequests < 0 || s.WarmupRequests > maximumWarmupRequests {
		return fmt.Errorf("warmup requests must be between 0 and %d", maximumWarmupRequests)
	}
	if cacheMode == CacheCold && s.WarmupRequests != 0 {
		return fmt.Errorf("cold cache mode cannot declare warmup requests")
	}
	if cacheMode == CacheControlledWarm && s.WarmupRequests == 0 {
		return fmt.Errorf("controlled-warm cache mode requires warmup requests")
	}
	if s.TargetPromptTokens < 0 || s.PromptTokenTolerance < 0 || (s.TargetPromptTokens == 0 && s.PromptTokenTolerance != 0) ||
		(s.TargetPromptTokens > 0 && (s.PromptTokenTolerance == 0 || s.PromptTokenTolerance >= s.TargetPromptTokens)) {
		return fmt.Errorf("prompt token target requires a positive tolerance smaller than the target")
	}
	return nil
}

// Markdown renders the report as a compact human-readable summary.
func (r BenchmarkReport) Markdown() string {
	var output strings.Builder
	fmt.Fprintf(&output, "# FishMesh benchmark report\n\n- Run: `%s`\n- Cache mode/generation: `%s` / `%s`\n- Workload seed: `%d`\n- Requests: %d (success %d, failed %d)\n- TTFT P50/P95: %.2f / %.2f ms\n- Duration P50/P95: %.2f / %.2f ms\n- Prompt-token missing/violations: %d / %d\n\n", r.RunID, r.Plan.CacheMode, r.Plan.CacheGeneration, r.Plan.WorkloadSeed, r.Requested, r.Succeeded, r.Failed, r.TTFTP50MS, r.TTFTP95MS, r.DurationP50MS, r.DurationP95MS, r.PromptTokenMissing, r.PromptTokenViolations)
	if r.GatewayMetrics != nil {
		if r.GatewayMetrics.Valid {
			fmt.Fprintf(&output, "- Gateway metrics: admitted %.3f QPS, completed %.3f QPS, admission rejects %.3f QPS, average in-flight %.3f, Little's Law W %.2f ms, warmup requests planned/excluded %d\n", r.GatewayMetrics.AcceptedRateQPS, r.GatewayMetrics.CompletedRateQPS, r.GatewayMetrics.RejectionRateQPS, r.GatewayMetrics.AverageInflight, r.GatewayMetrics.LittleLawWaitMS, r.GatewayMetrics.WarmupRequests)
			if r.GatewayMetrics.MemoryMetricsValid {
				fmt.Fprintf(&output, "- Gateway memory: RSS start/peak/end %.2f/%.2f/%.2f MiB (delta %.2f MiB), Go heap start/peak/end %.2f/%.2f/%.2f MiB (delta %.2f MiB)\n", bytesToMiB(r.GatewayMetrics.ResidentMemoryStartBytes), bytesToMiB(r.GatewayMetrics.ResidentMemoryPeakBytes), bytesToMiB(r.GatewayMetrics.ResidentMemoryEndBytes), bytesToMiB(r.GatewayMetrics.ResidentMemoryDeltaBytes), bytesToMiB(r.GatewayMetrics.HeapAllocStartBytes), bytesToMiB(r.GatewayMetrics.HeapAllocPeakBytes), bytesToMiB(r.GatewayMetrics.HeapAllocEndBytes), bytesToMiB(r.GatewayMetrics.HeapAllocDeltaBytes))
			}
		} else {
			fmt.Fprintf(&output, "- Gateway metrics: unavailable (%s)\n", r.GatewayMetrics.Error)
		}
	}
	output.WriteString("\n")
	output.WriteString("| Scenario | Pattern | Arrival QPS | Target tokens | Actual P50/P95 | Prefix bytes | Requests | Success | TTFT P50 | TTFT P95 | Cached samples | Cached tokens |\n|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, scenario := range r.Scenarios {
		fmt.Fprintf(&output, "| %s | %s | %.3f | %d | %d/%d | %d | %d | %d | %.2f | %.2f | %d | %d |\n", scenario.Name, scenario.Pattern, scenario.ArrivalRateQPS, scenario.TargetPromptTokens, scenario.ActualPromptTokenP50, scenario.ActualPromptTokenP95, scenario.PrefixBytes, scenario.Requested, scenario.Succeeded, scenario.TTFTP50MS, scenario.TTFTP95MS, scenario.CachedPrefixSamples, scenario.CachedPrefixSum)
		if scenario.GatewayMetrics != nil && scenario.GatewayMetrics.Valid {
			fmt.Fprintf(&output, "  - Gateway %s: accepted %.3f QPS, completed %.3f QPS, rejected %.3f QPS, average in-flight %.3f, Little's Law W %.2f ms\n", scenario.Name, scenario.GatewayMetrics.AcceptedRateQPS, scenario.GatewayMetrics.CompletedRateQPS, scenario.GatewayMetrics.RejectionRateQPS, scenario.GatewayMetrics.AverageInflight, scenario.GatewayMetrics.LittleLawWaitMS)
		}
	}
	output.WriteString("\nUnavailable KV status is reported separately from an available zero-token cache miss. Prompt text and API credentials are not included.\n")
	return output.String()
}

func bytesToMiB(value float64) float64 {
	return value / (1024 * 1024)
}
