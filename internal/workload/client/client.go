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
	HeaderRoutingMode             = "X-FishMesh-Routing-Mode"
	HeaderRouteReason             = "X-FishMesh-Route-Reason"
	HeaderBackendID               = "X-FishMesh-Backend-ID"
	HeaderPreferredBackendID      = "X-FishMesh-Preferred-Backend-ID"
	HeaderPolicy                  = "X-FishMesh-Policy"
	HeaderSpilloverReason         = "X-FishMesh-Spillover-Reason"
	HeaderKVStatus                = "X-FishMesh-KV-Status"
	HeaderCachedPrefixTokens      = "X-FishMesh-Cached-Prefix-Tokens"
	HeaderPromptTokens            = "X-FishMesh-Prompt-Tokens"
	HeaderUncachedTokens          = "X-FishMesh-Uncached-Tokens"
	HeaderEstimatedTTFTMS         = "X-FishMesh-Estimated-TTFT-Ms"
	HeaderEstimatorValid          = "X-FishMesh-Estimator-Valid"
	HeaderEstimatorConfidence     = "X-FishMesh-Estimator-Confidence"
	HeaderEstimatorVersion        = "X-FishMesh-Estimator-Version"
	HeaderEstimatorReason         = "X-FishMesh-Estimator-Reason"
	HeaderLoadValid               = "X-FishMesh-Load-Valid"
	HeaderLoadSampleAgeMS         = "X-FishMesh-Load-Sample-Age-Ms"
	HeaderQueueDepth              = "X-FishMesh-Queue-Depth"
	HeaderRunningRequests         = "X-FishMesh-Running-Requests"
	HeaderLocalDelta              = "X-FishMesh-Local-Delta"
	HeaderLocalInflight           = "X-FishMesh-Local-Inflight"
	HeaderHardOverloadCount       = "X-FishMesh-Hard-Overload-Candidates"
	HeaderPredictionStatus        = "X-FishMesh-Prediction-Status"
	HeaderPredictionModel         = "X-FishMesh-Prediction-Model"
	HeaderPredictionWouldSelect   = "X-FishMesh-Prediction-Would-Select"
	HeaderPredictionSelectedMS    = "X-FishMesh-Prediction-Selected-TTFT-Ms"
	HeaderPredictionWouldSelectMS = "X-FishMesh-Prediction-Would-Select-TTFT-Ms"
	HeaderPredictionSamples       = "X-FishMesh-Prediction-Samples"
	HeaderUpstream                = "X-FishMesh-Upstream"

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
	SessionKey   string
	CacheSalt    string
	IgnoreEOS    bool
	MaxTokens    int
	StreamOutput io.Writer
}

// DecisionHeaders is the allowlisted request provenance returned by FishMesh. It does not retain arbitrary headers.
type DecisionHeaders struct {
	RoutingMode             string  `json:"routing_mode,omitempty"`
	RouteReason             string  `json:"route_reason,omitempty"`
	BackendID               string  `json:"backend_id,omitempty"`
	PreferredBackendID      string  `json:"preferred_backend_id,omitempty"`
	Policy                  string  `json:"policy,omitempty"`
	SpilloverReason         string  `json:"spillover_reason,omitempty"`
	KVStatus                string  `json:"kv_status,omitempty"`
	CachedPrefixTokens      int     `json:"cached_prefix_tokens"`
	PromptTokens            int     `json:"prompt_tokens,omitempty"`
	UncachedTokens          int     `json:"uncached_tokens,omitempty"`
	EstimatedTTFTMS         float64 `json:"estimated_ttft_ms,omitempty"`
	EstimatorValid          bool    `json:"estimator_valid"`
	EstimatorConfidence     string  `json:"estimator_confidence,omitempty"`
	EstimatorVersion        string  `json:"estimator_version,omitempty"`
	EstimatorReason         string  `json:"estimator_reason,omitempty"`
	LoadValid               bool    `json:"load_valid"`
	LoadSampleAgeMS         float64 `json:"load_sample_age_ms,omitempty"`
	QueueDepth              int64   `json:"queue_depth,omitempty"`
	RunningRequests         int64   `json:"running_requests,omitempty"`
	LocalDelta              int64   `json:"local_delta,omitempty"`
	LocalInflight           int64   `json:"local_inflight,omitempty"`
	HardOverloadCandidates  int     `json:"hard_overload_candidates,omitempty"`
	PredictionStatus        string  `json:"prediction_status,omitempty"`
	PredictionModel         string  `json:"prediction_model,omitempty"`
	PredictionWouldSelect   string  `json:"prediction_would_select,omitempty"`
	PredictionSelectedMS    float64 `json:"prediction_selected_ttft_ms,omitempty"`
	PredictionWouldSelectMS float64 `json:"prediction_would_select_ttft_ms,omitempty"`
	PredictionSamples       int     `json:"prediction_samples,omitempty"`
	Upstream                string  `json:"upstream,omitempty"`
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
	response, err := c.do(ctx, body, request.SessionKey)
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
	result.HasCachedPrefixSample = result.Headers.KVStatus == kvStatusAvailable
	if err != nil {
		return result, err
	}
	return result, nil
}
