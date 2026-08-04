// Package llmd 负责把 llm-d 调度扩展点翻译为 FishMesh routing 输入，并提供 bounded-affinity 插件。
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
	PluginType = "fishmesh-bounded-affinity-scorer"

	HeaderRouteReason        = "x-fishmesh-route-reason"
	HeaderRoutingMode        = "x-fishmesh-routing-mode"
	HeaderBackendID          = "x-fishmesh-backend-id"
	HeaderSelectedBackendID  = "x-fishmesh-selected-backend-id"
	HeaderServedBackendID    = "x-fishmesh-served-backend-id"
	HeaderPreferredBackendID = "x-fishmesh-preferred-backend-id"
	HeaderPolicy             = "x-fishmesh-policy"
	HeaderSpilloverReason    = "x-fishmesh-spillover-reason"

	defaultRoutingKeyHeader = "x-fishmesh-prefix-key"
	defaultMetricsMaxAge    = 45 * time.Second
	decisionAttributePrefix = "fishmesh.io/bounded-affinity-decision/"
)

// Clock 为 llm-d metrics freshness 和 routing registry 提供同一时间源。
type Clock func() time.Time

// Config 包含 FishMesh scorer 自己拥有的 header、freshness 和有界亲和参数。
type Config struct {
	RoutingKeyHeader         string
	MetricsMaxAge            time.Duration
	InFlightLoadProducerName string
	BoundedAffinity          routing.BoundedAffinityConfig
	Clock                    Clock
}

type parameters struct {
	RoutingKeyHeader         string  `json:"routingKeyHeader"`
	MetricsMaxAge            string  `json:"metricsMaxAge"`
	InFlightLoadProducerName string  `json:"inFlightLoadProducerName,omitempty"`
	AffinityTTL              string  `json:"affinityTTL"`
	MaxAffinityEntries       int     `json:"maxAffinityEntries"`
	InflightDelta            int64   `json:"inflightDelta"`
	QueueDepthDelta          float64 `json:"queueDepthDelta"`
}

// DefaultConfig 返回与 standalone bounded-affinity 相同的策略阈值和观测时效。
func DefaultConfig() Config {
	return Config{
		RoutingKeyHeader: defaultRoutingKeyHeader,
		MetricsMaxAge:    defaultMetricsMaxAge,
		BoundedAffinity:  routing.DefaultBoundedAffinityConfig(),
		Clock:            time.Now,
	}
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
	if config.MetricsMaxAge <= 0 {
		return nil, fmt.Errorf("llm-d metrics max age must be positive")
	}
	strategy, err := routing.NewBoundedAffinity(config.BoundedAffinity)
	if err != nil {
		return nil, fmt.Errorf("llm-d bounded affinity: %w", err)
	}
	return newScorer(name, config, strategy), nil
}

func factory(name string, decoder *json.Decoder, _ llmdplugin.Handle) (llmdplugin.Plugin, error) {
	parameters := defaultParameters()
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
	defaults := DefaultConfig()
	config.RoutingKeyHeader = strings.ToLower(strings.TrimSpace(config.RoutingKeyHeader))
	if config.RoutingKeyHeader == "" {
		config.RoutingKeyHeader = defaults.RoutingKeyHeader
	}
	config.InFlightLoadProducerName = strings.TrimSpace(config.InFlightLoadProducerName)
	if config.Clock == nil {
		if config.BoundedAffinity.Clock != nil {
			config.Clock = config.BoundedAffinity.Clock
		} else {
			config.Clock = defaults.Clock
		}
	}
	// 两处时效判断必须共享同一时间源，否则测试时钟或时钟漂移会让
	// metrics freshness 与 affinity TTL 对同一次请求得出矛盾结论。
	config.BoundedAffinity.Clock = config.Clock
	return config
}

func defaultParameters() parameters {
	config := DefaultConfig()
	return parameters{
		RoutingKeyHeader:   config.RoutingKeyHeader,
		MetricsMaxAge:      config.MetricsMaxAge.String(),
		AffinityTTL:        config.BoundedAffinity.TTL.String(),
		MaxAffinityEntries: config.BoundedAffinity.MaxEntries,
		InflightDelta:      config.BoundedAffinity.InflightDelta,
		QueueDepthDelta:    config.BoundedAffinity.QueueDepthDelta,
	}
}

func (p parameters) config() (Config, error) {
	metricsMaxAge, err := time.ParseDuration(p.MetricsMaxAge)
	if err != nil {
		return Config{}, fmt.Errorf("parse llm-d metricsMaxAge %q: %w", p.MetricsMaxAge, err)
	}
	affinityTTL, err := time.ParseDuration(p.AffinityTTL)
	if err != nil {
		return Config{}, fmt.Errorf("parse llm-d affinityTTL %q: %w", p.AffinityTTL, err)
	}
	return Config{
		RoutingKeyHeader:         p.RoutingKeyHeader,
		MetricsMaxAge:            metricsMaxAge,
		InFlightLoadProducerName: p.InFlightLoadProducerName,
		BoundedAffinity: routing.BoundedAffinityConfig{
			TTL:             affinityTTL,
			MaxEntries:      p.MaxAffinityEntries,
			InflightDelta:   p.InflightDelta,
			QueueDepthDelta: p.QueueDepthDelta,
		},
	}, nil
}
