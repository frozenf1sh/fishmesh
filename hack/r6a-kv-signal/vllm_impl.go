package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	renderPath       = "/v1/chat/completions/render"
	generatePath     = "/v1/chat/completions"
	vllmTimeout      = 30 * time.Second
	maxVLLMResponse  = 4 << 20
	contentTypeJSON  = "application/json"
	defaultModelName = "qwen2.5-0.5b-instruct"
)

// vllmClient 只负责 R6A 所需的 Render 与定向生成 HTTP 边界。
type vllmClient struct {
	httpClient *http.Client
}

type renderResponse struct {
	TokenIDs []uint32 `json:"token_ids"`
}

type modelRequest struct {
	Model string `json:"model"`
}

func newVLLMClient() *vllmClient {
	return &vllmClient{httpClient: &http.Client{Timeout: vllmTimeout}}
}

func (c *vllmClient) Render(
	ctx context.Context,
	baseURL string,
	body json.RawMessage,
) ([]uint32, time.Duration, error) {
	started := time.Now()
	status, responseBody, err := c.post(ctx, baseURL+renderPath, body)
	latency := time.Since(started)
	if err != nil {
		return nil, latency, err
	}
	if status != http.StatusOK {
		return nil, latency, fmt.Errorf("Render 返回 HTTP %d: %s", status, responseBody)
	}

	var response renderResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, latency, fmt.Errorf("解析 Render 响应: %w", err)
	}
	if len(response.TokenIDs) == 0 {
		return nil, latency, fmt.Errorf("Render 未返回 token_ids")
	}
	return response.TokenIDs, latency, nil
}

func (c *vllmClient) Generate(
	ctx context.Context,
	baseURL string,
	body json.RawMessage,
) (int, []byte, time.Duration, error) {
	started := time.Now()
	status, responseBody, err := c.post(ctx, baseURL+generatePath, body)
	return status, responseBody, time.Since(started), err
}

func (c *vllmClient) post(ctx context.Context, url string, body []byte) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("创建 vLLM 请求: %w", err)
	}
	request.Header.Set("Content-Type", contentTypeJSON)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("调用 vLLM: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxVLLMResponse))
	if err != nil {
		return 0, nil, fmt.Errorf("读取 vLLM 响应: %w", err)
	}
	return response.StatusCode, responseBody, nil
}

func renderedModel(body json.RawMessage) string {
	var request modelRequest
	if err := json.Unmarshal(body, &request); err != nil || request.Model == "" {
		return defaultModelName
	}
	return request.Model
}
