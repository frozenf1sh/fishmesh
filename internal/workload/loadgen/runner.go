package loadgen

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Stream      bool      `json:"stream"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
}

// RunMetadata is emitted as the first JSONL record. It makes every artifact
// self-describing and prevents a successful rerun from silently replacing a
// failed run without preserving the treatment and runtime provenance.
type RunMetadata struct {
	RecordType      string `json:"record_type"`
	RunID           string `json:"run_id"`
	StartedAt       string `json:"started_at"`
	GitSHA          string `json:"git_sha"`
	GatewayImage    string `json:"gateway_image"`
	VLLMVersion     string `json:"vllm_version"`
	ClusterProfile  string `json:"cluster_profile"`
	Endpoint        string `json:"endpoint"`
	Model           string `json:"model"`
	Requests        int    `json:"requests"`
	Concurrency     int    `json:"concurrency"`
	PrefixGroups    int    `json:"prefix_groups"`
	PrefixBytes     int    `json:"prefix_bytes"`
	PrefixNamespace string `json:"prefix_namespace"`
	HotPrefixRatio  int    `json:"hot_prefix_ratio"`
	MaxTokens       int    `json:"max_tokens"`
	RequestTimeout  string `json:"request_timeout"`
	KeepAlive       bool   `json:"keep_alive"`
}

// Result is a single raw measurement. One JSON representation is emitted per request.
type Result struct {
	RecordType           string  `json:"record_type"`
	RunID                string  `json:"run_id"`
	RequestNumber        int     `json:"request_number"`
	PrefixGroup          int     `json:"prefix_group"`
	PrefixKey            string  `json:"prefix_key"`
	StartedAt            string  `json:"started_at"`
	StatusCode           int     `json:"status_code"`
	RoutingMode          string  `json:"routing_mode,omitempty"`
	RouteReason          string  `json:"route_reason,omitempty"`
	BackendID            string  `json:"backend_id,omitempty"`
	SelectedUpstream     string  `json:"selected_upstream,omitempty"`
	TTFTMilliseconds     float64 `json:"ttft_ms,omitempty"`
	DurationMilliseconds float64 `json:"duration_ms"`
	Error                string  `json:"error,omitempty"`
}

type Summary struct {
	RecordType          string  `json:"record_type"`
	RunID               string  `json:"run_id"`
	Requested           int     `json:"requested"`
	Completed           int     `json:"completed"`
	Succeeded           int     `json:"succeeded"`
	Failed              int     `json:"failed"`
	TTFTP50Milliseconds float64 `json:"ttft_p50_ms"`
	TTFTP95Milliseconds float64 `json:"ttft_p95_ms"`
	TTFTP99Milliseconds float64 `json:"ttft_p99_ms"`
}

func Run(ctx context.Context, config Config, output io.Writer) (Summary, error) {
	if err := config.Validate(); err != nil {
		return Summary{}, err
	}
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives:   !config.KeepAlive,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}, Timeout: config.RequestTimeout}
	jobs := make(chan int)
	results := make(chan Result, config.Requests)
	var workers sync.WaitGroup
	for worker := 0; worker < config.Concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for requestNumber := range jobs {
				results <- execute(ctx, client, config, requestNumber)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for requestNumber := 0; requestNumber < config.Requests; requestNumber++ {
			select {
			case jobs <- requestNumber:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	writer := bufio.NewWriter(output)
	defer writer.Flush()
	if err := writeJSONL(writer, metadataFor(config)); err != nil {
		return Summary{}, fmt.Errorf("write run metadata: %w", err)
	}
	allResults := make([]Result, 0, config.Requests)
	for result := range results {
		allResults = append(allResults, result)
		if err := writeJSONL(writer, result); err != nil {
			return Summary{}, fmt.Errorf("write result: %w", err)
		}
	}
	summary := summarize(config.Requests, allResults)
	if err := writeJSONL(writer, summary); err != nil {
		return Summary{}, fmt.Errorf("write summary: %w", err)
	}
	if ctx.Err() != nil {
		return summary, ctx.Err()
	}
	return summary, nil
}

func execute(ctx context.Context, client *http.Client, config Config, requestNumber int) Result {
	prefixGroup := prefixGroupFor(config, requestNumber)
	prefix := sharedPrefix(config.PrefixNamespace, prefixGroup, config.PrefixBytes)
	prefixHash := sha256.Sum256([]byte(prefix))
	result := Result{RecordType: "request", RunID: runID(config), RequestNumber: requestNumber, PrefixGroup: prefixGroup, PrefixKey: hex.EncodeToString(prefixHash[:16]), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	startedAt := time.Now()
	payload, _ := json.Marshal(completionRequest{
		Model:    config.Model,
		Messages: []message{{Role: "system", Content: prefix}, {Role: "user", Content: fmt.Sprintf("Summarize group %d for request %d in one short sentence.", prefixGroup, requestNumber)}},
		Stream:   true, MaxTokens: config.MaxTokens, Temperature: 0,
	})
	requestContext, cancel := context.WithTimeout(ctx, config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, strings.TrimRight(config.Endpoint, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		result.Error = err.Error()
		return finalize(result, startedAt)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if !config.KeepAlive {
		request.Header.Set("Connection", "close")
	}
	request.Header.Set("X-FishMesh-Prefix-Key", result.PrefixKey)

	response, err := client.Do(request)
	if err != nil {
		result.Error = err.Error()
		return finalize(result, startedAt)
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	result.RoutingMode = response.Header.Get("X-FishMesh-Routing-Mode")
	result.RouteReason = response.Header.Get("X-FishMesh-Route-Reason")
	result.BackendID = response.Header.Get("X-FishMesh-Backend-ID")
	result.SelectedUpstream = response.Header.Get("X-FishMesh-Upstream")
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		result.Error = fmt.Sprintf("upstream status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		return finalize(result, startedAt)
	}
	if ttft, err := consumeSSE(response.Body, startedAt); err != nil {
		result.Error = err.Error()
	} else {
		result.TTFTMilliseconds = float64(ttft.Microseconds()) / 1000
	}
	return finalize(result, startedAt)
}

// prefixGroupFor produces a deterministic but dispersed workload distribution.
// A hot-prefix ratio of zero preserves the original round-robin workload. When
// enabled, group zero receives the requested percentage and the remaining groups
// share the rest. Arithmetic rather than randomness keeps runs reproducible.
func prefixGroupFor(config Config, requestNumber int) int {
	if config.PrefixGroups == 1 || config.HotPrefixRatio == 0 {
		return requestNumber % config.PrefixGroups
	}
	if config.HotPrefixRatio == 100 {
		return 0
	}
	bucket := (requestNumber * 37) % 100
	if bucket < config.HotPrefixRatio {
		return 0
	}
	return 1 + ((requestNumber * 17) % (config.PrefixGroups - 1))
}

// consumeSSE measures the first non-terminal event but drains the response through
// [DONE]. Draining keeps benchmark requests semantically complete and avoids measuring
// client-cancelled generations as successful samples.
func consumeSSE(reader io.Reader, startedAt time.Time) (time.Duration, error) {
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	var firstEventAt time.Time
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			if firstEventAt.IsZero() {
				return 0, fmt.Errorf("stream ended before first SSE event")
			}
			return firstEventAt.Sub(startedAt), nil
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now()
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("stream ended before terminal SSE event")
}

func finalize(result Result, startedAt time.Time) Result {
	result.DurationMilliseconds = float64(time.Since(startedAt).Microseconds()) / 1000
	return result
}

func sharedPrefix(namespace string, group, targetBytes int) string {
	seed := fmt.Sprintf("FishMesh benchmark corpus namespace %s shared prefix group %d. Treat this context as stable background knowledge for every request in this group. ", namespace, group)
	return strings.Repeat(seed, targetBytes/len(seed)+1)[:targetBytes]
}

func summarize(requested int, results []Result) Summary {
	run := "manual"
	if len(results) > 0 && results[0].RunID != "" {
		run = results[0].RunID
	}
	summary := Summary{RecordType: "summary", RunID: run, Requested: requested, Completed: len(results)}
	ttfts := make([]float64, 0, len(results))
	for _, result := range results {
		if result.Error != "" || result.StatusCode < 200 || result.StatusCode >= 300 || result.TTFTMilliseconds == 0 {
			summary.Failed++
			continue
		}
		summary.Succeeded++
		ttfts = append(ttfts, result.TTFTMilliseconds)
	}
	sort.Float64s(ttfts)
	summary.TTFTP50Milliseconds = percentile(ttfts, 0.50)
	summary.TTFTP95Milliseconds = percentile(ttfts, 0.95)
	summary.TTFTP99Milliseconds = percentile(ttfts, 0.99)
	return summary
}

func metadataFor(config Config) RunMetadata {
	return RunMetadata{
		RecordType: "run_metadata", RunID: runID(config), StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		GitSHA: valueOrUnknown(config.GitSHA), GatewayImage: valueOrUnknown(config.GatewayImage),
		VLLMVersion: valueOrUnknown(config.VLLMVersion), ClusterProfile: valueOrUnknown(config.ClusterProfile),
		Endpoint: config.Endpoint, Model: config.Model, Requests: config.Requests, Concurrency: config.Concurrency,
		PrefixGroups: config.PrefixGroups, PrefixBytes: config.PrefixBytes, PrefixNamespace: config.PrefixNamespace,
		HotPrefixRatio: config.HotPrefixRatio, MaxTokens: config.MaxTokens,
		RequestTimeout: config.RequestTimeout.String(), KeepAlive: config.KeepAlive,
	}
}

func runID(config Config) string {
	if value := strings.TrimSpace(config.RunID); value != "" {
		return value
	}
	return "manual"
}

func valueOrUnknown(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(quantile * float64(len(values)-1))
	return values[index]
}

func writeJSONL(writer io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}
