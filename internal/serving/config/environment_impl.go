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
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

type environmentValues struct {
	endpointRefresh, endpointMaxAge                                                     time.Duration
	observationInterval, observationMaxAge                                              time.Duration
	observationRequestTimeout                                                           time.Duration
	requestTimeout, shutdownTimeout                                                     time.Duration
	admissionTuningInterval, admissionTuningCooldown                                    time.Duration
	maxRequestBody                                                                      int64
	sessionKeyTTL, circuitOpenDuration                                                  time.Duration
	keepAlive                                                                           bool
	sessionKeyMaxEntries, maxInflight, maxConnections                                   int
	admissionInitialTarget, admissionMinTarget, admissionMaxTarget, admissionTargetStep int
	circuitMinimumRequests                                                              int
	sessionKeyInflightDelta                                                             int64
	kvAwareQueueTokenPenalty                                                            int64
	kvAwareRunningTokenPenalty                                                          int64
	kvAwareInflightTokenPenalty                                                         int64
	kvAwareShortPromptTokens                                                            int
	kvAwareHardQueueDepth                                                               int64
	kvAwareHardLocalInflight                                                            int64
	runtimeCPUHardLimit, runtimeMemoryHardLimit                                         float64
	runtimeGPUUtilizationLimit, runtimeGPUMemoryHardLimit, runtimeGPUTemperatureLimit   float64
	sessionKeyQueueDelta, circuitAlpha, circuitError                                    float64
}

// LoadEnvironment 读取全部 FISHMESH_* 配置并返回按 domain 拆分的结果。
func LoadEnvironment() (Config, error) {
	defaults := DefaultConfig()
	values, err := loadEnvironmentValues(defaults)
	if err != nil {
		return Config{}, err
	}
	config, err := values.buildConfig(defaults)
	if err != nil {
		return Config{}, err
	}
	if config.Routing.Mode == routing.ModeKVAware && config.Routing.KVAware.EstimatorMode == routing.KVAwareEstimatorStatic {
		profileFile := strings.TrimSpace(os.Getenv(envKVAwareStaticProfileFile))
		if profileFile == "" {
			return Config{}, fmt.Errorf("%s must be configured for static TTFT routing", envKVAwareStaticProfileFile)
		}
		config.StaticProfile, err = loadStaticProfile(profileFile)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envKVAwareStaticProfileFile, err)
		}
	}
	return config, config.Validate()
}

