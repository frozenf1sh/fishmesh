package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

type environmentValues struct {
	endpointRefresh, endpointMaxAge                 time.Duration
	observationInterval, observationMaxAge          time.Duration
	requestTimeout, shutdownTimeout                 time.Duration
	maxRequestBody                                  int64
	affinityTTL, circuitOpenDuration                time.Duration
	keepAlive                                       bool
	affinityMaxEntries, maxInflight, maxConnections int
	circuitMinimumRequests                          int
	affinityInflightDelta                           int64
	exactQueueTokenPenalty                          int64
	exactRunningTokenPenalty                        int64
	exactInflightTokenPenalty                       int64
	affinityQueueDelta, circuitAlpha, circuitError  float64
}

// LoadEnvironment 读取全部 FISHMESH_* 配置并返回按 domain 拆分的结果。
func LoadEnvironment() (Config, error) {
	values, err := loadEnvironmentValues()
	if err != nil {
		return Config{}, err
	}
	config, err := values.buildConfig()
	if err != nil {
		return Config{}, err
	}
	return config, config.Validate()
}

func loadEnvironmentValues() (environmentValues, error) {
	values := environmentValues{}
	exactDefaults := routing.DefaultExactCacheLoadConfig()
	parsers := []func() error{
		func() error {
			return assignDuration(envEndpointRefreshInterval, defaultEndpointRefresh, &values.endpointRefresh)
		},
		func() error { return assignDuration(envEndpointMaxAge, defaultEndpointMaxAge, &values.endpointMaxAge) },
		func() error {
			return assignDuration(envObservationInterval, defaultObservationInterval, &values.observationInterval)
		},
		func() error {
			return assignDuration(envObservationMaxAge, defaultObservationMaxAge, &values.observationMaxAge)
		},
		func() error { return assignDuration(envRequestTimeout, defaultRequestTimeout, &values.requestTimeout) },
		func() error {
			return assignInt64(envMaxRequestBodyBytes, defaultMaxRequestBodyBytes, &values.maxRequestBody)
		},
		func() error {
			return assignDuration(envShutdownTimeout, defaultShutdownTimeout, &values.shutdownTimeout)
		},
		func() error { return assignDuration(envAffinityTTL, defaultAffinityTTL, &values.affinityTTL) },
		func() error {
			return assignDuration(envCircuitOpenDuration, defaultCircuitOpenDuration, &values.circuitOpenDuration)
		},
		func() error { return assignBool(envUpstreamKeepAlive, false, &values.keepAlive) },
		func() error {
			return assignInt(envAffinityMaxEntries, defaultAffinityMaxEntries, false, &values.affinityMaxEntries)
		},
		func() error {
			return assignInt(envMaxInflightRequests, defaultMaxInflightRequests, true, &values.maxInflight)
		},
		func() error {
			return assignInt(envMaxConnsPerHost, defaultMaxConnsPerHost, true, &values.maxConnections)
		},
		func() error {
			return assignInt(envCircuitMinimumRequests, defaultCircuitMinimumRequests, true, &values.circuitMinimumRequests)
		},
		func() error {
			return assignInt64(envAffinityInflightDelta, defaultAffinityInflightDelta, &values.affinityInflightDelta)
		},
		func() error {
			return assignFloat(envAffinityQueueDepthDelta, defaultAffinityQueueDepthDelta, &values.affinityQueueDelta)
		},
		func() error {
			return assignNonNegativeInt64(envExactQueueTokenPenalty, exactDefaults.QueueTokenPenalty, &values.exactQueueTokenPenalty)
		},
		func() error {
			return assignNonNegativeInt64(envExactRunningTokenPenalty, exactDefaults.RunningTokenPenalty, &values.exactRunningTokenPenalty)
		},
		func() error {
			return assignNonNegativeInt64(envExactInflightTokenPenalty, exactDefaults.InflightTokenPenalty, &values.exactInflightTokenPenalty)
		},
		func() error { return assignFloat(envCircuitEWMAAlpha, defaultCircuitEWMAAlpha, &values.circuitAlpha) },
		func() error {
			return assignFloat(envCircuitErrorThreshold, defaultCircuitErrorThreshold, &values.circuitError)
		},
	}
	for _, parse := range parsers {
		if err := parse(); err != nil {
			return environmentValues{}, err
		}
	}
	if values.circuitAlpha <= 0 || values.circuitAlpha > 1 || values.circuitError <= 0 || values.circuitError > 1 || values.circuitOpenDuration <= 0 {
		return environmentValues{}, fmt.Errorf("circuit alpha, threshold and open duration must be positive and bounded")
	}
	if values.maxRequestBody <= 0 {
		return environmentValues{}, fmt.Errorf("%s must be positive", envMaxRequestBodyBytes)
	}
	return values, nil
}

