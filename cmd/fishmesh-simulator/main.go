package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/simulator"
)

const (
	defaultListenAddress   = ":8090"
	readHeaderTimeout      = 5 * time.Second
	gracefulShutdownWindow = 10 * time.Second
)

type config struct {
	listenAddress string
	behavior      simulator.Behavior
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(parseFlags(), logger); err != nil {
		logger.Error("simulator exited", "error", err)
		os.Exit(1)
	}
}

func run(config config, logger *slog.Logger) error {
	backend, err := simulator.New(config.behavior)
	if err != nil {
		return fmt.Errorf("create simulator: %w", err)
	}
	server := &http.Server{Addr: config.listenAddress, Handler: backend.Handler(), ReadHeaderTimeout: readHeaderTimeout}
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("simulator listening", "address", config.listenAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("simulator stopped unexpectedly", "error", err)
			stop()
		}
	}()
	<-shutdown.Done()

	ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownWindow)
	defer cancel()
	return server.Shutdown(ctx)
}

func parseFlags() config {
	result := config{}
	flag.StringVar(&result.listenAddress, "listen", defaultListenAddress, "HTTP listen address")
	flag.IntVar(&result.behavior.StatusCode, "status", http.StatusOK, "upstream HTTP status")
	flag.DurationVar(&result.behavior.FirstEventDelay, "first-event-delay", 0, "delay before the first SSE event")
	flag.DurationVar(&result.behavior.EventInterval, "event-interval", 0, "delay between SSE events")
	flag.IntVar(&result.behavior.Events, "events", 1, "number of non-terminal SSE events")
	flag.IntVar(&result.behavior.AbortAfterEvents, "abort-after-events", 0, "abort stream after N events; zero disables")
	flag.BoolVar(&result.behavior.Hold, "hold", false, "hold streams until clients cancel")
	flag.Float64Var(&result.behavior.QueueDepth, "queue-depth", 0, "simulated vLLM waiting requests")
	flag.Float64Var(&result.behavior.RunningRequests, "running-requests", 0, "simulated vLLM running requests")
	flag.Parse()
	return result
}
