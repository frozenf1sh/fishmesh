package loadgen

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunRecordsStreamingTTFT(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-FishMesh-Prefix-Key") == "" {
			t.Fatal("expected prefix key header")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-FishMesh-Route-Reason", "service-default")
		writer.Header().Set("X-FishMesh-Backend-ID", "service")
		writer.Header().Set("X-FishMesh-Preferred-Backend-ID", "service")
		writer.Header().Set("X-FishMesh-Policy", "service-v1")
		_, _ = writer.Write([]byte("data: {\"choices\":[{}]}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	var output bytes.Buffer
	summary, err := Run(context.Background(), Config{Endpoint: upstream.URL, Model: "test", Requests: 3, Concurrency: 2, PrefixGroups: 2, PrefixBytes: 256, MaxTokens: 4, RequestTimeout: time.Second}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Succeeded != 3 || summary.Failed != 0 || summary.TTFTP50Milliseconds <= 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"record_type":"run_metadata"`)) {
		t.Fatal("expected JSONL run metadata")
	}
	if !bytes.Contains(output.Bytes(), []byte(`"record_type":"summary"`)) {
		t.Fatal("expected JSONL summary")
	}
	if !bytes.Contains(output.Bytes(), []byte(`"route_reason":"service-default"`)) {
		t.Fatal("expected route reason in request record")
	}
	if !bytes.Contains(output.Bytes(), []byte(`"policy":"service-v1"`)) {
		t.Fatal("expected policy in request record")
	}
}

func TestPrefixGroupForDeterministicHotDistribution(t *testing.T) {
	config := Config{PrefixGroups: 4, HotPrefixRatio: 75}
	counts := make([]int, config.PrefixGroups)
	for requestNumber := 0; requestNumber < 100; requestNumber++ {
		counts[prefixGroupFor(config, requestNumber)]++
	}
	if counts[0] != 75 {
		t.Fatalf("hot prefix count = %d, want 75", counts[0])
	}
	if counts[1]+counts[2]+counts[3] != 25 {
		t.Fatalf("cold prefix count = %d, want 25", counts[1]+counts[2]+counts[3])
	}
}
