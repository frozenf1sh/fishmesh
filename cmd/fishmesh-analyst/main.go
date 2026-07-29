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

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/adapters"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/config"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/delivery"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

// fishmesh-analyst 是慢速、只读的集群分析控制面入口。它不在 Gateway
// 请求路径上，也不执行 kubectl、扩缩容或路由修改。
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	runtimeConfig, err := config.LoadConfigFromEnvironment()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	registry, err := adapters.NewRegistryForConfig(runtimeConfig, &http.Client{Timeout: runtimeConfig.RequestTimeout}, time.Now)
	if err != nil {
		logger.Error("create tool registry", "error", err)
		os.Exit(1)
	}
	engine, err := application.NewEngine(registry, domain.DefaultRulePolicy(), time.Now)
	if err != nil {
		logger.Error("create analysis engine", "error", err)
		os.Exit(1)
	}
	server, err := delivery.NewHTTPServer(engine, registry.Descriptors(), runtimeConfig.RequestTimeout)
	if err != nil {
		logger.Error("create analyst server", "error", err)
		os.Exit(1)
	}
	httpServer := &http.Server{Addr: runtimeConfig.ListenAddress, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("analyst listening", "address", runtimeConfig.ListenAddress, "mode", runtimeConfig.Mode)
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("analyst stopped unexpectedly", "error", serveErr)
			stop()
		}
	}()
	<-shutdownSignal.Done()
	shutdownContext, cancel := context.WithTimeout(context.Background(), runtimeConfig.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}
