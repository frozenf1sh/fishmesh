// fishmesh-client is a local OpenAI-compatible conversation and bounded benchmark client.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/workload/client"
)

const (
	defaultEndpoint = "http://127.0.0.1:8080"
	defaultModel    = "qwen2.5-0.5b-instruct"

	colorAuto   colorMode = "auto"
	colorAlways colorMode = "always"
	colorNever  colorMode = "never"

	ansiEscape = "\x1b["
	ansiReset  = ansiEscape + "0m"
	ansiCyan   = ansiEscape + "36m"
	ansiGreen  = ansiEscape + "32m"
	ansiYellow = ansiEscape + "33m"
	ansiBlue   = ansiEscape + "34m"
	ansiPurple = ansiEscape + "35m"
	ansiDim    = ansiEscape + "2m"

	defaultHistoryRelativeDir = ".local/state/fishmesh"
	defaultHistoryTimestamp   = "20060102T150405.000000000Z"
)

// colorMode 只控制面向人的诊断输出；机器可读输出永远不包含 ANSI 控制序列。
type colorMode string

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
	sessionKey := set.String("session-key", "", "optional compatibility session hint")
	color := set.String("color", string(colorAuto), "auto|always|never terminal diagnostic colors")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *prompt == "" {
		return errors.New("--prompt is required")
	}
	colorEnabled, err := colorForDiagnostics(*color, diagnostics)
	if err != nil {
		return err
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
	result, err := service.Send(context.Background(), client.Request{Messages: messages, SessionKey: *sessionKey, StreamOutput: output})
	fmt.Fprintln(output)
	fprintlnDiagnosticEvidence(diagnostics, result, colorEnabled)
	return err
}

func runChat(arguments []string, input io.Reader, output, diagnostics io.Writer) error {
	set := flag.NewFlagSet("chat", flag.ContinueOnError)
	set.SetOutput(diagnostics)
	endpoint, model, maxTokens, timeout := commonFlags(set)
	historyPath := set.String("history", "", "local history JSON path (default ~/.local/state/fishmesh/<timestamp>.json)")
	system := set.String("system", "", "initial system prompt for an empty history")
	sessionKey := set.String("session-key", "", "optional compatibility session hint")
	clear := set.Bool("clear", false, "remove the selected history and exit")
	color := set.String("color", string(colorAuto), "auto|always|never terminal diagnostic colors")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *clear && *historyPath == "" {
		return errors.New("--clear requires --history so an existing conversation is selected explicitly")
	}
	resolvedHistoryPath, err := resolveHistoryPath(*historyPath, time.Now())
	if err != nil {
		return err
	}
	if *clear {
		return client.ClearHistory(resolvedHistoryPath)
	}
	if *historyPath == "" {
		fmt.Fprintf(diagnostics, "using default history=%s\n", resolvedHistoryPath)
	}
	colorEnabled, err := colorForDiagnostics(*color, diagnostics)
	if err != nil {
		return err
	}
	messages, err := client.LoadHistory(resolvedHistoryPath)
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
		result, requestErr := service.Send(context.Background(), client.Request{Messages: requestMessages, SessionKey: *sessionKey, StreamOutput: output})
		fmt.Fprintln(output)
		fprintlnDiagnosticEvidence(diagnostics, result, colorEnabled)
		if requestErr != nil {
			return requestErr
		}
		messages = append(requestMessages, client.Message{Role: client.RoleAssistant, Content: result.Text})
		if err := client.SaveHistory(resolvedHistoryPath, messages); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// resolveHistoryPath keeps the CLI's implicit conversation state in the user's private state directory.
// A timestamped file makes each invocation independent unless the user explicitly supplies --history.
func resolveHistoryPath(path string, now time.Time) (string, error) {
	if path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve default history path: %w", err)
	}
	return filepath.Join(home, defaultHistoryRelativeDir, now.UTC().Format(defaultHistoryTimestamp)+".json"), nil
}

// colorForDiagnostics 默认不向重定向 stderr 或测试 writer 写终端控制序列，避免污染日志和脚本输入。
func colorForDiagnostics(raw string, diagnostics io.Writer) (bool, error) {
	mode, err := parseColorMode(raw)
	if err != nil {
		return false, err
	}
	switch mode {
	case colorAlways:
		return true, nil
	case colorNever:
		return false, nil
	default:
		file, ok := diagnostics.(*os.File)
		if !ok {
			return false, nil
		}
		info, err := file.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0, nil
	}
}

// parseColorMode 只接受固定 CLI 枚举，避免由任意字符串触发不可预测的 ANSI 行为。
func parseColorMode(raw string) (colorMode, error) {
	mode := colorMode(raw)
	switch mode {
	case colorAuto, colorAlways, colorNever:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported --color value %q (use auto, always, or never)", raw)
	}
}

