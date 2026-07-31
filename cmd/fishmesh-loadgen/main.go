package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/workload/loadgen"
)

func main() {
	config := loadgen.Config{}
	flag.StringVar(&config.RunID, "run-id", valueOrDefault("FISHMESH_RUN_ID", "manual"), "unique immutable experiment run identifier")
	flag.StringVar(&config.GitSHA, "git-sha", valueOrDefault("FISHMESH_GIT_SHA", "unknown"), "FishMesh git revision")
	flag.StringVar(&config.GatewayImage, "gateway-image", valueOrDefault("FISHMESH_GATEWAY_IMAGE", "unknown"), "Gateway image reference or digest")
	flag.StringVar(&config.VLLMVersion, "vllm-version", valueOrDefault("FISHMESH_VLLM_VERSION", "unknown"), "vLLM runtime version")
	flag.StringVar(&config.ClusterProfile, "cluster-profile", valueOrDefault("FISHMESH_CLUSTER_PROFILE", "unknown"), "benchmark cluster profile")
	flag.StringVar(&config.Endpoint, "endpoint", "http://fishmesh-gateway.kubellm.svc.cluster.local:8080", "FishMesh Gateway base URL")
	flag.StringVar(&config.Model, "model", "qwen2.5-0.5b-instruct", "OpenAI model name")
	flag.IntVar(&config.Requests, "requests", 200, "total requests to send")
	flag.IntVar(&config.Concurrency, "concurrency", 4, "maximum in-flight requests")
	flag.IntVar(&config.PrefixGroups, "prefix-groups", 8, "number of deterministic shared-prefix groups")
	flag.IntVar(&config.PrefixBytes, "prefix-bytes", 4096, "approximate bytes in each shared system prefix")
	flag.StringVar(&config.PrefixNamespace, "prefix-namespace", "default", "namespace for this benchmark's prefix corpus")
	flag.IntVar(&config.HotPrefixRatio, "hot-prefix-ratio", 0, "percentage of requests assigned to prefix group 0")
	flag.IntVar(&config.MaxTokens, "max-tokens", 32, "maximum generated tokens per request")
	flag.DurationVar(&config.RequestTimeout, "request-timeout", 90*time.Second, "per-request timeout")
	flag.BoolVar(&config.KeepAlive, "keep-alive", false, "reuse client-to-gateway HTTP connections")
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
	_, err := loadgen.Run(contextWithSignal, config, output)
	if err != nil {
		logger.Error("benchmark failed", "error", err)
		os.Exit(1)
	}
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
