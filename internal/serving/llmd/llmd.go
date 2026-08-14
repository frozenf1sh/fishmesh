// Package llmd 负责把 llm-d 调度扩展点翻译为 FishMesh routing 输入，并保留冻结的 session-key 兼容插件。
package llmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"

	llmdplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
)

const (
	PluginType = "fishmesh-session-key-scorer"

	HeaderRouteReason        = "x-fishmesh-route-reason"
	HeaderRoutingMode        = "x-fishmesh-routing-mode"
	HeaderBackendID          = "x-fishmesh-backend-id"
	HeaderSelectedBackendID  = "x-fishmesh-selected-backend-id"
	HeaderServedBackendID    = "x-fishmesh-served-backend-id"
	HeaderPreferredBackendID = "x-fishmesh-preferred-backend-id"
	HeaderPolicy             = "x-fishmesh-policy"
	HeaderSpilloverReason    = "x-fishmesh-spillover-reason"

	decisionAttributePrefix = "fishmesh.io/session-key-decision/"
)

// Clock 为 llm-d metrics freshness 和 routing registry 提供同一时间源。
type Clock func() time.Time

// Config 包含 FishMesh scorer 自己拥有的 header、freshness 和有界亲和参数。
type Config struct {
	SessionKeyHeader         string
	MetricsMaxAge            time.Duration
	InFlightLoadProducerName string
	SessionKey               routing.SessionKeyConfig
	Clock                    Clock
}

type parameters struct {
	SessionKeyHeader         string  `json:"sessionKeyHeader"`
	MetricsMaxAge            string  `json:"metricsMaxAge"`
	InFlightLoadProducerName string  `json:"inFlightLoadProducerName,omitempty"`
	SessionKeyTTL            string  `json:"sessionKeyTTL"`
	MaxSessionKeyEntries     int     `json:"maxSessionKeyEntries"`
	InflightDelta            int64   `json:"inflightDelta"`
	QueueDepthDelta          float64 `json:"queueDepthDelta"`
}

// Register 把 FishMesh factory 注册到 llm-d 的进程级插件表；只应在组合根启动时调用。
func Register() {
	llmdplugin.Register(PluginType, factory)
}

// New 创建一个实现 llm-d Filter、Scorer、ConsumerPlugin 和 ResponseHeaderProcessor 的插件。
func New(name string, config Config) (llmdplugin.Plugin, error) {
	config = normalizeConfig(config)
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("llm-d plugin name must not be empty")
	}
	if config.SessionKeyHeader == "" {
		return nil, fmt.Errorf("llm-d session-key header must not be empty")
	}
	if config.MetricsMaxAge <= 0 {
		return nil, fmt.Errorf("llm-d metrics max age must be positive")
	}
	strategy, err := routing.NewSessionKey(config.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("llm-d session-key: %w", err)
	}
	return newScorer(name, config, strategy), nil
}

func factory(name string, decoder *json.Decoder, _ llmdplugin.Handle) (llmdplugin.Plugin, error) {
	var parameters parameters
	if decoder != nil {
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&parameters); err != nil {
			return nil, fmt.Errorf("decode %s parameters: %w", PluginType, err)
		}
	}
	config, err := parameters.config()
	if err != nil {
		return nil, err
	}
	return New(name, config)
}

func normalizeConfig(config Config) Config {
	config.SessionKeyHeader = strings.ToLower(strings.TrimSpace(config.SessionKeyHeader))
	config.InFlightLoadProducerName = strings.TrimSpace(config.InFlightLoadProducerName)
	if config.Clock == nil {
		if config.SessionKey.Clock != nil {
			config.Clock = config.SessionKey.Clock
		} else {
			config.Clock = time.Now
		}
	}
	// 两处时效判断必须共享同一时间源，否则测试时钟或时钟漂移会让
	// metrics freshness 与 session-key TTL 对同一次请求得出矛盾结论。
	config.SessionKey.Clock = config.Clock
	return config
}

func (p parameters) config() (Config, error) {
	metricsMaxAge, err := time.ParseDuration(p.MetricsMaxAge)
	if err != nil {
		return Config{}, fmt.Errorf("parse llm-d metricsMaxAge %q: %w", p.MetricsMaxAge, err)
	}
	sessionKeyTTL, err := time.ParseDuration(p.SessionKeyTTL)
	if err != nil {
		return Config{}, fmt.Errorf("parse llm-d sessionKeyTTL %q: %w", p.SessionKeyTTL, err)
	}
	return Config{
		SessionKeyHeader:         p.SessionKeyHeader,
		MetricsMaxAge:            metricsMaxAge,
		InFlightLoadProducerName: p.InFlightLoadProducerName,
		SessionKey: routing.SessionKeyConfig{
			TTL:             sessionKeyTTL,
			MaxEntries:      p.MaxSessionKeyEntries,
			InflightDelta:   p.InflightDelta,
			QueueDepthDelta: p.QueueDepthDelta,
		},
	}, nil
}
