package tokenization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVLLMRendererTokenizesChatRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != chatRenderPath {
			t.Errorf("unexpected render path %q", request.URL.Path)
		}
		if got := request.Header.Get(headerContentType); got != mediaTypeJSON {
			t.Errorf("unexpected content type %q", got)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if string(body[fieldModel]) != `"qwen"` || string(body["reasoning_effort"]) != `"low"` {
			t.Errorf("adapter did not preserve payload fields: %v", body)
		}
		writer.Header().Set(headerContentType, mediaTypeJSON)
		_, _ = writer.Write([]byte(`{"model":"qwen","token_ids":[1,2,3],"features":null}`))
	}))
	defer server.Close()

	renderer := newTestRenderer(t, testConfig(server.URL, "qwen"), server.Client())
	result, err := renderer.Tokenize(context.Background(), Input{
		Route: RouteChatCompletions,
		Body:  []byte(`{"model":"qwen","cache_salt":"tenant-a","messages":[],"reasoning_effort":"low"}`),
	})
	if err != nil {
		t.Fatalf("tokenize chat request: %v", err)
	}
	if result.Model() != "qwen" || result.CacheSalt() != "tenant-a" || result.TotalTokens() != 3 {
		t.Fatalf("unexpected result metadata: model=%q salt=%q tokens=%d", result.Model(), result.CacheSalt(), result.TotalTokens())
	}
	if got := result.Prompts()[0].TokenIDs(); fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("unexpected token IDs: %v", got)
	}
}

func TestVLLMRendererSupportsCompletionsBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != completionsRenderPath {
			t.Errorf("unexpected render path %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`[{"model":"qwen","token_ids":[7,8]},{"model":"qwen","token_ids":[9]}]`))
	}))
	defer server.Close()

	renderer := newTestRenderer(t, testConfig(server.URL, "qwen"), server.Client())
	result, err := renderer.Tokenize(context.Background(), Input{
		Route: RouteCompletions,
		Body:  []byte(`{"model":"qwen","prompt":["a","b"]}`),
	})
	if err != nil {
		t.Fatalf("tokenize completions request: %v", err)
	}
	if len(result.Prompts()) != 2 || result.TotalTokens() != 3 {
		t.Fatalf("unexpected batch result: prompts=%d tokens=%d", len(result.Prompts()), result.TotalTokens())
	}
}

func TestVLLMRendererRejectsModelMismatchBeforeIO(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	renderer := newTestRenderer(t, testConfig(server.URL, "qwen"), server.Client())
	_, err := renderer.Tokenize(context.Background(), Input{
		Route: RouteChatCompletions,
		Body:  []byte(`{"model":"another-model","messages":[]}`),
	})
	requireErrorCode(t, err, CodeInvalidRequest)
	if called {
		t.Fatal("model mismatch reached the render upstream")
	}
}

func TestVLLMRendererClassifiesInvalidResponses(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		configure func(*Config)
		code      ErrorCode
	}{
		{name: "model mismatch", response: `{"model":"other","token_ids":[1]}`, code: CodeInvalidResponse},
		{name: "missing token IDs", response: `{"model":"qwen"}`, code: CodeInvalidResponse},
		{name: "multimodal features", response: `{"model":"qwen","token_ids":[1],"features":{"mm_hashes":{"image":["hash"]}}}`, code: CodeUnsupportedFeature},
		{name: "token limit", response: `{"model":"qwen","token_ids":[1,2]}`, configure: func(config *Config) { config.MaxTotalTokens = 1 }, code: CodeTokenLimitExceeded},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()

			config := testConfig(server.URL, "qwen")
			if test.configure != nil {
				test.configure(&config)
			}
			renderer := newTestRenderer(t, config, server.Client())
			_, err := renderer.Tokenize(context.Background(), chatInput())
			requireErrorCode(t, err, test.code)
		})
	}
}

func TestVLLMRendererEnforcesHTTPBounds(t *testing.T) {
	t.Run("request body", func(t *testing.T) {
		config := testConfig("http://renderer.invalid", "qwen")
		config.MaxRequestBytes = 8
		renderer := newTestRenderer(t, config, http.DefaultClient)
		_, err := renderer.Tokenize(context.Background(), chatInput())
		requireErrorCode(t, err, CodeRequestTooLarge)
	})

	t.Run("response body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"model":"qwen","token_ids":[1]}`))
		}))
		defer server.Close()
		config := testConfig(server.URL, "qwen")
		config.MaxResponseBytes = 8
		renderer := newTestRenderer(t, config, server.Client())
		_, err := renderer.Tokenize(context.Background(), chatInput())
		requireErrorCode(t, err, CodeResponseTooLarge)
	})

	t.Run("upstream rejection", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, strings.Repeat("x", maxErrorBodyBytes+100), http.StatusServiceUnavailable)
		}))
		defer server.Close()
		renderer := newTestRenderer(t, testConfig(server.URL, "qwen"), server.Client())
		_, err := renderer.Tokenize(context.Background(), chatInput())
		typed := requireErrorCode(t, err, CodeUpstreamRejected)
		if typed.UpstreamStatus != http.StatusServiceUnavailable || len(err.Error()) > maxErrorBodyBytes+100 {
			t.Fatalf("unexpected bounded upstream error: status=%d length=%d", typed.UpstreamStatus, len(err.Error()))
		}
	})
}

func TestVLLMRendererPropagatesCancellationAndTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	t.Run("client cancellation", func(t *testing.T) {
		renderer := newTestRenderer(t, testConfig(server.URL, "qwen"), server.Client())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := renderer.Tokenize(ctx, chatInput())
		requireErrorCode(t, err, CodeUpstreamUnavailable)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was not preserved: %v", err)
		}
	})

	t.Run("adapter timeout", func(t *testing.T) {
		renderer := newTestRenderer(t, shortTimeoutConfig(server.URL), server.Client())
		_, err := renderer.Tokenize(context.Background(), chatInput())
		requireErrorCode(t, err, CodeUpstreamUnavailable)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline was not preserved: %v", err)
		}
	})
}

func TestNewVLLMRendererRequiresHTTPClient(t *testing.T) {
	_, err := NewVLLMRenderer(testConfig("http://renderer.invalid", "qwen"), Dependencies{})
	if err == nil {
		t.Fatal("nil HTTP client accepted")
	}
}

func TestVLLMRendererRejectsInvalidCacheSalt(t *testing.T) {
	renderer := newTestRenderer(t, testConfig("http://renderer.invalid", "qwen"), http.DefaultClient)
	_, err := renderer.Tokenize(context.Background(), Input{
		Route: RouteChatCompletions,
		Body:  []byte(`{"model":"qwen","cache_salt":42,"messages":[]}`),
	})
	requireErrorCode(t, err, CodeInvalidRequest)
}

func newTestRenderer(t *testing.T, config Config, client *http.Client) Tokenizer {
	t.Helper()
	renderer, err := NewVLLMRenderer(config, Dependencies{HTTPClient: client})
	if err != nil {
		t.Fatalf("construct renderer: %v", err)
	}
	return renderer
}

func chatInput() Input {
	return Input{Route: RouteChatCompletions, Body: []byte(`{"model":"qwen","messages":[]}`)}
}
