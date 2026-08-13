package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const (
	DefaultMaxTokens     = 32
	chatCompletionsRoute = "/v1/chat/completions"
	contentTypeJSON      = "application/json"
	acceptSSE            = "text/event-stream"
	authorizationScheme  = "Bearer "
	headerSessionKey     = "X-FishMesh-Session-Key"
	kvStatusAvailable    = "available"
	responseErrorLimit   = 4096
)

const defaultMaxTokens = DefaultMaxTokens

type completionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
	CacheSalt string    `json:"cache_salt,omitempty"`
	IgnoreEOS bool      `json:"ignore_eos,omitempty"`
}

func (c *Client) encodeRequest(request Request) ([]byte, error) {
	maxTokens := c.config.MaxTokens
	if request.MaxTokens > 0 {
		maxTokens = request.MaxTokens
	}
	payload, err := json.Marshal(completionRequest{Model: c.config.Model, Messages: request.Messages, Stream: true, MaxTokens: maxTokens, CacheSalt: request.CacheSalt, IgnoreEOS: request.IgnoreEOS})
	if err != nil {
		return nil, fmt.Errorf("encode chat completion request: %w", err)
	}
	return payload, nil
}

func (c *Client) do(ctx context.Context, body []byte, sessionKey string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.Endpoint, "/")+chatCompletionsRoute, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build chat completion request: %w", err)
	}
	request.Header.Set("Content-Type", contentTypeJSON)
	request.Header.Set("Accept", acceptSSE)
	if c.config.APIKey != "" {
		request.Header.Set("Authorization", authorizationScheme+c.config.APIKey)
	}
	if sessionKey != "" {
		request.Header.Set(headerSessionKey, sessionKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send chat completion request: %w", err)
	}
	return response, nil
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, responseErrorLimit))
	return fmt.Errorf("chat completion status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

func decisionHeaders(headers http.Header) DecisionHeaders {
	return DecisionHeaders{
		RoutingMode: headers.Get(HeaderRoutingMode), RouteReason: headers.Get(HeaderRouteReason), BackendID: headers.Get(HeaderBackendID),
		PreferredBackendID: headers.Get(HeaderPreferredBackendID), Policy: headers.Get(HeaderPolicy), SpilloverReason: headers.Get(HeaderSpilloverReason),
		KVStatus: headers.Get(HeaderKVStatus), CachedPrefixTokens: cachedPrefixTokens(headers.Get(HeaderCachedPrefixTokens)), Upstream: headers.Get(HeaderUpstream),
		PromptTokens: nonNegativeInt(headers.Get(HeaderPromptTokens)), UncachedTokens: nonNegativeInt(headers.Get(HeaderUncachedTokens)),
		EstimatedTTFTMS: nonNegativeFloat(headers.Get(HeaderEstimatedTTFTMS)), EstimatorValid: headerBool(headers.Get(HeaderEstimatorValid)),
		EstimatorConfidence: headers.Get(HeaderEstimatorConfidence), EstimatorVersion: headers.Get(HeaderEstimatorVersion), EstimatorReason: headers.Get(HeaderEstimatorReason),
		LoadValid: headerBool(headers.Get(HeaderLoadValid)), LoadSampleAgeMS: nonNegativeFloat(headers.Get(HeaderLoadSampleAgeMS)),
		QueueDepth: nonNegativeInt64(headers.Get(HeaderQueueDepth)), RunningRequests: nonNegativeInt64(headers.Get(HeaderRunningRequests)),
		LocalDelta: nonNegativeInt64(headers.Get(HeaderLocalDelta)), LocalInflight: nonNegativeInt64(headers.Get(HeaderLocalInflight)),
		HardOverloadCandidates: nonNegativeInt(headers.Get(HeaderHardOverloadCount)),
		PredictionStatus:       headers.Get(HeaderPredictionStatus), PredictionModel: headers.Get(HeaderPredictionModel),
		PredictionWouldSelect: headers.Get(HeaderPredictionWouldSelect), PredictionSelectedMS: nonNegativeFloat(headers.Get(HeaderPredictionSelectedMS)),
		PredictionWouldSelectMS: nonNegativeFloat(headers.Get(HeaderPredictionWouldSelectMS)), PredictionSamples: nonNegativeInt(headers.Get(HeaderPredictionSamples)),
	}
}

func cachedPrefixTokens(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func nonNegativeInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func nonNegativeInt64(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func nonNegativeFloat(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func headerBool(raw string) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(raw))
	return value
}

// String formats only the fixed decision-header contract for humans and avoids dumping unbounded upstream headers.
func (h DecisionHeaders) String() string {
	return fmt.Sprintf("routing_mode=%s route_reason=%s policy=%s kv_status=%s cached_prefix_tokens=%d prompt_tokens=%d uncached_tokens=%d estimated_ttft_ms=%.3f estimator_confidence=%s estimator_version=%s backend_id=%s preferred_backend_id=%s upstream=%s spillover_reason=%s",
		h.RoutingMode, h.RouteReason, h.Policy, h.KVStatus, h.CachedPrefixTokens, h.PromptTokens, h.UncachedTokens, h.EstimatedTTFTMS,
		h.EstimatorConfidence, h.EstimatorVersion, h.BackendID, h.PreferredBackendID, h.Upstream, h.SpilloverReason)
}
