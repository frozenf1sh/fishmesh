// Package gateway 负责 standalone HTTP/SSE 交付，并把 requestpath lease 转换为兼容 OpenAI 的上游流。
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

// Config 只包含 HTTP delivery 自己解释的运行参数。
type Config struct {
	RoutingMode    routing.Mode
	KeepAlive      bool
	RequestTimeout time.Duration
}

// Dependencies 由进程组合根创建并注入，Gateway 不选择具体实现。
type Dependencies struct {
	RequestPath requestpath.Path
	Admission   admission.Controller
	Transport   transport.Pool
	Metrics     *Metrics
	Logger      *slog.Logger
}

// Server 是 standalone 开发与 conformance runtime 的 HTTP adapter。
type Server struct {
	config      Config
	requestPath requestpath.Path
	admission   admission.Controller
	transport   transport.Pool
	metrics     *Metrics
	logger      *slog.Logger
}

// New 创建纯 delivery Server；所有外部能力必须已经由组合根构造完成。
func New(config Config, dependencies Dependencies) (*Server, error) {
	if config.RequestTimeout <= 0 {
		return nil, fmt.Errorf("gateway request timeout must be positive")
	}
	if dependencies.RequestPath == nil || dependencies.Admission == nil || dependencies.Transport == nil || dependencies.Metrics == nil || dependencies.Logger == nil {
		return nil, fmt.Errorf("gateway requestpath, admission, transport, metrics and logger must not be nil")
	}
	return &Server{
		config: config, requestPath: dependencies.RequestPath, admission: dependencies.Admission,
		transport: dependencies.Transport, metrics: dependencies.Metrics, logger: dependencies.Logger,
	}, nil
}
