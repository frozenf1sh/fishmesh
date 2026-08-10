// Package client owns external OpenAI-compatible conversation, request evidence and bounded workload execution.
package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	HeaderRoutingMode        = "X-FishMesh-Routing-Mode"
	HeaderRouteReason        = "X-FishMesh-Route-Reason"
	HeaderBackendID          = "X-FishMesh-Backend-ID"
	HeaderPreferredBackendID = "X-FishMesh-Preferred-Backend-ID"
	HeaderPolicy             = "X-FishMesh-Policy"
	HeaderSpilloverReason    = "X-FishMesh-Spillover-Reason"
	HeaderExactStatus        = "X-FishMesh-Exact-Status"
	HeaderCachedPrefixTokens = "X-FishMesh-Cached-Prefix-Tokens"
	HeaderUpstream           = "X-FishMesh-Upstream"

	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Role is an OpenAI chat message role retained in local conversation history.
type Role string

// Message is an API-compatible text message. It deliberately excludes tool calls, media, API keys and response metadata.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Config contains only external-client request settings. The cmd composition root reads environment variables.
type Config struct {
	Endpoint       string
	Model          string
	MaxTokens      int
	RequestTimeout time.Duration
	APIKey         string
}

// Dependencies isolates HTTP transport for contract tests without importing Gateway internals.
type Dependencies struct {
	HTTPClient *http.Client
}

// Request is one external chat-completion request. StreamOutput receives only decoded assistant text.
type Request struct {
	Messages     []Message
	PrefixKey    string
	MaxTokens    int
	StreamOutput io.Writer
}

// DecisionHeaders is the allowlisted request provenance returned by FishMesh. It does not retain arbitrary headers.
type DecisionHeaders struct {
	RoutingMode        string `json:"routing_mode,omitempty"`
	RouteReason        string `json:"route_reason,omitempty"`
	BackendID          string `json:"backend_id,omitempty"`
	PreferredBackendID string `json:"preferred_backend_id,omitempty"`
	Policy             string `json:"policy,omitempty"`
	SpilloverReason    string `json:"spillover_reason,omitempty"`
	ExactStatus        string `json:"exact_status,omitempty"`
	CachedPrefixTokens int    `json:"cached_prefix_tokens"`
	Upstream           string `json:"upstream,omitempty"`
}

// Result is a completed request evidence record. HasCachedPrefixSample prevents unavailable state becoming a zero miss.
type Result struct {
	StatusCode            int             `json:"status_code"`
	Headers               DecisionHeaders `json:"headers"`
	Text                  string          `json:"text,omitempty"`
	TTFT                  time.Duration   `json:"ttft"`
	Duration              time.Duration   `json:"duration"`
	HasCachedPrefixSample bool            `json:"has_cached_prefix_sample"`
}

// Client performs bounded external HTTP/SSE chat requests.
type Client struct {
	config Config
	http   *http.Client
}

// New validates external-client settings without reading environment variables or creating hidden dependencies.
func New(config Config, dependencies Dependencies) (*Client, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("client endpoint must be an absolute HTTP URL: %q", config.Endpoint)
	}
	if strings.TrimSpace(config.Model) == "" || config.RequestTimeout <= 0 {
		return nil, fmt.Errorf("client model and request timeout must be set")
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = defaultMaxTokens
	}
	client := dependencies.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: config.RequestTimeout}
	}
	return &Client{config: config, http: client}, nil
}

// Send submits one streaming Chat Completions request and drains it through [DONE].
func (c *Client) Send(ctx context.Context, request Request) (Result, error) {
	if len(request.Messages) == 0 {
		return Result{}, fmt.Errorf("client request must contain at least one message")
	}

	// 1. Encode only caller-owned message values into the OpenAI request body.
	body, err := c.encodeRequest(request)
	if err != nil {
		return Result{}, err
	}

	// 2. Send the stream and capture only fixed FishMesh provenance headers.
	startedAt := time.Now()
	response, err := c.do(ctx, body, request.PrefixKey)
	if err != nil {
		return Result{Duration: time.Since(startedAt)}, err
	}
	defer response.Body.Close()
	result := Result{StatusCode: response.StatusCode, Headers: decisionHeaders(response.Header)}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Duration = time.Since(startedAt)
		return result, responseError(response)
	}

	// 3. Drain complete SSE output; a stream missing [DONE] is not a successful sample.
	text, ttft, err := consumeStream(response.Body, startedAt, request.StreamOutput)
	result.Text, result.TTFT, result.Duration = text, ttft, time.Since(startedAt)
	result.HasCachedPrefixSample = result.Headers.ExactStatus == exactStatusAvailable
	if err != nil {
		return result, err
	}
	return result, nil
}
