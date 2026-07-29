package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/gateway"
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
	// 使用 JSON 格式的 slog logger：输出到容器 stdout 后，日志采集器
	// （Loki/ELK 等）可以直接结构化解析，默认的 text 格式在 K8s 里很难查询。
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// 配置全部来自环境变量（见 internal/serving/gateway/config.go）：
	// 这样同一个镜像无需重新编译就能在本地进程和 Kubernetes 两种环境运行，
	// 镜像构建一次、运行参数由部署方注入。
	config, err := gateway.LoadConfigFromEnvironment()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	server, err := gateway.NewServer(config, logger)
	if err != nil {
		logger.Error("create gateway", "error", err)
		os.Exit(1)
	}
	defer server.Close()

	httpServer := &http.Server{
		Addr:    config.ListenAddress,
		Handler: server.Handler(),
		// 只给"读取请求头"设超时：防止慢速客户端用极慢的 header 占住连接
		// （slowloris 攻击）。请求体不设超时——流式推理响应可能持续很久，
		// 端到端的超时由 gateway 内部的 RequestTimeout 负责（见 config.go）。
		ReadHeaderTimeout: 5 * time.Second,
	}

	// signal.NotifyContext：注册 SIGINT（本地 Ctrl-C）和 SIGTERM（Kubernetes
	// 滚动更新或删除 Pod 时由 kubelet 发送）两个信号，收到后 shutdownSignal.Done()
	// 被关闭。defer stop() 确保 main 退出时取消订阅，避免 goroutine 泄漏。
	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("gateway listening", "address", config.ListenAddress, "upstream", config.UpstreamURL)
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			// http.ErrServerClosed 是 Shutdown() 被调用后 ListenAndServe 的正常
			// 返回，不是错误；只有其他错误才需要记录并主动触发关停流程。
			logger.Error("gateway stopped unexpectedly", "error", serveErr)
			stop()
		}
	}()

	// 阻塞直到收到退出信号。
	// Kubernetes 默认给 Pod 30 秒优雅终止宽限期（terminationGracePeriodSeconds），
	// 所以 Shutdown 的超时必须小于这个值，否则请求还没处理完就会被 kubelet SIGKILL。
	<-shutdownSignal.Done()
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	// Shutdown：停止接收新连接，等待所有 in-flight 请求自然结束；
	// 超过 shutdown 超时仍未完成的连接会被强制断开。
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}