// fprintlnDiagnosticEvidence 将模型正文和精简的 allowlist 路由证据分写，流式正文不混入控制信息。
func fprintlnDiagnosticEvidence(diagnostics io.Writer, result client.Result, color bool) {
	fmt.Fprintln(diagnostics, formatDiagnosticEvidence(result, color))
}

// formatDiagnosticEvidence 只突出人类排障关键值，不改变固定 header allowlist 或 JSONL schema。
func formatDiagnosticEvidence(result client.Result, color bool) string {
	headers := result.Headers
	return fmt.Sprintf("ttft=%s duration=%s routing_mode=%s route_reason=%s policy=%s kv_status=%s cached_prefix_tokens=%d backend_id=%s preferred_backend_id=%s upstream=%s spillover_reason=%s",
		colorize(result.TTFT.Round(time.Millisecond).String(), ansiCyan, color),
		colorize(result.Duration.Round(time.Millisecond).String(), ansiDim, color),
		colorize(headers.RoutingMode, ansiBlue, color),
		colorize(headers.RouteReason, ansiYellow, color),
		colorize(headers.Policy, ansiGreen, color),
		colorize(headers.KVStatus, ansiYellow, color),
		headers.CachedPrefixTokens,
		colorize(headers.BackendID, ansiPurple, color), headers.PreferredBackendID, headers.Upstream, headers.SpilloverReason)
}

func colorize(value, sequence string, enabled bool) string {
	if !enabled || value == "" {
		return value
	}
	return sequence + value + ansiReset
}

func runBenchmark(arguments []string, diagnostics io.Writer) error {
	set := flag.NewFlagSet("bench", flag.ContinueOnError)
	set.SetOutput(diagnostics)
	endpoint := set.String("endpoint", defaultEndpoint, "OpenAI-compatible Gateway URL")
	model := set.String("model", defaultModel, "model name")
	maxTokens := set.Int("max-tokens", 0, "override plan max generated tokens")
	timeout := set.Duration("timeout", 0, "override plan request timeout")
	planPath := set.String("plan", "", "JSON benchmark plan; omitted uses the built-in final matrix")
	outputDir := set.String("output-dir", "", "report directory; default artifacts/bench/<run-id>")
	runID := set.String("run-id", "", "override the plan run ID")
	allowHigh := set.Bool("allow-high-concurrency", false, "acknowledge high concurrency declared by the plan")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	var plan client.BenchmarkPlan
	var err error
	if *planPath == "" {
		plan = client.DefaultBenchmarkPlan()
	} else {
		plan, err = client.LoadBenchmarkPlan(*planPath)
		if err != nil {
			return err
		}
	}
	if *maxTokens > 0 {
		plan.MaxTokens = *maxTokens
	}
	if *timeout > 0 {
		plan.RequestTimeoutMS = int((*timeout) / time.Millisecond)
	}
	if *runID != "" {
		plan.RunID = *runID
	}
	if plan.RunID == "" {
		plan.RunID = time.Now().UTC().Format(defaultHistoryTimestamp)
	}
	if *allowHigh {
		plan.AllowHighConcurrency = true
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	requestTimeout := time.Duration(plan.RequestTimeoutMS) * time.Millisecond
	service, err := newClient(*endpoint, *model, plan.MaxTokens, requestTimeout)
	if err != nil {
		return err
	}
	if *outputDir == "" {
		*outputDir = filepath.Join("artifacts", "bench", plan.RunID)
	}
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		return fmt.Errorf("create benchmark output directory: %w", err)
	}
	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode benchmark plan: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, "plan.json"), planJSON, 0o644); err != nil {
		return fmt.Errorf("write benchmark plan: %w", err)
	}
	requestsPath := filepath.Join(*outputDir, "requests.jsonl")
	file, err := os.OpenFile(requestsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open benchmark requests: %w", err)
	}
	report, runErr := service.RunPlan(context.Background(), plan, file)
	closeErr := file.Close()
	if runErr == nil && closeErr != nil {
		runErr = fmt.Errorf("close benchmark requests: %w", closeErr)
	}
	reportJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode benchmark report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, "report.json"), reportJSON, 0o644); err != nil {
		return fmt.Errorf("write benchmark report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, "report.md"), []byte(report.Markdown()), 0o644); err != nil {
		return fmt.Errorf("write benchmark markdown report: %w", err)
	}
	fmt.Fprintf(diagnostics, "run_id=%s output_dir=%s completed=%d succeeded=%d failed=%d ttft_p50=%.2fms ttft_p95=%.2fms report=%s\n", report.RunID, *outputDir, report.Completed, report.Succeeded, report.Failed, report.TTFTP50MS, report.TTFTP95MS, filepath.Join(*outputDir, "report.md"))
	return runErr
}