func loadEnvironmentValues(defaults Config) (environmentValues, error) {
	values := environmentValues{}
	parsers := []func() error{
		func() error {
			return assignDuration(envEndpointRefreshInterval, defaults.Discovery.EndpointSlice.RefreshInterval, &values.endpointRefresh)
		},
		func() error {
			return assignDuration(envEndpointMaxAge, defaults.RequestPath.DiscoveryMaxAge, &values.endpointMaxAge)
		},
		func() error {
			return assignDuration(envObservationInterval, defaults.Observation.Interval, &values.observationInterval)
		},
		func() error {
			return assignDuration(envObservationMaxAge, defaults.Observation.MaxAge, &values.observationMaxAge)
		},
		func() error {
			return assignDuration(envObservationRequestTimeout, defaults.Observation.RequestTimeout, &values.observationRequestTimeout)
		},
		func() error {
			return assignDuration(envRequestTimeout, defaults.Gateway.RequestTimeout, &values.requestTimeout)
		},
		func() error {
			return assignInt64(envMaxRequestBodyBytes, defaults.Gateway.MaxRequestBodyBytes, &values.maxRequestBody)
		},
		func() error {
			return assignDuration(envShutdownTimeout, defaults.Process.ShutdownTimeout, &values.shutdownTimeout)
		},
		func() error {
			return assignDuration(envSessionKeyTTL, defaults.Routing.SessionKey.TTL, &values.sessionKeyTTL)
		},
		func() error {
			return assignDuration(envCircuitOpenDuration, defaults.Circuit.OpenDuration, &values.circuitOpenDuration)
		},
		func() error { return assignBool(envUpstreamKeepAlive, defaults.Transport.KeepAlive, &values.keepAlive) },
		func() error {
			return assignInt(envSessionKeyMaxEntries, defaults.Routing.SessionKey.MaxEntries, false, &values.sessionKeyMaxEntries)
		},
		func() error {
			return assignInt(envMaxInflightRequests, defaults.Admission.MaxInflight, true, &values.maxInflight)
		},
		func() error {
			return assignInt(envAdmissionInitialTarget, defaults.Admission.InitialTarget, true, &values.admissionInitialTarget)
		},
		func() error {
			return assignInt(envAdmissionMinTarget, defaults.AdmissionTuning.MinTarget, true, &values.admissionMinTarget)
		},
		func() error {
			return assignInt(envAdmissionMaxTarget, defaults.AdmissionTuning.MaxTarget, true, &values.admissionMaxTarget)
		},
		func() error {
			return assignInt(envAdmissionTargetStep, defaults.AdmissionTuning.Step, true, &values.admissionTargetStep)
		},
		func() error {
			return assignDuration(envAdmissionTuningInterval, defaults.AdmissionTuning.Interval, &values.admissionTuningInterval)
		},
		func() error {
			return assignDuration(envAdmissionTuningCooldown, defaults.AdmissionTuning.Cooldown, &values.admissionTuningCooldown)
		},
		func() error {
			return assignInt(envMaxConnsPerHost, defaults.Transport.MaxConnsPerHost, true, &values.maxConnections)
		},
		func() error {
			return assignInt(envCircuitMinimumRequests, defaults.Circuit.MinimumRequests, true, &values.circuitMinimumRequests)
		},
		func() error {
			return assignInt64(envSessionKeyInflightDelta, defaults.Routing.SessionKey.InflightDelta, &values.sessionKeyInflightDelta)
		},
		func() error {
			return assignFloat(envSessionKeyQueueDepthDelta, defaults.Routing.SessionKey.QueueDepthDelta, &values.sessionKeyQueueDelta)
		},
		func() error {
			return assignNonNegativeInt64(envKVAwareQueueTokenPenalty, defaults.Routing.KVAware.QueueTokenPenalty, &values.kvAwareQueueTokenPenalty)
		},
		func() error {
			return assignNonNegativeInt64(envKVAwareRunningTokenPenalty, defaults.Routing.KVAware.RunningTokenPenalty, &values.kvAwareRunningTokenPenalty)
		},
		func() error {
			return assignNonNegativeInt64(envKVAwareInflightTokenPenalty, defaults.Routing.KVAware.InflightTokenPenalty, &values.kvAwareInflightTokenPenalty)
		},
		func() error {
			return assignNonNegativeInt(envKVAwareShortPromptTokens, defaults.RequestPath.ShortPromptTokens, &values.kvAwareShortPromptTokens)
		},
		func() error {
			return assignNonNegativeInt64(envKVAwareHardQueueDepth, defaults.RequestPath.HardQueueDepth, &values.kvAwareHardQueueDepth)
		},
		func() error {
			return assignNonNegativeInt64(envKVAwareHardLocalInflight, defaults.RequestPath.HardLocalInflight, &values.kvAwareHardLocalInflight)
		},
		func() error {
			return assignFloat(envRuntimeCPUHardLimit, defaults.RequestPath.RuntimeCPUHardLimitCores, &values.runtimeCPUHardLimit)
		},
		func() error {
			return assignFloat(envRuntimeMemoryHardLimit, defaults.RequestPath.RuntimeMemoryHardLimitBytes, &values.runtimeMemoryHardLimit)
		},
		func() error {
			return assignFloat(envRuntimeGPUUtilizationLimit, defaults.RequestPath.RuntimeGPUUtilizationHardLimitPct, &values.runtimeGPUUtilizationLimit)
		},
		func() error {
			return assignFloat(envRuntimeGPUMemoryHardLimit, defaults.RequestPath.RuntimeGPUMemoryHardLimitBytes, &values.runtimeGPUMemoryHardLimit)
		},
		func() error {
			return assignFloat(envRuntimeGPUTemperatureLimit, defaults.RequestPath.RuntimeGPUTemperatureHardLimitC, &values.runtimeGPUTemperatureLimit)
		},
		func() error {
			return assignFloat(envCircuitEWMAAlpha, defaults.Circuit.EWMAAlpha, &values.circuitAlpha)
		},
		func() error {
			return assignFloat(envCircuitErrorThreshold, defaults.Circuit.ErrorThreshold, &values.circuitError)
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
	if values.observationRequestTimeout <= 0 {
		return environmentValues{}, fmt.Errorf("%s must be positive", envObservationRequestTimeout)
	}
	if values.runtimeCPUHardLimit < 0 || values.runtimeMemoryHardLimit < 0 || values.runtimeGPUUtilizationLimit < 0 || values.runtimeGPUMemoryHardLimit < 0 || values.runtimeGPUTemperatureLimit < 0 {
		return environmentValues{}, fmt.Errorf("runtime hard limits must not be negative")
	}
	if strings.TrimSpace(os.Getenv(envAdmissionInitialTarget)) == "" {
		values.admissionInitialTarget = values.maxInflight
	}
	if strings.TrimSpace(os.Getenv(envAdmissionMaxTarget)) == "" {
		values.admissionMaxTarget = values.maxInflight
	}
	return values, nil
}

func (v environmentValues) buildConfig(defaults Config) (Config, error) {
	service := defaults.RequestPath.Service
	service.URL = valueOrDefault(envUpstreamURL, service.URL)
	if err := service.Validate(); err != nil {
		return Config{}, err
	}
	staticBackends, err := staticBackendsFromEnvironment()
	if err != nil {
		return Config{}, err
	}
	discoveryMode := discovery.Mode(valueOrDefault(envEndpointDiscovery, string(defaults.Discovery.Mode)))
	routingMode := routing.Mode(valueOrDefault(envRoutingMode, string(defaults.Routing.Mode)))
	if discoveryMode == discovery.ModeStatic && routingMode == routing.ModeLoadBalanced && len(staticBackends) == 0 {
		staticBackends = []backend.Backend{service}
	}
	reconcileInterval := time.Duration(0)
	if discoveryMode == discovery.ModeEndpointSlice {
		reconcileInterval = v.endpointRefresh
	}
	namespace := valueOrDefault(envEndpointNamespace, defaults.Discovery.EndpointSlice.Namespace)
	apiURL := valueOrDefault(envKubernetesAPIURL, defaults.Discovery.EndpointSlice.BaseURL)
	tokenFile := valueOrDefault(envKubernetesTokenFile, defaults.Discovery.EndpointSlice.TokenFile)
	caFile := valueOrDefault(envKubernetesCAFile, defaults.Discovery.EndpointSlice.CAFile)
	config := Config{
		Process: ProcessConfig{ListenAddress: valueOrDefault(envListenAddress, defaults.Process.ListenAddress), ReadHeaderTimeout: defaults.Process.ReadHeaderTimeout, ShutdownTimeout: v.shutdownTimeout},
		Gateway: gateway.Config{RoutingMode: routingMode, KeepAlive: v.keepAlive, RequestTimeout: v.requestTimeout, MaxRequestBodyBytes: v.maxRequestBody},
		Discovery: discovery.Config{Mode: discoveryMode, Static: staticBackends, EndpointSlice: discovery.EndpointSliceConfig{
			Namespace: namespace, ServiceName: valueOrDefault(envEndpointService, defaults.Discovery.EndpointSlice.ServiceName), BaseURL: apiURL,
			TokenFile: tokenFile, CAFile: caFile, RefreshInterval: v.endpointRefresh,
		}},
		Identity:        identity.Config{Namespace: namespace, BaseURL: apiURL, TokenFile: tokenFile, CAFile: caFile},
		ObservationMode: observation.Mode(valueOrDefault(envObservationMode, string(defaults.ObservationMode))),
		Observation:     observation.Config{Interval: v.observationInterval, MaxAge: v.observationMaxAge, RequestTimeout: v.observationRequestTimeout, Clock: defaults.Observation.Clock},
		Prometheus: observation.PrometheusConfig{
			MetricsPath: defaults.Prometheus.MetricsPath,
			Clock:       defaults.Prometheus.Clock,
			Runtime: observation.RuntimePrometheusConfig{
				Endpoint:            valueOrDefault(envRuntimePrometheusURL, defaults.Prometheus.Runtime.Endpoint),
				Namespace:           namespace,
				CPUQuery:            valueOrDefault(envRuntimeCPUQuery, defaults.Prometheus.Runtime.CPUQuery),
				MemoryQuery:         valueOrDefault(envRuntimeMemoryQuery, defaults.Prometheus.Runtime.MemoryQuery),
				GPUUtilizationQuery: valueOrDefault(envRuntimeGPUUtilizationQuery, defaults.Prometheus.Runtime.GPUUtilizationQuery),
				GPUMemoryQuery:      valueOrDefault(envRuntimeGPUMemoryQuery, defaults.Prometheus.Runtime.GPUMemoryQuery),
				GPUTemperatureQuery: valueOrDefault(envRuntimeGPUTemperatureQuery, defaults.Prometheus.Runtime.GPUTemperatureQuery),
			},
		},
		Routing: routing.Config{Mode: routingMode, Service: service, SessionKey: routing.SessionKeyConfig{
			TTL: v.sessionKeyTTL, MaxEntries: v.sessionKeyMaxEntries, InflightDelta: v.sessionKeyInflightDelta, QueueDepthDelta: v.sessionKeyQueueDelta, Clock: defaults.Routing.SessionKey.Clock,
		}, KVAware: routing.KVAwareConfig{
			EstimatorMode:     routing.KVAwareEstimatorMode(valueOrDefault(envKVAwareEstimatorMode, string(defaults.Routing.KVAware.EstimatorMode))),
			QueueTokenPenalty: v.kvAwareQueueTokenPenalty, RunningTokenPenalty: v.kvAwareRunningTokenPenalty, InflightTokenPenalty: v.kvAwareInflightTokenPenalty,
		}},
		Circuit:   circuit.Config{EWMAAlpha: v.circuitAlpha, ErrorThreshold: v.circuitError, MinimumRequests: v.circuitMinimumRequests, OpenDuration: v.circuitOpenDuration, Clock: defaults.Circuit.Clock},
		Admission: admission.Config{MaxInflight: v.maxInflight, InitialTarget: v.admissionInitialTarget},
		AdmissionTuning: admission.TuningConfig{
			Mode:      admission.TuningMode(valueOrDefault(envAdmissionTuningMode, string(defaults.AdmissionTuning.Mode))),
			MinTarget: v.admissionMinTarget, MaxTarget: v.admissionMaxTarget, Step: v.admissionTargetStep,
			Interval: v.admissionTuningInterval, Cooldown: v.admissionTuningCooldown,
			LowWatermark: defaults.AdmissionTuning.LowWatermark, HighWatermark: defaults.AdmissionTuning.HighWatermark,
		},
		Transport: transport.Config{KeepAlive: v.keepAlive, RequestTimeout: v.requestTimeout, MaxConnsPerHost: v.maxConnections, IdleConnTimeout: defaults.Transport.IdleConnTimeout},
		RequestPath: requestpath.Config{
			Service: service, RequireFreshDiscovery: discoveryMode == discovery.ModeEndpointSlice, DiscoveryMaxAge: v.endpointMaxAge,
			ReconcileInterval: reconcileInterval, HardQueueDepth: v.kvAwareHardQueueDepth, HardLocalInflight: v.kvAwareHardLocalInflight,
			ShortPromptTokens:        v.kvAwareShortPromptTokens,
			RuntimeCPUHardLimitCores: v.runtimeCPUHardLimit, RuntimeMemoryHardLimitBytes: v.runtimeMemoryHardLimit,
			RuntimeGPUUtilizationHardLimitPct: v.runtimeGPUUtilizationLimit, RuntimeGPUMemoryHardLimitBytes: v.runtimeGPUMemoryHardLimit,
			RuntimeGPUTemperatureHardLimitC: v.runtimeGPUTemperatureLimit,
		},
		Tokenization: tokenization.Config{BaseURL: valueOrDefault(envKVAwareRenderURL, defaults.Tokenization.BaseURL), Model: valueOrDefault(envKVAwareModel, defaults.Tokenization.Model), Timeout: defaults.Tokenization.Timeout, MaxRequestBytes: defaults.Tokenization.MaxRequestBytes, MaxResponseBytes: defaults.Tokenization.MaxResponseBytes, MaxTotalTokens: defaults.Tokenization.MaxTotalTokens},
		KVCache:      defaults.KVCache,
		Prediction: prediction.Config{
			Mode: valueOrDefaultMode(envPredictionMode, defaults.Prediction.Mode), MaxSamples: defaults.Prediction.MaxSamples,
			MaxSampleAge: defaults.Prediction.MaxSampleAge, MinimumSamples: defaults.Prediction.MinimumSamples, RefitEvery: defaults.Prediction.RefitEvery, Clock: defaults.Prediction.Clock,
		},
	}
	return config, nil
}

func valueOrDefaultMode(key string, fallback prediction.Mode) prediction.Mode {
	return prediction.Mode(valueOrDefault(key, string(fallback)))
}

func staticBackendsFromEnvironment() ([]backend.Backend, error) {
	value := os.Getenv(envBackendEndpoints)
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

func assignNonNegativeInt(key string, fallback int, destination *int) error {
	if err := assignInt(key, fallback, false, destination); err != nil {
		return err
	}
	if *destination < 0 {
		return fmt.Errorf("%s must not be negative: %d", key, *destination)
	}
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
