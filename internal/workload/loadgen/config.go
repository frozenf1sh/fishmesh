package loadgen

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config defines a deterministic workload. Prefix group membership is the only source
// of shared prompt state, making later routing-policy comparisons reproducible.
type Config struct {
	Endpoint        string
	Model           string
	Requests        int
	Concurrency     int
	PrefixGroups    int
	PrefixBytes     int
	PrefixNamespace string
	HotPrefixRatio  int
	MaxTokens       int
	RequestTimeout  time.Duration
	KeepAlive       bool
	OutputPath      string
}

func (c Config) Validate() error {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return fmt.Errorf("endpoint must be an absolute HTTP URL: %q", c.Endpoint)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("endpoint scheme must be http or https")
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("model must not be empty")
	}
	if c.Requests <= 0 || c.Concurrency <= 0 || c.PrefixGroups <= 0 || c.PrefixBytes < 128 || c.MaxTokens <= 0 {
		return fmt.Errorf("requests, concurrency, prefix-groups, max-tokens must be positive and prefix-bytes must be at least 128")
	}
	if c.HotPrefixRatio < 0 || c.HotPrefixRatio > 100 {
		return fmt.Errorf("hot-prefix-ratio must be between 0 and 100")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request timeout must be positive")
	}
	return nil
}
