// Package gateway 负责 standalone HTTP/SSE 交付，把协议无关的 requestpath lease
// 转换为兼容 OpenAI 的上游 HTTP 流。
//
// Gateway 的职责止于 delivery：它负责健康检查、就绪检查、Prometheus 指标、
// 请求体边界、请求头翻译、上游连接建立以及 SSE 字节流透传；它不拥有 endpoint
// discovery、负载观测、KV index 或路由算法。具体实现由 cmd/fishmesh-gateway
// 这个组合根创建后，通过 Dependencies 注入，因此阅读本包时不需要追踪隐藏的
// Kubernetes client 或全局 HTTP client。
//
// 一次代理请求固定经过以下阶段：先取得非阻塞 admission permit，再有界读取并
// 保存原始请求体，然后调用 requestpath 选择 backend 并取得 lease，接着建立一次
// upstream 请求并逐块复制响应，最后把精确的 outcome 交给 lease 完成结算。请求体
// 只保留一份字节副本：KV-aware 模式可以用它调用 Render API，真正转发时仍使用同一份
// 原始 JSON，从而避免“选路时解析过的请求”和“上游收到的请求”产生差异。
//
// response headers 发出以后禁止透明重试。客户端取消、请求超时、连接建立失败、
// response headers 后的上游断流和下游写失败必须分别分类，因为它们对 circuit、
// 指标和运维排障的含义不同。SSE 的 TTFT 只在第一个非终止 data 事件被识别时记录，
// [DONE] 不算首事件；chunk 可以在任意字节边界被拆分，因此 detector 需要跨 read
// 调用保存未完成的行。
//
// 本包的指标只允许使用 backend ID、状态和固定 reason 等低基数标签，绝不能把
// routing key、prompt、Token IDs 或完整请求内容写进 Prometheus label。
package gateway

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

// Config 包含 HTTP delivery 自己解释的运行参数。
//
// RoutingMode 只用于把本次运行采用的策略投影到请求头和指标；Gateway 不根据这个
// 字段创建或切换策略。RequestTimeout 同时限制单次上游请求的生命周期，
// MaxRequestBodyBytes 则在选路之前保护 Gateway 的内存和 Render/upstream 重放边界。
type Config struct {
	RoutingMode         routing.Mode
	KeepAlive           bool
	RequestTimeout      time.Duration
	MaxRequestBodyBytes int64
}

// Dependencies 是由进程组合根创建并注入的 Gateway 外部能力。
//
// RequestPath 负责选择与 lease 生命周期，Admission 负责进程级并发上限，Transport
// 负责按 backend 管理 HTTP client，Metrics 负责低基数观测，Logger 负责结构化告警。
// Gateway 只依赖这些稳定契约，不在请求处理过程中调用 New 函数或读取环境变量。
type Dependencies struct {
	RequestPath requestpath.Path
	Admission   admission.Controller
	Transport   transport.Pool
	Metrics     *Metrics
	Logger      *slog.Logger
}

// Server 是 standalone runtime 的 HTTP delivery adapter。
//
// Server 不保存跨请求的路由事实；跨请求状态归 requestpath、transport、circuit 和
// kvcache 等 owner 管理。本结构只保存处理 HTTP 请求所需的已注入引用，以及当前
// delivery 配置和指标对象。
type Server struct {
	config      Config
	requestPath requestpath.Path
	admission   admission.Controller
	transport   transport.Pool
	metrics     *Metrics
	logger      *slog.Logger
}

// New 创建只负责 delivery 的 Server。
//
// 构造函数只校验本地配置和依赖，不读取环境变量、不创建 Kubernetes/Prometheus/HTTP
// 外部资源，也不启动后台 goroutine。所有带生命周期的能力都必须由组合根先创建，
// 并在组合根关闭时按依赖反序释放。
func New(config Config, dependencies Dependencies) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := dependencies.Validate(); err != nil {
		return nil, err
	}
	return &Server{
		config: config, requestPath: dependencies.RequestPath, admission: dependencies.Admission,
		transport: dependencies.Transport, metrics: dependencies.Metrics, logger: dependencies.Logger,
	}, nil
}

// Validate 检查 Gateway 在进程启动后不会变化的本地配置。
//
// RoutingMode 只作为观测元数据使用，因此具体策略是否合法由 routing 配置的 owner
// 检查；这里只检查 delivery 真正需要的时间边界和请求体边界。把这些条件集中在方法中，
// 可以让构造函数保持为“校验、组装、返回”，请求处理路径无需重复检查启动配置。
func (c Config) Validate() error {
	if err := c.RoutingMode.Validate(); err != nil {
		return fmt.Errorf("gateway routing mode: %w", err)
	}
	if c.RequestTimeout <= 0 || c.MaxRequestBodyBytes <= 0 {
		return fmt.Errorf("gateway request timeout and body limit must be positive")
	}
	return nil
}

// Validate 检查 Gateway 运行时必须具备的外部能力。
//
// 这些依赖由组合根创建并在这里一次性确认；请求处理函数因此可以直接使用注入的
// contract，而不必在每个逻辑分支中重复写 nil 判断。
func (d Dependencies) Validate() error {
	if d.RequestPath == nil || d.Admission == nil || d.Transport == nil || d.Metrics == nil || d.Logger == nil {
		return fmt.Errorf("gateway requestpath, admission, transport, metrics and logger must not be nil")
	}
	return nil
}
