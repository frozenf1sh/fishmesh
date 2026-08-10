package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	defaultRequests       = 200
	defaultConcurrency    = 4
	defaultPrefixGroups   = 8
	defaultPrefixBytes    = 4096
	minimumPrefixBytes    = 128
	defaultRequestTimeout = 90 * time.Second
)

// BenchmarkMode determines deterministic prompt/history generation without mutating any cluster-side state.
type BenchmarkMode string

const (
	BenchmarkUniform      BenchmarkMode = "uniform"
	BenchmarkSharedPrefix BenchmarkMode = "shared-prefix"
	BenchmarkHotPrefix    BenchmarkMode = "hot-prefix"
	BenchmarkConversation BenchmarkMode = "conversation"
)

// BenchmarkConfig bounds a client-side workload. It never owns route-mode switching, cache clearing, or rollout.
type BenchmarkConfig struct {
	RunID                string
	Mode                 BenchmarkMode
	Requests             int
	Concurrency          int
	PrefixGroups         int
	PrefixBytes          int
	HotPrefixRatio       int
	MaxTokens            int
	RequestTimeout       time.Duration
	AllowHighConcurrency bool
}

// BenchmarkResult is one append-only JSONL request record. It never stores prompt text or API credentials.
type BenchmarkResult struct {
	RecordType   string          `json:"record_type"`
	RunID        string          `json:"run_id"`
	Request      int             `json:"request"`
	Group        int             `json:"group"`
	StatusCode   int             `json:"status_code"`
	Headers      DecisionHeaders `json:"headers"`
	TTFTMS       float64         `json:"ttft_ms,omitempty"`
	DurationMS   float64         `json:"duration_ms"`
	Error        string          `json:"error,omitempty"`
	CachedSample bool            `json:"cached_prefix_sample"`
}

type benchmarkMetadata struct {
	RecordType   string        `json:"record_type"`
	RunID        string        `json:"run_id"`
	Mode         BenchmarkMode `json:"mode"`
	Requests     int           `json:"requests"`
	Concurrency  int           `json:"concurrency"`
	PrefixGroups int           `json:"prefix_groups"`
	PrefixBytes  int           `json:"prefix_bytes"`
	MaxTokens    int           `json:"max_tokens"`
}

// BenchmarkSummary aggregates completed request evidence without inferring unavailable state as a zero cache miss.
type BenchmarkSummary struct {
	RecordType          string  `json:"record_type"`
	RunID               string  `json:"run_id"`
	Requested           int     `json:"requested"`
	Completed           int     `json:"completed"`
	Succeeded           int     `json:"succeeded"`
	Failed              int     `json:"failed"`
	CachedPrefixSamples int     `json:"cached_prefix_samples"`
	CachedPrefixSum     int     `json:"cached_prefix_sum"`
	TTFTP50MS           float64 `json:"ttft_p50_ms"`
	TTFTP95MS           float64 `json:"ttft_p95_ms"`
}

// DefaultBenchmarkConfig returns the repository's low-risk profile defaults.
func DefaultBenchmarkConfig() BenchmarkConfig {
	return BenchmarkConfig{Mode: BenchmarkUniform, Requests: defaultRequests, Concurrency: defaultConcurrency, PrefixGroups: defaultPrefixGroups, PrefixBytes: defaultPrefixBytes, MaxTokens: defaultMaxTokens, RequestTimeout: defaultRequestTimeout}
}

// Validate rejects ambiguous modes and high GPU concurrency unless the user explicitly acknowledges it.
func (c BenchmarkConfig) Validate() error {
	switch c.Mode {
	case BenchmarkUniform, BenchmarkSharedPrefix, BenchmarkHotPrefix, BenchmarkConversation:
	default:
		return fmt.Errorf("unsupported benchmark mode %q", c.Mode)
	}
	if c.Requests <= 0 || c.Concurrency <= 0 || c.PrefixGroups <= 0 || c.PrefixBytes < minimumPrefixBytes || c.MaxTokens <= 0 || c.RequestTimeout <= 0 {
		return fmt.Errorf("benchmark requests, concurrency, groups, prefix bytes, max tokens and timeout are invalid")
	}
	if c.HotPrefixRatio < 0 || c.HotPrefixRatio > 100 {
		return fmt.Errorf("hot prefix ratio must be between 0 and 100")
	}
	if c.Concurrency > defaultConcurrency && !c.AllowHighConcurrency {
		return fmt.Errorf("concurrency above %d requires explicit allow-high-concurrency", defaultConcurrency)
	}
	return nil
}

