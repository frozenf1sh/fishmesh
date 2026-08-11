package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
)

// main 是 fishmesh-gateway 的进程入口，只做四件事：
//  1. 从环境变量加载配置（校验失败直接退出，不尝试恢复）；
//  2. 由显式组合根创建 Gateway 及其全部依赖，拿到 HTTP Handler；
//  3. 用标准库 http.Server 开始监听；
//  4. 等待退出信号（SIGINT/SIGTERM），触发优雅关停。
//
// 选择标准库 http.Server 而不是 gin/echo 等框架是有意的：本项目核心诉求是
// “透明的流式代理”，框架自带的中间件、路由和缓冲行为可能干扰对字节流的精确
// 控制；标准库的请求取消、ResponseWriter 和 Shutdown 行为更容易与 SSE 透传契约
// 对齐，也避免在最外层入口引入额外的框架生命周期。
func main() {
	// 上游 llm-d-kv-cache 的 InMemoryIndex 内部使用 controller-runtime 的 logr。
	// 如果进程没有先设置 logger，首次构造或使用 index 时会打印
	// “log.SetLogger(...) was never called” 警告并污染 Gateway 日志。
	// FishMesh 自己的日志仍统一走下方 slog JSON；上游库的低价值内部日志丢弃，
	// 让第三方协议和日志实现都停留在 adapter/组合根边界，不进入 Gateway delivery。
	ctrllog.SetLogger(logr.Discard())
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// 1. 环境变量只在进程边界解析一次，domain 只接收已经拆分并校验的 Config。
	//    配置错误在任何外部 watcher、HTTP server 或 KV subscriber 启动前返回，
	//    因此失败启动不会留下需要异步回收的半初始化资源。
	config, err := servingconfig.LoadEnvironment()
	if err != nil {
		return fmt.Errorf("load environment: %w", err)
	}

	// 2. 显式组合所有实现，并把资源关闭责任保留在 cmd 组合根。
	//    Gateway 只接收接口，不在 handler 内创建 discovery、observation、transport
	//    或 KV index；这样关闭顺序和外部依赖的 owner 都可以在一个地方审查。
	runtime, err := buildRuntime(config, logger)
	if err != nil {
		return err
	}
	defer runtime.Close()
	httpServer := &http.Server{
		Addr:              config.Process.ListenAddress,
		Handler:           runtime.handler,
		ReadHeaderTimeout: config.Process.ReadHeaderTimeout,
	}

	// 3. 监听 SIGINT/SIGTERM；非正常 Listen 错误也会触发同一关停路径。
	//    startHTTPServer 使用 stop 结束信号上下文，使监听失败和操作系统信号共享
	//    后续的 HTTP Shutdown 与 domain Close 流程。
	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logRuntimeConfig(logger, config)
	startHTTPServer(httpServer, logger, stop)
	<-shutdownSignal.Done()

	// 4. 先停止接收新请求并等待 HTTP in-flight，再由 defer 按依赖反序关闭 domain 资源。
	//    只有 HTTP server 返回后，requestpath 才不会再产生新的 lease；随后关闭 KV、
	//    observation、discovery 和 transport，避免后台回调访问已经销毁的下游对象。
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.Process.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

// startHTTPServer 在独立 goroutine 中监听 HTTP，并把非预期监听错误转成进程级停止信号。
//
// ListenAndServe 的 http.ErrServerClosed 是 Shutdown 的正常结果，不能被当作故障记录。
// 其他错误调用 stop，让主 goroutine 继续执行统一的 Shutdown，而不是直接 os.Exit，
// 这样已有连接和下游 domain 仍有机会按约定释放。
func startHTTPServer(server *http.Server, logger *slog.Logger, stop context.CancelFunc) {
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway stopped unexpectedly", "error", err)
			stop()
		}
	}()
}

// logRuntimeConfig 记录启动时的有效运行边界，帮助排障时确认实际加载的配置。
//
// 日志只包含模式、地址、timeout、容量和阈值等低敏感配置，不记录 API key、请求体、
// prompt、Token IDs 或 routing key。这里展示的参数必须与组合根传入的 Config 一致，
// 不重新读取环境变量，避免出现“日志配置”和“实际配置”不一致。
func logRuntimeConfig(logger *slog.Logger, config servingconfig.Config) {
	logger.Info("gateway listening",
		"address", config.Process.ListenAddress,
		"upstream", config.RequestPath.Service.URL,
		"routing_mode", config.Routing.Mode,
		"endpoint_discovery", config.Discovery.Mode,
		"observation_mode", config.ObservationMode,
		"upstream_keepalive", config.Transport.KeepAlive,
		"session_key_ttl", config.Routing.SessionKey.TTL,
		"session_key_max_entries", config.Routing.SessionKey.MaxEntries,
		"session_key_inflight_delta", config.Routing.SessionKey.InflightDelta,
		"session_key_queue_depth_delta", config.Routing.SessionKey.QueueDepthDelta,
		"max_inflight_requests", config.Admission.MaxInflight,
		"max_conns_per_host", config.Transport.MaxConnsPerHost,
		"circuit_ewma_alpha", config.Circuit.EWMAAlpha,
		"circuit_error_threshold", config.Circuit.ErrorThreshold,
		"circuit_min_requests", config.Circuit.MinimumRequests,
		"circuit_open_duration", config.Circuit.OpenDuration,
	)
}
