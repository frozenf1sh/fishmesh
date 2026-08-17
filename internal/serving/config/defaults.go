package config

import (
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/gateway"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

const (
	defaultServiceBackendID    = "service"
	defaultServiceURL          = "http://qwen-vllm-baseline.kubellm.svc.cluster.local:8000"
	defaultEndpointService     = "qwen-vllm"
	defaultEndpointNamespace   = "kubellm"
	defaultKubernetesTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultKubernetesCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultKVAwareModel        = "qwen2.5-0.5b-instruct"
	defaultMetricsPath         = "/metrics"
)

// DefaultConfig 是 standalone Serving 的唯一产品默认配置入口。
//
// 各 domain 只定义配置类型、校验规则和运行时依赖；它们不再自行决定
// 产品阈值、服务地址或资源上限。环境变量加载器也从这里取 fallback，
// 因而“未配置时实际运行什么”只需要在一个地方回答。
func DefaultConfig() Config {
	service := backend.Backend{ID: defaultServiceBackendID, URL: defaultServiceURL}
	return Config{
		Process: ProcessConfig{
			ListenAddress:     ":8080",
			ReadHeaderTimeout: 5 * time.Second,
			ShutdownTimeout:   30 * time.Second,
		},
		Gateway: gateway.Config{
			RoutingMode:         routing.ModeLoadAware,
			KeepAlive:           false,
			RequestTimeout:      90 * time.Second,
			MaxRequestBodyBytes: 2 << 20,
		},
		Discovery: discovery.Config{
			Mode:   discovery.ModeStatic,
			Static: []backend.Backend{service},
			EndpointSlice: discovery.EndpointSliceConfig{
				Namespace:       defaultEndpointNamespace,
				ServiceName:     defaultEndpointService,
				TokenFile:       defaultKubernetesTokenFile,
				CAFile:          defaultKubernetesCAFile,
				RefreshInterval: 30 * time.Second,
			},
		},
		Identity:        identityConfig(),
		ObservationMode: observation.ModeNone,
		Observation: observation.Config{
			Interval:       15 * time.Second,
			MaxAge:         45 * time.Second,
			RequestTimeout: 5 * time.Second,
			Clock:          time.Now,
		},
		Prometheus: observation.PrometheusConfig{MetricsPath: defaultMetricsPath, Clock: time.Now},
		Routing: routing.Config{
			Mode:    routing.ModeLoadAware,
			Service: service,
			SessionKey: routing.SessionKeyConfig{
				TTL:             5 * time.Minute,
				MaxEntries:      10_000,
				InflightDelta:   2,
				QueueDepthDelta: 1,
				Clock:           time.Now,
			},
			KVAware: routing.KVAwareConfig{
				EstimatorMode:        routing.KVAwareEstimatorTokenCost,
				QueueTokenPenalty:    512,
				RunningTokenPenalty:  128,
				InflightTokenPenalty: 64,
			},
		},
		Circuit: circuit.Config{
			EWMAAlpha:       0.5,
			ErrorThreshold:  0.6,
			MinimumRequests: 3,
			OpenDuration:    10 * time.Second,
			Clock:           time.Now,
		},
		Admission: admission.Config{MaxInflight: 128, InitialTarget: 128},
		AdmissionTuning: admission.TuningConfig{
			Mode: admission.TuningOff, MinTarget: 16, MaxTarget: 128, Step: 8,
			Interval: 2 * time.Second, Cooldown: 5 * time.Second, LowWatermark: 0.25, HighWatermark: 0.8,
		},
		Transport: transport.Config{
			KeepAlive:       false,
			RequestTimeout:  90 * time.Second,
			MaxConnsPerHost: 32,
			IdleConnTimeout: 90 * time.Second,
		},
		RequestPath: requestpath.Config{
			Service:           service,
			DiscoveryMaxAge:   90 * time.Second,
			ShortPromptTokens: 0,
			// Disabled by default for compatibility. KV-aware deployment profiles
			// must choose explicit thresholds for their tested capacity.
			HardQueueDepth:    0,
			HardLocalInflight: 0,
		},
		Tokenization: tokenization.Config{
			BaseURL:          defaultServiceURL,
			Model:            defaultKVAwareModel,
			Timeout:          5 * time.Second,
			MaxRequestBytes:  2 << 20,
			MaxResponseBytes: 8 << 20,
			MaxTotalTokens:   131072,
		},
		KVCache: kvcache.Config{
			BlockSizeTokens:   16,
			MaxIndexKeys:      100_000,
			MaxInstances:      8,
			MaxBackendsPerKey: 8,
			MaxEventBytes:     4 << 20,
			MaxReplayEvents:   4096,
			MaxQueryTokens:    131072,
			MaxCacheSaltBytes: 1024,
			ReplayPeriod:      2 * time.Second,
			ReplayTimeout:     3 * time.Second,
			FreshnessTTL:      5 * time.Second,
			ReconnectDelay:    time.Second,
		},
		Prediction: prediction.Config{
			Mode:           prediction.ModeOff,
			MaxSamples:     128,
			MaxSampleAge:   15 * time.Minute,
			MinimumSamples: 16,
			RefitEvery:     16,
			Clock:          time.Now,
		},
	}
}

func identityConfig() identity.Config {
	return identity.Config{
		Namespace: defaultEndpointNamespace,
		TokenFile: defaultKubernetesTokenFile,
		CAFile:    defaultKubernetesCAFile,
	}
}
