package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendStreamsTextAndReturnsDecisionHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header = %q", request.Header.Get("Authorization"))
		}
		var body struct {
			CacheSalt string `json:"cache_salt"`
			IgnoreEOS bool   `json:"ignore_eos"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.CacheSalt != "isolated-salt" || !body.IgnoreEOS {
			t.Fatalf("cache salt body = %+v, err=%v", body, err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderKVStatus, "available")
		writer.Header().Set(HeaderCachedPrefixTokens, "0")
		writer.Header().Set(HeaderPromptTokens, "1024")
		writer.Header().Set(HeaderUncachedTokens, "256")
		writer.Header().Set(HeaderEstimatedTTFTMS, "42.5")
		writer.Header().Set(HeaderEstimatorValid, "true")
		writer.Header().Set(HeaderEstimatorConfidence, "calibrated")
		writer.Header().Set(HeaderEstimatorVersion, "profile-v1")
		writer.Header().Set(HeaderLocalDelta, "3")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	client, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second, APIKey: "test-key"}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	output := &bytes.Buffer{}
	result, err := client.Send(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}, CacheSalt: "isolated-salt", IgnoreEOS: true, StreamOutput: output})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if output.String() != "hello" || result.Text != "hello" || result.TTFT <= 0 {
		t.Fatalf("stream result = %+v, output=%q", result, output.String())
	}
	if result.Headers.KVStatus != "available" || result.Headers.CachedPrefixTokens != 0 {
		t.Fatalf("decision headers = %+v", result.Headers)
	}
	if result.Headers.PromptTokens != 1024 || result.Headers.UncachedTokens != 256 || result.Headers.EstimatedTTFTMS != 42.5 || !result.Headers.EstimatorValid || result.Headers.EstimatorVersion != "profile-v1" || result.Headers.LocalDelta != 3 {
		t.Fatalf("estimator headers = %+v", result.Headers)
	}
}

func TestSendKeepsUnavailableSeparateFromZeroMiss(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set(HeaderKVStatus, "match-unavailable")
		writer.Header().Set(HeaderCachedPrefixTokens, "0")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fallback\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	client, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.Send(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Headers.KVStatus != "match-unavailable" || result.HasCachedPrefixSample {
		t.Fatalf("unavailable was treated as a zero-miss sample: %+v", result)
	}
}

func TestSendRejectsIncompleteStreamAndNonSuccessResponse(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"incomplete": func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		},
		"upstream status": func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
		},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(handler)
			defer upstream.Close()
			client, err := New(Config{Endpoint: upstream.URL, Model: "qwen", RequestTimeout: time.Second}, Dependencies{HTTPClient: upstream.Client()})
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			if _, err := client.Send(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}

func TestConfigRejectsUnsafeOrInvalidInputs(t *testing.T) {
	for _, config := range []Config{
		{Endpoint: "not-a-url", Model: "qwen", RequestTimeout: time.Second},
		{Endpoint: "http://example.test", Model: "", RequestTimeout: time.Second},
		{Endpoint: "http://example.test", Model: "qwen", RequestTimeout: 0},
	} {
		if _, err := New(config, Dependencies{}); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
}

func TestResultFormatsFixedDecisionHeaders(t *testing.T) {
	headers := DecisionHeaders{Policy: "kv-aware-v1", RouteReason: "kv-aware", KVStatus: "available", CachedPrefixTokens: 32, BackendID: "backend-a"}
	text := headers.String()
	for _, want := range []string{"policy=kv-aware-v1", "cached_prefix_tokens=32", "backend_id=backend-a"} {
		if !strings.Contains(text, want) {
			t.Fatalf("headers string %q does not contain %q", text, want)
		}
	}
}
