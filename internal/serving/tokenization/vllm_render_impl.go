package tokenization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	chatRenderPath        = "/v1/chat/completions/render"
	completionsRenderPath = "/v1/completions/render"

	headerContentType = "Content-Type"
	mediaTypeJSON     = "application/json"
	fieldModel        = "model"
	fieldCacheSalt    = "cache_salt"

	maxErrorBodyBytes = 1024
)

var _ Tokenizer = vllmRenderer{}

// vllmRenderer 是 vLLM Render HTTP 协议的薄 adapter。
// 它只翻译 wire format 和保护资源边界，不包含 fallback 或 backend 选择逻辑。
type vllmRenderer struct {
	config Config
	client *http.Client
}

// renderResponse 是 FishMesh 从 vLLM Render 响应中消费的最小字段集合。
type renderResponse struct {
	Model    string          `json:"model"`
	TokenIDs []uint32        `json:"token_ids"`
	Features *renderFeatures `json:"features,omitempty"`
}

// renderFeatures 只用于识别当前文本 MVP 尚未支持的多模态 block key 输入。
type renderFeatures struct {
	Hashes       map[string][]string          `json:"mm_hashes"`
	Placeholders map[string][]placeholderWire `json:"mm_placeholders"`
}

// placeholderWire 描述多模态内容在 token 序列中的区间。
type placeholderWire struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
}

// NewVLLMRenderer 构造 vLLM Render adapter。HTTP client 必须由组合根显式注入，
// 以便统一管理连接池、TLS 和进程退出时的资源回收。
func NewVLLMRenderer(config Config, dependencies Dependencies) (Tokenizer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if dependencies.HTTPClient == nil {
		return nil, fmt.Errorf("tokenization HTTP client must not be nil")
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.Model = strings.TrimSpace(config.Model)
	return vllmRenderer{config: config, client: dependencies.HTTPClient}, nil
}

// Tokenize 校验并转发请求，然后把 vLLM wire response 收敛为只读 Result。
func (r vllmRenderer) Tokenize(ctx context.Context, input Input) (Result, error) {
	if int64(len(input.Body)) > r.config.MaxRequestBytes {
		return Result{}, &Error{Code: CodeRequestTooLarge, Err: fmt.Errorf("request body is %d bytes", len(input.Body))}
	}
	if err := input.Validate(); err != nil {
		return Result{}, err
	}

	// 1. 读取稳定字段，并用配置模型覆盖 Render payload。
	payload, cacheSalt, err := r.buildPayload(input.Body)
	if err != nil {
		return Result{}, err
	}

	// 2. 调用与原始推理 route 对应的 Render API。
	responses, err := r.render(ctx, input.Route, payload)
	if err != nil {
		return Result{}, err
	}

	// 3. 验证响应后再发布不可变 prompt profile。
	groups, err := r.validateResponses(responses)
	if err != nil {
		return Result{}, err
	}
	return newResult(r.config.Model, cacheSalt, groups), nil
}

func (r vllmRenderer) buildPayload(body []byte) ([]byte, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, "", &Error{Code: CodeInvalidRequest, Err: errors.New("body must be a JSON object")}
	}

	requestModel, err := requiredString(fields, fieldModel)
	if err != nil {
		return nil, "", &Error{Code: CodeInvalidRequest, Err: err}
	}
	if requestModel != r.config.Model {
		return nil, "", &Error{Code: CodeInvalidRequest, Err: fmt.Errorf("request model %q does not match configured model %q", requestModel, r.config.Model)}
	}
	cacheSalt, err := optionalString(fields, fieldCacheSalt)
	if err != nil {
		return nil, "", &Error{Code: CodeInvalidRequest, Err: err}
	}

	encodedModel, err := json.Marshal(r.config.Model)
	if err != nil {
		return nil, "", &Error{Code: CodeInvalidRequest, Err: fmt.Errorf("encode configured model: %w", err)}
	}
	fields[fieldModel] = encodedModel
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, "", &Error{Code: CodeInvalidRequest, Err: fmt.Errorf("encode render payload: %w", err)}
	}
	if int64(len(payload)) > r.config.MaxRequestBytes {
		return nil, "", &Error{Code: CodeRequestTooLarge, Err: fmt.Errorf("render payload is %d bytes", len(payload))}
	}
	return payload, cacheSalt, nil
}

func (r vllmRenderer) render(ctx context.Context, route Route, payload []byte) ([]renderResponse, error) {
	path := chatRenderPath
	if route == RouteCompletions {
		path = completionsRenderPath
	}
	body, err := r.post(ctx, path, payload)
	if err != nil {
		return nil, err
	}

	if route == RouteChatCompletions {
		var response renderResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, &Error{Code: CodeInvalidResponse, Err: fmt.Errorf("decode chat render response: %w", err)}
		}
		return []renderResponse{response}, nil
	}

	var responses []renderResponse
	if err := json.Unmarshal(body, &responses); err != nil {
		return nil, &Error{Code: CodeInvalidResponse, Err: fmt.Errorf("decode completions render response: %w", err)}
	}
	return responses, nil
}

func (r vllmRenderer) post(ctx context.Context, path string, payload []byte) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, r.config.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Code: CodeInvalidRequest, Err: fmt.Errorf("build render request: %w", err)}
	}
	request.Header.Set(headerContentType, mediaTypeJSON)

	response, err := r.client.Do(request)
	if err != nil {
		return nil, &Error{Code: CodeUpstreamUnavailable, Err: fmt.Errorf("call vLLM render: %w", err)}
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return nil, &Error{
			Code:           CodeUpstreamRejected,
			UpstreamStatus: response.StatusCode,
			Err:            fmt.Errorf("vLLM render rejected request: %s", strings.TrimSpace(string(snippet))),
		}
	}

	body, err := readBounded(response.Body, r.config.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (r vllmRenderer) validateResponses(responses []renderResponse) ([][]uint32, error) {
	if len(responses) == 0 {
		return nil, &Error{Code: CodeInvalidResponse, Err: errors.New("vLLM render returned no prompts")}
	}

	groups := make([][]uint32, len(responses))
	totalTokens := 0
	for index, response := range responses {
		if response.Model != "" && response.Model != r.config.Model {
			return nil, &Error{Code: CodeInvalidResponse, Err: fmt.Errorf("response model %q does not match configured model %q", response.Model, r.config.Model)}
		}
		if response.TokenIDs == nil {
			return nil, &Error{Code: CodeInvalidResponse, Err: fmt.Errorf("prompt %d has no token_ids", index)}
		}
		if response.Features != nil && (len(response.Features.Hashes) > 0 || len(response.Features.Placeholders) > 0) {
			return nil, &Error{Code: CodeUnsupportedFeature, Err: errors.New("multimodal render features are not supported by the text MVP")}
		}

		totalTokens += len(response.TokenIDs)
		if totalTokens > r.config.MaxTotalTokens {
			return nil, &Error{Code: CodeTokenLimitExceeded, Err: fmt.Errorf("render returned %d tokens", totalTokens)}
		}
		groups[index] = response.TokenIDs
	}
	return groups, nil
}

func requiredString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("field %q is required", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("field %q must be a non-empty string", name)
	}
	return value, nil
}

func optionalString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("field %q must be a string", name)
	}
	return value, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, &Error{Code: CodeUpstreamUnavailable, Err: fmt.Errorf("read vLLM render response: %w", err)}
	}
	if int64(len(body)) > limit {
		return nil, &Error{Code: CodeResponseTooLarge, Err: fmt.Errorf("render response exceeds %d bytes", limit)}
	}
	return body, nil
}
