package kvcache

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	zmqEndpointScheme = "tcp"
)

// Clock 提供 freshness 时间，测试可以注入确定性时钟。
type Clock func() time.Time

// Config 定义本地 KV index、事件/replay 和查询的全部资源边界。
type Config struct {
	BlockSizeTokens   int
	HashSeed          string
	MaxIndexKeys      int
	MaxInstances      int
	MaxBackendsPerKey int
	MaxEventBytes     int
	MaxReplayEvents   int
	MaxQueryTokens    int
	MaxCacheSaltBytes int
	ReplayPeriod      time.Duration
	ReplayTimeout     time.Duration
	FreshnessTTL      time.Duration
	ReconnectDelay    time.Duration
}

// Dependencies 是组合根注入的 transport 与时钟。
type Dependencies struct {
	EventSource   EventSource
	Clock         Clock
	EventObserver EventObserver
}

// Validate 检查 KV index 创建所需的事件源；时钟为空时由构造函数补入默认实现。
func (d Dependencies) Validate() error {
	if d.EventSource == nil {
		return fmt.Errorf("kvcache event source must not be nil")
	}
	return nil
}

// Validate 检查所有状态、buffer 和 freshness 边界是否明确且可运行。
func (c Config) Validate() error {
	if c.BlockSizeTokens <= 0 || c.MaxIndexKeys <= 0 || c.MaxInstances <= 0 || c.MaxBackendsPerKey <= 0 {
		return errors.New("block and index limits must be positive")
	}
	if c.MaxBackendsPerKey < c.MaxInstances {
		return errors.New("backends per key must cover every configured instance")
	}
	if c.MaxEventBytes <= 0 || c.MaxReplayEvents <= 0 || c.MaxQueryTokens <= 0 || c.MaxCacheSaltBytes <= 0 {
		return errors.New("event, replay and query limits must be positive")
	}
	if c.ReplayPeriod <= 0 || c.ReplayTimeout <= 0 || c.FreshnessTTL <= c.ReplayPeriod || c.ReconnectDelay <= 0 {
		return errors.New("replay, freshness and reconnect durations are invalid")
	}
	return nil
}

func validateZMQEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != zmqEndpointScheme || parsed.Host == "" {
		return fmt.Errorf("must be an absolute tcp endpoint: %q", raw)
	}
	return nil
}
