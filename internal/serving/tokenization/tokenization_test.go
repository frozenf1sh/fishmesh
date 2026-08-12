package tokenization

import (
	"errors"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	valid := testConfig("http://renderer.kubellm.svc:8000", "qwen")
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "relative URL", mutate: func(config *Config) { config.BaseURL = "renderer:8000" }},
		{name: "empty model", mutate: func(config *Config) { config.Model = "" }},
		{name: "zero timeout", mutate: func(config *Config) { config.Timeout = 0 }},
		{name: "zero request limit", mutate: func(config *Config) { config.MaxRequestBytes = 0 }},
		{name: "zero response limit", mutate: func(config *Config) { config.MaxResponseBytes = 0 }},
		{name: "zero token limit", mutate: func(config *Config) { config.MaxTotalTokens = 0 }},
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestInputRejectsUnknownRouteAndInvalidJSON(t *testing.T) {
	tests := []struct {
		name  string
		input Input
		code  ErrorCode
	}{
		{name: "unknown route", input: Input{Route: "/v1/embeddings", Body: []byte(`{}`)}, code: CodeUnsupportedRoute},
		{name: "invalid JSON", input: Input{Route: RouteChatCompletions, Body: []byte(`{`)}, code: CodeInvalidRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.input.Validate()
			requireErrorCode(t, err, test.code)
		})
	}
}

func TestResultDoesNotPublishMutableTokenSlice(t *testing.T) {
	source := []uint32{1, 2, 3}
	result := newResult("qwen", "tenant-a", [][]uint32{source})
	source[0] = 99

	prompts := result.Prompts()
	tokens := prompts[0].TokenIDs()
	tokens[1] = 88
	prompts[0] = Prompt{}

	if got := result.Prompts()[0].TokenIDs(); got[0] != 1 || got[1] != 2 {
		t.Fatalf("published result was mutated: %v", got)
	}
	if result.Model() != "qwen" || result.CacheSalt() != "tenant-a" || result.TotalTokens() != 3 {
		t.Fatalf("unexpected result metadata: model=%q salt=%q tokens=%d", result.Model(), result.CacheSalt(), result.TotalTokens())
	}
}

func TestTypedErrorPreservesCause(t *testing.T) {
	cause := errors.New("cause")
	err := &Error{Code: CodeUpstreamUnavailable, Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("typed error did not preserve its cause")
	}
	if err.Error() == "" {
		t.Fatal("typed error returned an empty message")
	}
}

func TestErrorIsTransientOnlyForRenderFailures(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want bool
	}{
		{name: "render timeout", err: &Error{Code: CodeUpstreamUnavailable}, want: true},
		{name: "upstream overload", err: &Error{Code: CodeUpstreamRejected, UpstreamStatus: 429}, want: true},
		{name: "upstream client error", err: &Error{Code: CodeUpstreamRejected, UpstreamStatus: 400}, want: false},
		{name: "invalid request", err: &Error{Code: CodeInvalidRequest}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.IsTransient(); got != test.want {
				t.Fatalf("IsTransient() = %t, want %t", got, test.want)
			}
		})
	}
}

func requireErrorCode(t *testing.T, err error, expected ErrorCode) *Error {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("expected typed tokenization error, got %T: %v", err, err)
	}
	if typed.Code != expected {
		t.Fatalf("expected error code %q, got %q: %v", expected, typed.Code, err)
	}
	return typed
}

func shortTimeoutConfig(baseURL string) Config {
	config := testConfig(baseURL, "qwen")
	config.Timeout = 20 * time.Millisecond
	return config
}

func testConfig(baseURL, model string) Config {
	return Config{
		BaseURL:          baseURL,
		Model:            model,
		Timeout:          5 * time.Second,
		MaxRequestBytes:  2 << 20,
		MaxResponseBytes: 8 << 20,
		MaxTotalTokens:   131072,
	}
}