// Benchmark executes a bounded deterministic workload and writes metadata, every request attempt, then a summary.
func (c *Client) Benchmark(ctx context.Context, config BenchmarkConfig, output io.Writer) (BenchmarkSummary, error) {
	if err := config.Validate(); err != nil {
		return BenchmarkSummary{}, err
	}
	if output == nil {
		return BenchmarkSummary{}, fmt.Errorf("benchmark output must not be nil")
	}
	if config.RunID == "" {
		config.RunID = time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	writer := bufio.NewWriter(output)
	if err := writeJSONL(writer, benchmarkMetadata{RecordType: "metadata", RunID: config.RunID, Mode: config.Mode, Requests: config.Requests, Concurrency: config.Concurrency, PrefixGroups: config.PrefixGroups, PrefixBytes: config.PrefixBytes, MaxTokens: config.MaxTokens}); err != nil {
		return BenchmarkSummary{}, err
	}

	type job struct{ request, group int }
	type attempt struct{ result BenchmarkResult }
	jobs := make(chan job)
	attempts := make(chan attempt, config.Concurrency)
	for worker := 0; worker < config.Concurrency; worker++ {
		go func() {
			for item := range jobs {
				messages := benchmarkMessages(config, item.request, item.group)
				result, err := c.Send(ctx, Request{Messages: messages, MaxTokens: config.MaxTokens})
				record := BenchmarkResult{RecordType: "request", RunID: config.RunID, Request: item.request, Group: item.group, StatusCode: result.StatusCode, Headers: result.Headers, TTFTMS: durationMS(result.TTFT), DurationMS: durationMS(result.Duration), CachedSample: result.HasCachedPrefixSample}
				if err != nil {
					record.Error = err.Error()
				}
				attempts <- attempt{result: record}
			}
		}()
	}
	go func() {
		for request := 0; request < config.Requests; request++ {
			jobs <- job{request: request, group: benchmarkGroup(config, request)}
		}
		close(jobs)
	}()

	summary := BenchmarkSummary{RecordType: "summary", RunID: config.RunID, Requested: config.Requests}
	ttfts := make([]float64, 0, config.Requests)
	for completed := 0; completed < config.Requests; completed++ {
		item := <-attempts
		record := item.result
		summary.Completed++
		if record.Error != "" {
			summary.Failed++
		} else {
			summary.Succeeded++
			if record.TTFTMS > 0 {
				ttfts = append(ttfts, record.TTFTMS)
			}
		}
		if record.CachedSample {
			summary.CachedPrefixSamples++
			summary.CachedPrefixSum += record.Headers.CachedPrefixTokens
		}
		if err := writeJSONL(writer, record); err != nil {
			return summary, err
		}
	}
	summary.TTFTP50MS = percentile(ttfts, 50)
	summary.TTFTP95MS = percentile(ttfts, 95)
	if err := writeJSONL(writer, summary); err != nil {
		return summary, err
	}
	if err := writer.Flush(); err != nil {
		return summary, fmt.Errorf("flush benchmark JSONL: %w", err)
	}
	return summary, nil
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

func benchmarkGroup(config BenchmarkConfig, request int) int {
	if config.Mode == BenchmarkHotPrefix && request%100 < config.HotPrefixRatio {
		return 0
	}
	return request % config.PrefixGroups
}

func benchmarkMessages(config BenchmarkConfig, request, group int) []Message {
	prefix := strings.Repeat("p", config.PrefixBytes)
	system := fmt.Sprintf("FishMesh benchmark shared prefix group=%d %s", group, prefix)
	unique := fmt.Sprintf("request=%d group=%d", request, group)
	switch config.Mode {
	case BenchmarkUniform:
		return []Message{{Role: RoleUser, Content: "FishMesh benchmark uniform " + unique + " " + prefix}}
	case BenchmarkConversation:
		return []Message{{Role: RoleSystem, Content: system}, {Role: RoleUser, Content: "previous question " + unique}, {Role: RoleAssistant, Content: "previous answer"}, {Role: RoleUser, Content: "current question " + unique}}
	default:
		return []Message{{Role: RoleSystem, Content: system}, {Role: RoleUser, Content: "FishMesh benchmark request " + unique}}
	}
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
