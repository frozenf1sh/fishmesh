// Package tokenization 负责把 OpenAI 推理请求转换为目标模型真实使用的 Token IDs。
//
// 这个包只提供 prompt profile，不维护 KV cache，也不选择推理 backend。调用方应根据
// typed error 决定降级策略，不能把分词失败解释为“缓存命中为零”。
package tokenization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	RouteChatCompletions Route = "/v1/chat/completions"
	RouteCompletions     Route = "/v1/completions"

	CodeInvalidRequest      ErrorCode = "invalid_request"
	CodeUnsupportedRoute    ErrorCode = "unsupported_route"
	CodeRequestTooLarge     ErrorCode = "request_too_large"
	CodeUpstreamUnavailable ErrorCode = "upstream_unavailable"
	CodeUpstreamRejected    ErrorCode = "upstream_rejected"
	CodeResponseTooLarge    ErrorCode = "response_too_large"
	CodeInvalidResponse     ErrorCode = "invalid_response"
	CodeUnsupportedFeature  ErrorCode = "unsupported_feature"
	CodeTokenLimitExceeded  ErrorCode = "token_limit_exceeded"
)

// Route 是原始推理请求的 OpenAI-compatible 路径。
type Route string

// ErrorCode 是请求路径可以稳定统计和判断的失败类别。
type ErrorCode string

// Error 保留稳定错误类别、可选 upstream 状态码和原始错误链。
// 原始 context cancellation/deadline 会通过 Unwrap 继续支持 errors.Is。
type Error struct {
	Code           ErrorCode
	UpstreamStatus int
	Err            error
}

// Error 返回适合日志记录的简短错误信息。
func (e *Error) Error() string {
	if e == nil {
		return "tokenization error"
	}
	detail := string(e.Code)
	if e.UpstreamStatus != 0 {
		detail = fmt.Sprintf("%s: upstream status %d", detail, e.UpstreamStatus)
	}
	if e.Err != nil {
		detail += ": " + e.Err.Error()
	}
	return detail
}

// Unwrap 返回底层错误，供 errors.Is/errors.As 使用。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Input 是一次分词请求。Body 必须是对应 route 的完整 JSON 请求体。
type Input struct {
	Route Route
	Body  []byte
}

// Validate 执行不需要外部 I/O 的输入校验。
func (i Input) Validate() error {
	if !i.Route.supported() {
		return &Error{Code: CodeUnsupportedRoute, Err: fmt.Errorf("route %q is not supported", i.Route)}
	}
	if len(i.Body) == 0 || !json.Valid(i.Body) {
		return &Error{Code: CodeInvalidRequest, Err: errors.New("body must be valid JSON")}
	}
	return nil
}

// Prompt 是一个独立 prompt 的只读 token 序列。
type Prompt struct {
	tokenIDs []uint32
}

// TokenIDs 返回 token 序列副本，避免调用方修改已发布的 prompt profile。
func (p Prompt) TokenIDs() []uint32 {
	return slices.Clone(p.tokenIDs)
}

// TokenCount 返回 prompt 的 token 数。
func (p Prompt) TokenCount() int {
	return len(p.tokenIDs)
}

// Result 是一次 Render 调用得到的只读 prompt profile。
// Completions 的 batch prompt 可能产生多个 Prompt；Chat Completions 始终只有一个。
type Result struct {
	model       string
	cacheSalt   string
	prompts     []Prompt
	totalTokens int
}

// Model 返回生成 Token IDs 时实际使用的模型名。
func (r Result) Model() string {
	return r.model
}

// CacheSalt 返回请求携带的 cache isolation salt；空字符串表示请求未提供。
func (r Result) CacheSalt() string {
	return r.cacheSalt
}

// Prompts 返回 prompt 描述副本；每个 Prompt 的 token slice 仍由只读访问器保护。
func (r Result) Prompts() []Prompt {
	return slices.Clone(r.prompts)
}

// TotalTokens 返回所有 prompt 的 token 总数。
func (r Result) TotalTokens() int {
	return r.totalTokens
}

// Tokenizer 是“推理请求到真实模型 Token IDs”的稳定替换边界。
type Tokenizer interface {
	Tokenize(context.Context, Input) (Result, error)
}

// Config 定义 vLLM Render adapter 的模型身份和资源上限。
type Config struct {
	BaseURL          string
	Model            string
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxTotalTokens   int
}

// Dependencies 是由组合根注入的外部 I/O 能力。
type Dependencies struct {
	HTTPClient *http.Client
}

// Validate 检查 Render adapter 所需的固定外部能力。
func (d Dependencies) Validate() error {
	if d.HTTPClient == nil {
		return fmt.Errorf("tokenization HTTP client must not be nil")
	}
	return nil
}

// Validate 检查 adapter 是否具有确定的模型和有限的资源预算。
func (c Config) Validate() error {
	parsed, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("tokenization base URL must be an absolute HTTP URL: %q", c.BaseURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("tokenization base URL must not contain query or fragment")
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("tokenization model must not be empty")
	}
	if c.Timeout <= 0 || c.MaxRequestBytes <= 0 || c.MaxResponseBytes <= 0 || c.MaxTotalTokens <= 0 {
		return fmt.Errorf("tokenization timeout and resource limits must be positive")
	}
	return nil
}

func (r Route) supported() bool {
	switch r {
	case RouteChatCompletions, RouteCompletions:
		return true
	default:
		return false
	}
}

func newResult(model, cacheSalt string, tokenGroups [][]uint32) Result {
	result := Result{model: model, cacheSalt: cacheSalt, prompts: make([]Prompt, len(tokenGroups))}
	for index, tokens := range tokenGroups {
		result.prompts[index] = Prompt{tokenIDs: slices.Clone(tokens)}
		result.totalTokens += len(tokens)
	}
	return result
}
