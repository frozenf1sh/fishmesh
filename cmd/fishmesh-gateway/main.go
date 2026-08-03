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

	servingconfig "github.com/frozenf1sh/fishmesh/internal/serving/config"
)

// main 是 fishmesh-gateway 的入口，只做四件事：
//  1. 从环境变量加载配置（校验失败直接退出，不尝试恢复）；
//  2. 创建 gateway.Server，拿到它的 HTTP Handler；
//  3. 用标准库 http.Server 开始监听；
//  4. 等待退出信号（SIGINT/SIGTERM），触发优雅关停。
//
// 选择标准库 http.Server 而不是 gin/echo 等框架是有意的：本项目核心诉求是
// "透明的流式代理"，框架自带的中间件、路由和缓冲行为会干扰对字节流的精确
// 控制；标准库的行为完全可预期，也避免了额外的依赖。
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// 1. 环境变量只在进程边界解析一次，domain 接收已经拆分并校验的 Config。
	config, err := servingconfig.LoadEnvironment()
	if err != nil {
		return fmt.Errorf("load environment: %w", err)
	}

	// 2. 显式组合所有实现，并把资源关闭责任保留在 cmd。
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
	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logRuntimeConfig(logger, config)
	startHTTPServer(httpServer, logger, stop)
	<-shutdownSignal.Done()

	// 4. 先停止接收请求并等待 HTTP in-flight，再由 defer 反序关闭 domain 资源。
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.Process.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

func startHTTPServer(server *http.Server, logger *slog.Logger, stop context.CancelFunc) {
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway stopped unexpectedly", "error", err)
			stop()
		}
	}()
}

func logRuntimeConfig(logger *slog.Logger, config servingconfig.Config) {
	logger.Info("gateway listening",
		"address", config.Process.ListenAddress,
		"upstream", config.RequestPath.Service.URL,
		"routing_mode", config.Routing.Mode,
		"endpoint_discovery", config.Discovery.Mode,
		"observation_mode", config.ObservationMode,
		"upstream_keepalive", config.Transport.KeepAlive,
		"affinity_ttl", config.Routing.BoundedAffinity.TTL,
		"affinity_max_entries", config.Routing.BoundedAffinity.MaxEntries,
		"affinity_inflight_delta", config.Routing.BoundedAffinity.InflightDelta,
		"affinity_queue_depth_delta", config.Routing.BoundedAffinity.QueueDepthDelta,
		"max_inflight_requests", config.Admission.MaxInflight,
		"max_conns_per_host", config.Transport.MaxConnsPerHost,
		"circuit_ewma_alpha", config.Circuit.EWMAAlpha,
		"circuit_error_threshold", config.Circuit.ErrorThreshold,
		"circuit_min_requests", config.Circuit.MinimumRequests,
		"circuit_open_duration", config.Circuit.OpenDuration,
	)
}
