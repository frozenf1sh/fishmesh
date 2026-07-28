package gateway

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultListenAddress = ":8080"
	defaultUpstreamURL   = "http://qwen-vllm-baseline.kubellm.svc.cluster.local:8000"
)

// Config contains every runtime setting for the Gateway. Configuration is intentionally
// environment-only so the same image can run locally and in Kubernetes.
type Config struct {
	ListenAddress   string
	UpstreamURL     string
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

func LoadConfigFromEnvironment() (Config, error) {
	config := Config{
		ListenAddress:   valueOrDefault("FISHMESH_LISTEN_ADDRESS", defaultListenAddress),
		UpstreamURL:     valueOrDefault("FISHMESH_UPSTREAM_URL", defaultUpstreamURL),
		RequestTimeout:  durationOrDefault("FISHMESH_REQUEST_TIMEOUT", 90*time.Second),
		ShutdownTimeout: durationOrDefault("FISHMESH_SHUTDOWN_TIMEOUT", 30*time.Second),
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddress) == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	upstream, err := url.Parse(c.UpstreamURL)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return fmt.Errorf("upstream URL must be an absolute HTTP URL: %q", c.UpstreamURL)
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return fmt.Errorf("upstream URL scheme must be http or https: %q", upstream.Scheme)
	}
	if c.RequestTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	return nil
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
