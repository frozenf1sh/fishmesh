// fishmesh-client is a local OpenAI-compatible conversation and bounded benchmark client.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/workload/client"
)

const (
	defaultEndpoint = "http://127.0.0.1:8080"
	defaultModel    = "qwen2.5-0.5b-instruct"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "fishmesh-client:", err)
		os.Exit(1)
	}
}

func run(arguments []string, input io.Reader, output, diagnostics io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: fishmesh-client <chat|request|bench> [flags]")
	}
	switch arguments[0] {
	case "chat":
		return runChat(arguments[1:], input, output, diagnostics)
	case "request":
		return runRequest(arguments[1:], output, diagnostics)
	case "bench":
		return runBenchmark(arguments[1:], diagnostics)
	default:
		return fmt.Errorf("unknown command %q (use chat, request, or bench)", arguments[0])
	}
}

func commonFlags(set *flag.FlagSet) (endpoint, model *string, maxTokens *int, timeout *time.Duration) {
	endpoint = set.String("endpoint", defaultEndpoint, "OpenAI-compatible Gateway URL")
	model = set.String("model", defaultModel, "model name")
	maxTokens = set.Int("max-tokens", client.DefaultMaxTokens, "maximum generated tokens")
	timeout = set.Duration("timeout", 90*time.Second, "request timeout")
	return endpoint, model, maxTokens, timeout
}

func newClient(endpoint, model string, maxTokens int, timeout time.Duration) (*client.Client, error) {
	return client.New(client.Config{Endpoint: endpoint, Model: model, MaxTokens: maxTokens, RequestTimeout: timeout, APIKey: os.Getenv("FISHMESH_API_KEY")}, client.Dependencies{})
}

func runRequest(arguments []string, output, diagnostics io.Writer) error {
	set := flag.NewFlagSet("request", flag.ContinueOnError)
	set.SetOutput(diagnostics)
	endpoint, model, maxTokens, timeout := commonFlags(set)
	prompt := set.String("prompt", "", "required user prompt")
	system := set.String("system", "", "optional system prompt")
	prefixKey := set.String("prefix-key", "", "optional compatibility session hint")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *prompt == "" {
		return errors.New("--prompt is required")
	}
	service, err := newClient(*endpoint, *model, *maxTokens, *timeout)
	if err != nil {
		return err
	}
	messages := make([]client.Message, 0, 2)
	if *system != "" {
		messages = append(messages, client.Message{Role: client.RoleSystem, Content: *system})
	}
	messages = append(messages, client.Message{Role: client.RoleUser, Content: *prompt})
	result, err := service.Send(context.Background(), client.Request{Messages: messages, PrefixKey: *prefixKey, StreamOutput: output})
	fmt.Fprintln(output)
	fmt.Fprintf(diagnostics, "ttft=%s duration=%s %s\n", result.TTFT.Round(time.Millisecond), result.Duration.Round(time.Millisecond), result.Headers.String())
	return err
}

func runChat(arguments []string, input io.Reader, output, diagnostics io.Writer) error {
	set := flag.NewFlagSet("chat", flag.ContinueOnError)
	set.SetOutput(diagnostics)
	endpoint, model, maxTokens, timeout := commonFlags(set)
	historyPath := set.String("history", "", "required local history JSON path")
	system := set.String("system", "", "initial system prompt for an empty history")
	prefixKey := set.String("prefix-key", "", "optional compatibility session hint")
	clear := set.Bool("clear", false, "remove the selected history and exit")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *historyPath == "" {
		return errors.New("--history is required")
	}
	if *clear {
		return client.ClearHistory(*historyPath)
	}
	messages, err := client.LoadHistory(*historyPath)
	if err != nil {
		return err
	}
	if len(messages) == 0 && *system != "" {
		messages = append(messages, client.Message{Role: client.RoleSystem, Content: *system})
	}
	service, err := newClient(*endpoint, *model, *maxTokens, *timeout)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	fmt.Fprintln(diagnostics, "Enter a message per line. EOF ends the conversation.")
	for scanner.Scan() {
		prompt := scanner.Text()
		if prompt == "" {
			continue
		}
		requestMessages := append(append([]client.Message(nil), messages...), client.Message{Role: client.RoleUser, Content: prompt})
		result, requestErr := service.Send(context.Background(), client.Request{Messages: requestMessages, PrefixKey: *prefixKey, StreamOutput: output})
		fmt.Fprintln(output)
		fmt.Fprintf(diagnostics, "ttft=%s duration=%s %s\n", result.TTFT.Round(time.Millisecond), result.Duration.Round(time.Millisecond), result.Headers.String())
		if requestErr != nil {
			return requestErr
		}
		messages = append(requestMessages, client.Message{Role: client.RoleAssistant, Content: result.Text})
		if err := client.SaveHistory(*historyPath, messages); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func runBenchmark(arguments []string, diagnostics io.Writer) error {
	defaults := client.DefaultBenchmarkConfig()
	set := flag.NewFlagSet("bench", flag.ContinueOnError)
	set.SetOutput(diagnostics)
	endpoint, model, maxTokens, timeout := commonFlags(set)
	mode := set.String("mode", string(defaults.Mode), "uniform|shared-prefix|hot-prefix|conversation")
	requests := set.Int("requests", defaults.Requests, "number of requests")
	concurrency := set.Int("concurrency", defaults.Concurrency, "parallel requests; over 4 requires opt-in")
	prefixGroups := set.Int("prefix-groups", defaults.PrefixGroups, "shared-prefix groups")
	prefixBytes := set.Int("prefix-bytes", defaults.PrefixBytes, "generated prefix bytes")
	hotRatio := set.Int("hot-prefix-ratio", 80, "percentage routed to hot prefix group")
	allowHigh := set.Bool("allow-high-concurrency", false, "acknowledge GPU load above the safe default")
	resultPath := set.String("output", "", "required append-only JSONL result path")
	runID := set.String("run-id", "", "optional result run ID")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *resultPath == "" {
		return errors.New("--output is required")
	}
	service, err := newClient(*endpoint, *model, *maxTokens, *timeout)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*resultPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open benchmark output: %w", err)
	}
	defer file.Close()
	summary, err := service.Benchmark(context.Background(), client.BenchmarkConfig{RunID: *runID, Mode: client.BenchmarkMode(*mode), Requests: *requests, Concurrency: *concurrency, PrefixGroups: *prefixGroups, PrefixBytes: *prefixBytes, HotPrefixRatio: *hotRatio, MaxTokens: *maxTokens, RequestTimeout: *timeout, AllowHighConcurrency: *allowHigh}, file)
	fmt.Fprintf(diagnostics, "completed=%d succeeded=%d failed=%d ttft_p50=%.2fms ttft_p95=%.2fms cached_prefix_samples=%d cached_prefix_sum=%d\n", summary.Completed, summary.Succeeded, summary.Failed, summary.TTFTP50MS, summary.TTFTP95MS, summary.CachedPrefixSamples, summary.CachedPrefixSum)
	return err
}