func (v environmentValues) buildConfig() (Config, error) {
	predictionDefaults := prediction.DefaultConfig()
	service := backend.Backend{ID: serviceBackendID, URL: valueOrDefault(envUpstreamURL, defaultUpstreamURL)}
	if err := service.Validate(); err != nil {
		return Config{}, err
	}
	staticBackends, err := staticBackendsFromEnvironment()
	if err != nil {
		return Config{}, err
	}
	discoveryMode := discovery.Mode(valueOrDefault(envEndpointDiscovery, string(discovery.ModeStatic)))
	routingMode := routing.Mode(valueOrDefault(envRoutingMode, string(defaultRoutingMode)))
	if discoveryMode == discovery.ModeStatic && routingMode == routing.ModeService && len(staticBackends) == 0 {
		staticBackends = []backend.Backend{service}
	}
	reconcileInterval := time.Duration(0)
	if discoveryMode == discovery.ModeEndpointSlice {
		reconcileInterval = v.endpointRefresh
	}
	namespace := valueOrDefault(envEndpointNamespace, defaultEndpointNamespace)
	apiURL := strings.TrimSpace(os.Getenv(envKubernetesAPIURL))
	tokenFile := valueOrDefault(envKubernetesTokenFile, defaultKubernetesTokenFile)
	caFile := valueOrDefault(envKubernetesCAFile, defaultKubernetesCAFile)
	config := Config{
		Process: ProcessConfig{ListenAddress: valueOrDefault(envListenAddress, defaultListenAddress), ReadHeaderTimeout: defaultReadHeaderTimeout, ShutdownTimeout: v.shutdownTimeout},
		Gateway: gateway.Config{RoutingMode: routingMode, KeepAlive: v.keepAlive, RequestTimeout: v.requestTimeout, MaxRequestBodyBytes: v.maxRequestBody},
		Discovery: discovery.Config{Mode: discoveryMode, Static: staticBackends, EndpointSlice: discovery.EndpointSliceConfig{
			Namespace: namespace, ServiceName: valueOrDefault(envEndpointService, defaultEndpointService), BaseURL: apiURL,
			TokenFile: tokenFile, CAFile: caFile, RefreshInterval: v.endpointRefresh,
		}},
		Identity:        identity.Config{Namespace: namespace, BaseURL: apiURL, TokenFile: tokenFile, CAFile: caFile},
		ObservationMode: observation.Mode(valueOrDefault(envObservationMode, string(observation.ModeNone))),
		Observation:     observation.Config{Interval: v.observationInterval, MaxAge: v.observationMaxAge},
		Prometheus:      observation.PrometheusConfig{},
		Routing: routing.Config{Mode: routingMode, Service: service, BoundedAffinity: routing.BoundedAffinityConfig{
			TTL: v.affinityTTL, MaxEntries: v.affinityMaxEntries, InflightDelta: v.affinityInflightDelta, QueueDepthDelta: v.affinityQueueDelta,
		}, ExactCacheLoad: routing.ExactCacheLoadConfig{
			QueueTokenPenalty: v.exactQueueTokenPenalty, RunningTokenPenalty: v.exactRunningTokenPenalty, InflightTokenPenalty: v.exactInflightTokenPenalty,
		}},
		Circuit:      circuit.Config{EWMAAlpha: v.circuitAlpha, ErrorThreshold: v.circuitError, MinimumRequests: v.circuitMinimumRequests, OpenDuration: v.circuitOpenDuration},
		Admission:    admission.Config{MaxInflight: v.maxInflight},
		Transport:    transport.Config{KeepAlive: v.keepAlive, RequestTimeout: v.requestTimeout, MaxConnsPerHost: v.maxConnections},
		RequestPath:  requestpath.Config{Service: service, RequireFreshDiscovery: discoveryMode == discovery.ModeEndpointSlice, DiscoveryMaxAge: v.endpointMaxAge, ReconcileInterval: reconcileInterval},
		Tokenization: tokenization.DefaultConfig(valueOrDefault(envExactRenderURL, defaultExactRenderURL), valueOrDefault(envExactModel, defaultExactModel)),
		KVCache:      kvcache.DefaultConfig(),
		Prediction: prediction.Config{
			Mode: prediction.Mode(valueOrDefault(envPredictionMode, string(prediction.ModeOff))), MaxSamples: predictionDefaults.MaxSamples,
			MaxSampleAge: predictionDefaults.MaxSampleAge, MinimumSamples: predictionDefaults.MinimumSamples,
		},
	}
	return config, nil
}

func staticBackendsFromEnvironment() ([]backend.Backend, error) {
	value := os.Getenv(envBackendEndpoints)
	if strings.TrimSpace(value) == "" {
		value = os.Getenv(envLegacyPrefixEndpoints)
	}
	items := csvValues(value)
	backends := make([]backend.Backend, 0, len(items))
	for index, rawURL := range items {
		candidate := backend.Backend{ID: backend.ID(fmt.Sprintf("backend-%d", index)), URL: rawURL}
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		backends = append(backends, candidate)
	}
	return backends, nil
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func assignDuration(key string, fallback time.Duration, destination *time.Duration) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		*destination = fallback
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a duration: %q: %w", key, value, err)
	}
	*destination = parsed
	return nil
}

func assignBool(key string, fallback bool, destination *bool) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		*destination = fallback
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%s must be a boolean: %q: %w", key, value, err)
	}
	*destination = parsed
	return nil
}

func assignInt(key string, fallback int, positive bool, destination *int) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		*destination = fallback
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %q: %w", key, value, err)
	}
	if positive && parsed <= 0 {
		return fmt.Errorf("%s must be positive: %d", key, parsed)
	}
	*destination = parsed
	return nil
}

func assignInt64(key string, fallback int64, destination *int64) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		*destination = fallback
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %q: %w", key, value, err)
	}
	*destination = parsed
	return nil
}

func assignNonNegativeInt64(key string, fallback int64, destination *int64) error {
	if err := assignInt64(key, fallback, destination); err != nil {
		return err
	}
	if *destination < 0 {
		return fmt.Errorf("%s must not be negative: %d", key, *destination)
	}
	return nil
}

func assignFloat(key string, fallback float64, destination *float64) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		*destination = fallback
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%s must be a number: %q: %w", key, value, err)
	}
	*destination = parsed
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
