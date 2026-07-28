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

// Result is a single raw measurement. One JSON representation is emitted per request.
type Result struct {
	RecordType           string  `json:"record_type"`
	RequestNumber        int     `json:"request_number"`
	PrefixGroup          int     `json:"prefix_group"`
	PrefixKey            string  `json:"prefix_key"`
	StartedAt            string  `json:"started_at"`
	StatusCode           int     `json:"status_code"`
	TTFTMilliseconds     float64 `json:"ttft_ms,omitempty"`
	DurationMilliseconds float64 `json:"duration_ms"`
	Error                string  `json:"error,omitempty"`
}

type Summary struct {
	RecordType          string  `json:"record_type"`
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
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, ForceAttemptHTTP2: false}, Timeout: config.RequestTimeout}
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
	prefixGroup := requestNumber % config.PrefixGroups
	prefix := sharedPrefix(prefixGroup, config.PrefixBytes)
	prefixHash := sha256.Sum256([]byte(prefix))
	result := Result{RecordType: "request", RequestNumber: requestNumber, PrefixGroup: prefixGroup, PrefixKey: hex.EncodeToString(prefixHash[:16]), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
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
	request.Header.Set("Connection", "close")
	request.Header.Set("X-FishMesh-Prefix-Key", result.PrefixKey)

	response, err := client.Do(request)
	if err != nil {
		result.Error = err.Error()
		return finalize(result, startedAt)
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
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

func sharedPrefix(group, targetBytes int) string {
	seed := fmt.Sprintf("FishMesh benchmark corpus for shared prefix group %d. Treat this context as stable background knowledge for every request in this group. ", group)
	return strings.Repeat(seed, targetBytes/len(seed)+1)[:targetBytes]
}

func summarize(requested int, results []Result) Summary {
	summary := Summary{RecordType: "summary", Requested: requested, Completed: len(results)}
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
