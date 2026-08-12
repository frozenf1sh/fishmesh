package config

import (
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestDefaultConfigIsComplete(t *testing.T) {
	config := DefaultConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if config.Routing.Mode != routing.ModeLoadBalanced || config.Gateway.RoutingMode != routing.ModeLoadBalanced {
		t.Fatalf("default routing mode = %q/%q", config.Routing.Mode, config.Gateway.RoutingMode)
	}
	if config.Discovery.Mode != discovery.ModeStatic || len(config.Discovery.Static) != 1 {
		t.Fatalf("default discovery = %+v", config.Discovery)
	}
	if config.Observation.Interval <= 0 || config.Observation.MaxAge <= 0 || config.Observation.RequestTimeout <= 0 || config.Prometheus.MetricsPath != "/metrics" {
		t.Fatalf("default observation config = %+v / %+v", config.Observation, config.Prometheus)
	}
	if config.Tokenization.Timeout <= 0 || config.KVCache.FreshnessTTL <= 0 || config.Prediction.MaxSamples <= 0 || config.Transport.IdleConnTimeout <= 0 {
		t.Fatalf("default adapter bounds are incomplete: %+v %+v %+v %+v", config.Tokenization, config.KVCache, config.Prediction, config.Transport)
	}
}

func TestEndpointSliceConfigDoesNotRequireStaticEndpoints(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "session-key")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	config, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.Discovery.Mode != discovery.ModeEndpointSlice || len(config.Discovery.Static) != 0 {
		t.Fatalf("unexpected discovery config: %+v", config.Discovery)
	}
}

func TestLoadEnvironmentRejectsMalformedValues(t *testing.T) {
	for name, test := range map[string]struct {
		key   string
		value string
	}{
		"duration":            {"FISHMESH_REQUEST_TIMEOUT", "ninety-seconds"},
		"boolean":             {"FISHMESH_UPSTREAM_KEEPALIVE", "sometimes"},
		"float":               {"FISHMESH_SESSION_KEY_QUEUE_DEPTH_DELTA", "low"},
		"positive":            {"FISHMESH_MAX_INFLIGHT_REQUESTS", "0"},
		"body limit":          {"FISHMESH_MAX_REQUEST_BODY_BYTES", "0"},
		"observation timeout": {envObservationRequestTimeout, "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			_, err := LoadEnvironment()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("expected error naming %s, got %v", test.key, err)
			}
		})
	}
}

func TestLoadEnvironmentMapsSessionKeyDomains(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "session-key")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	t.Setenv("FISHMESH_SESSION_KEY_TTL", "2m")
	t.Setenv("FISHMESH_SESSION_KEY_MAX_ENTRIES", "2048")
	t.Setenv("FISHMESH_SESSION_KEY_INFLIGHT_DELTA", "3")
	t.Setenv("FISHMESH_SESSION_KEY_QUEUE_DEPTH_DELTA", "2.5")
	t.Setenv("FISHMESH_MAX_INFLIGHT_REQUESTS", "64")
	t.Setenv("FISHMESH_MAX_CONNS_PER_HOST", "12")
	t.Setenv("FISHMESH_MAX_REQUEST_BODY_BYTES", "4096")
	t.Setenv("FISHMESH_CIRCUIT_EWMA_ALPHA", "0.4")
	t.Setenv("FISHMESH_CIRCUIT_ERROR_THRESHOLD", "0.7")
	t.Setenv("FISHMESH_CIRCUIT_MIN_REQUESTS", "5")
	t.Setenv("FISHMESH_CIRCUIT_OPEN_DURATION", "15s")
	config, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey := config.Routing.SessionKey
	if config.Routing.Mode != routing.ModeSessionKey || sessionKey.TTL != 2*time.Minute || sessionKey.MaxEntries != 2048 || sessionKey.InflightDelta != 3 || sessionKey.QueueDepthDelta != 2.5 {
		t.Fatalf("unexpected routing config: %+v", config.Routing)
	}
	if config.Gateway.MaxRequestBodyBytes != 4096 || config.Admission.MaxInflight != 64 || config.Transport.MaxConnsPerHost != 12 || config.Circuit.EWMAAlpha != 0.4 || config.Circuit.ErrorThreshold != 0.7 || config.Circuit.MinimumRequests != 5 || config.Circuit.OpenDuration != 15*time.Second {
		t.Fatalf("unexpected reliability config: %+v", config)
	}
}

func TestLoadEnvironmentMapsKVAwareCostDomains(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "kv-aware")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	t.Setenv("FISHMESH_KV_AWARE_QUEUE_TOKEN_PENALTY", "320")
	t.Setenv("FISHMESH_KV_AWARE_RUNNING_TOKEN_PENALTY", "80")
	t.Setenv("FISHMESH_KV_AWARE_INFLIGHT_TOKEN_PENALTY", "40")
	t.Setenv(envObservationMode, "prometheus")
	t.Setenv(envObservationRequestTimeout, "350ms")
	t.Setenv(envKVAwareHardQueueDepth, "12")
	t.Setenv(envKVAwareHardLocalInflight, "24")
	config, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	kvAware := config.Routing.KVAware
	if kvAware.QueueTokenPenalty != 320 || kvAware.RunningTokenPenalty != 80 || kvAware.InflightTokenPenalty != 40 {
		t.Fatalf("unexpected KV-aware cost config: %+v", kvAware)
	}
	if config.Observation.RequestTimeout != 350*time.Millisecond || config.RequestPath.HardQueueDepth != 12 || config.RequestPath.HardLocalInflight != 24 {
		t.Fatalf("unexpected observation/hard-overload config: %+v / %+v", config.Observation, config.RequestPath)
	}
}

func TestLoadEnvironmentRejectsNegativeKVAwareCostPenalty(t *testing.T) {
	t.Setenv("FISHMESH_KV_AWARE_QUEUE_TOKEN_PENALTY", "-1")
	if _, err := LoadEnvironment(); err == nil || !strings.Contains(err.Error(), envKVAwareQueueTokenPenalty) {
		t.Fatalf("negative KV-aware cost penalty was accepted: %v", err)
	}
}

func TestLoadEnvironmentRejectsNegativeHardOverloadThreshold(t *testing.T) {
	t.Setenv(envKVAwareHardQueueDepth, "-1")
	if _, err := LoadEnvironment(); err == nil || !strings.Contains(err.Error(), envKVAwareHardQueueDepth) {
		t.Fatalf("negative hard-overload threshold was accepted: %v", err)
	}
}

func TestLoadEnvironmentMapsPredictionShadowMode(t *testing.T) {
	t.Setenv(envPredictionMode, "shadow")
	config, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.Prediction.Mode != "shadow" {
		t.Fatalf("prediction mode = %q, want shadow", config.Prediction.Mode)
	}
}

func TestEndpointSliceRequiresServiceIdentity(t *testing.T) {
	config, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	config.Discovery.Mode = discovery.ModeEndpointSlice
	config.Discovery.EndpointSlice.ServiceName = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing EndpointSlice service identity to fail validation")
	}
}
