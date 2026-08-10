package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBenchmarkWritesAllAttemptsAndSeparatesUnavailable(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 2 {
			http.Error(writer, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderExactStatus, "available")
		writer.Header().Set(HeaderCachedPrefixTokens, "0")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	client, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	output := &bytes.Buffer{}
	summary, err := client.Benchmark(context.Background(), BenchmarkConfig{Mode: BenchmarkSharedPrefix, Requests: 3, Concurrency: 1, PrefixGroups: 1, PrefixBytes: 128, MaxTokens: 8, RequestTimeout: time.Second, RunID: "contract"}, output)
	if err != nil {
		t.Fatalf("benchmark: %v", err)
	}
	if summary.Completed != 3 || summary.Failed != 1 || summary.CachedPrefixSamples != 2 || summary.CachedPrefixSum != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if records := strings.Count(output.String(), `"record_type"`); records != 5 {
		t.Fatalf("JSONL records=%d, output=%s", records, output.String())
	}
}

func TestBenchmarkConfigModesAndLoadDiscipline(t *testing.T) {
	for _, mode := range []BenchmarkMode{BenchmarkUniform, BenchmarkSharedPrefix, BenchmarkHotPrefix, BenchmarkConversation} {
		config := BenchmarkConfig{Mode: mode, Requests: 2, Concurrency: defaultConcurrency, PrefixGroups: 2, PrefixBytes: minimumPrefixBytes, MaxTokens: defaultMaxTokens, RequestTimeout: time.Second}
		if err := config.Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	config := BenchmarkConfig{Mode: BenchmarkUniform, Requests: 1, Concurrency: defaultConcurrency + 1, PrefixGroups: 1, PrefixBytes: minimumPrefixBytes, MaxTokens: defaultMaxTokens, RequestTimeout: time.Second}
	if err := config.Validate(); err == nil {
		t.Fatal("high concurrency was accepted without explicit opt-in")
	}
	config.AllowHighConcurrency = true
	if err := config.Validate(); err != nil {
		t.Fatalf("explicit high concurrency opt-in rejected: %v", err)
	}
}
