package config

import (
	"strings"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestEndpointSliceConfigDoesNotRequireStaticEndpoints(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "prefix-affinity")
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
		"duration":   {"FISHMESH_REQUEST_TIMEOUT", "ninety-seconds"},
		"boolean":    {"FISHMESH_UPSTREAM_KEEPALIVE", "sometimes"},
		"float":      {"FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA", "low"},
		"positive":   {"FISHMESH_MAX_INFLIGHT_REQUESTS", "0"},
		"body limit": {"FISHMESH_MAX_REQUEST_BODY_BYTES", "0"},
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

func TestLoadEnvironmentMapsBoundedAffinityDomains(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "bounded-affinity")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	t.Setenv("FISHMESH_AFFINITY_TTL", "2m")
	t.Setenv("FISHMESH_AFFINITY_MAX_ENTRIES", "2048")
	t.Setenv("FISHMESH_AFFINITY_INFLIGHT_DELTA", "3")
	t.Setenv("FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA", "2.5")
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
	bounded := config.Routing.BoundedAffinity
	if config.Routing.Mode != routing.ModeBoundedAffinity || bounded.TTL != 2*time.Minute || bounded.MaxEntries != 2048 || bounded.InflightDelta != 3 || bounded.QueueDepthDelta != 2.5 {
		t.Fatalf("unexpected routing config: %+v", config.Routing)
	}
	if config.Gateway.MaxRequestBodyBytes != 4096 || config.Admission.MaxInflight != 64 || config.Transport.MaxConnsPerHost != 12 || config.Circuit.EWMAAlpha != 0.4 || config.Circuit.ErrorThreshold != 0.7 || config.Circuit.MinimumRequests != 5 || config.Circuit.OpenDuration != 15*time.Second {
		t.Fatalf("unexpected reliability config: %+v", config)
	}
}

func TestLoadEnvironmentMapsExactCostDomains(t *testing.T) {
	t.Setenv("FISHMESH_ROUTING_MODE", "exact-cache-load")
	t.Setenv("FISHMESH_ENDPOINT_DISCOVERY", "endpointslice")
	t.Setenv("FISHMESH_EXACT_QUEUE_TOKEN_PENALTY", "320")
	t.Setenv("FISHMESH_EXACT_RUNNING_TOKEN_PENALTY", "80")
	t.Setenv("FISHMESH_EXACT_INFLIGHT_TOKEN_PENALTY", "40")
	config, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	exact := config.Routing.ExactCacheLoad
	if exact.QueueTokenPenalty != 320 || exact.RunningTokenPenalty != 80 || exact.InflightTokenPenalty != 40 {
		t.Fatalf("unexpected exact cost config: %+v", exact)
	}
}

func TestLoadEnvironmentRejectsNegativeExactCostPenalty(t *testing.T) {
	t.Setenv("FISHMESH_EXACT_QUEUE_TOKEN_PENALTY", "-1")
	if _, err := LoadEnvironment(); err == nil || !strings.Contains(err.Error(), envExactQueueTokenPenalty) {
		t.Fatalf("negative exact cost penalty was accepted: %v", err)
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
