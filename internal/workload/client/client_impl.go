package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
}

func (c *Client) encodeRequest(request Request) ([]byte, error) {
	maxTokens := c.config.MaxTokens
	if request.MaxTokens > 0 {
		maxTokens = request.MaxTokens
	}
	payload, err := json.Marshal(completionRequest{Model: c.config.Model, Messages: request.Messages, Stream: true, MaxTokens: maxTokens})
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
	}
}

func cachedPrefixTokens(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// String formats only the fixed decision-header contract for humans and avoids dumping unbounded upstream headers.
func (h DecisionHeaders) String() string {
	return fmt.Sprintf("routing_mode=%s route_reason=%s policy=%s kv_status=%s cached_prefix_tokens=%d backend_id=%s preferred_backend_id=%s upstream=%s spillover_reason=%s",
		h.RoutingMode, h.RouteReason, h.Policy, h.KVStatus, h.CachedPrefixTokens, h.BackendID, h.PreferredBackendID, h.Upstream, h.SpilloverReason)
}
