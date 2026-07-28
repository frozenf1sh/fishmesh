package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/loadgen"
)

func main() {
	config := loadgen.Config{}
	flag.StringVar(&config.Endpoint, "endpoint", "http://fishmesh-gateway.kubellm.svc.cluster.local:8080", "FishMesh Gateway base URL")
	flag.StringVar(&config.Model, "model", "qwen2.5-0.5b-instruct", "OpenAI model name")
	flag.IntVar(&config.Requests, "requests", 200, "total requests to send")
	flag.IntVar(&config.Concurrency, "concurrency", 4, "maximum in-flight requests")
	flag.IntVar(&config.PrefixGroups, "prefix-groups", 8, "number of deterministic shared-prefix groups")
	flag.IntVar(&config.PrefixBytes, "prefix-bytes", 4096, "approximate bytes in each shared system prefix")
	flag.IntVar(&config.MaxTokens, "max-tokens", 32, "maximum generated tokens per request")
	flag.DurationVar(&config.RequestTimeout, "request-timeout", 90*time.Second, "per-request timeout")
	flag.StringVar(&config.OutputPath, "output", "", "optional JSONL result path; stdout is always used")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := config.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	output := io.Writer(os.Stdout)
	if config.OutputPath != "" {
		outputFile, err := os.OpenFile(config.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			logger.Error("open output file", "error", err)
			os.Exit(1)
		}
		defer outputFile.Close()
		output = io.MultiWriter(os.Stdout, outputFile)
	}

	contextWithSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	summary, err := loadgen.Run(contextWithSignal, config, output)
	if err != nil {
		logger.Error("benchmark failed", "error", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "completed=%d succeeded=%d failed=%d ttft_p50_ms=%.2f ttft_p95_ms=%.2f\\n", summary.Completed, summary.Succeeded, summary.Failed, summary.TTFTP50Milliseconds, summary.TTFTP95Milliseconds)
}
