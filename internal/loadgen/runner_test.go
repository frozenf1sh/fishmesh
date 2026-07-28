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
	if !bytes.Contains(output.Bytes(), []byte(`"record_type":"summary"`)) {
		t.Fatal("expected JSONL summary")
	}
}
