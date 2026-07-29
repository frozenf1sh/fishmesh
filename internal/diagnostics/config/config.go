package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultListenAddress = ":8090"
	defaultMode          = "demo"
)

// Config 是 analyst 控制面自身的运行时配置。LLM API key 等敏感信息不在
// MVP 配置中，未来通过 Secret 注入专用 LLMClient，而不是写入 ConfigMap。
type Config struct {
	ListenAddress       string
	Mode                string
	GatewayMetricsURL   string
	VLLMMetricsURLs     []string
	GPUMetricsURL       string
	KubernetesNamespace string
	KubernetesAPIURL    string
	KubernetesTokenFile string
	KubernetesCAFile    string
	RequestTimeout      time.Duration
	ShutdownTimeout     time.Duration
}

func LoadConfigFromEnvironment() (Config, error) {
	config := Config{
		ListenAddress:       valueOrDefault("FISHMESH_ANALYST_LISTEN_ADDRESS", defaultListenAddress),
		Mode:                valueOrDefault("FISHMESH_ANALYST_MODE", defaultMode),
		GatewayMetricsURL:   valueOrDefault("FISHMESH_GATEWAY_METRICS_URL", ""),
		VLLMMetricsURLs:     csvValues(os.Getenv("FISHMESH_VLLM_METRICS_URLS")),
		GPUMetricsURL:       valueOrDefault("FISHMESH_GPU_METRICS_URL", ""),
		KubernetesNamespace: valueOrDefault("FISHMESH_KUBERNETES_NAMESPACE", "default"),
		KubernetesAPIURL:    valueOrDefault("FISHMESH_KUBERNETES_API_URL", ""),
		KubernetesTokenFile: valueOrDefault("FISHMESH_KUBERNETES_TOKEN_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/token"),
		KubernetesCAFile:    valueOrDefault("FISHMESH_KUBERNETES_CA_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"),
		RequestTimeout:      durationOrDefault("FISHMESH_ANALYST_REQUEST_TIMEOUT", 5*time.Second),
		ShutdownTimeout:     durationOrDefault("FISHMESH_ANALYST_SHUTDOWN_TIMEOUT", 10*time.Second),
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddress) == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	if c.Mode != "demo" && c.Mode != "gateway" && c.Mode != "observability" {
		return fmt.Errorf("analyst mode must be demo, gateway, or observability: %q", c.Mode)
	}
	if c.Mode == "gateway" && strings.TrimSpace(c.GatewayMetricsURL) == "" {
		return fmt.Errorf("gateway mode requires FISHMESH_GATEWAY_METRICS_URL")
	}
	if c.RequestTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	return nil
}

func csvValues(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
