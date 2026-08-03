package gateway

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/admission"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/requestpath"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
)

const (
	defaultRequestTimeout = 5 * time.Second
)

type testRuntimeConfig struct {
	UpstreamURL            string
	RoutingMode            routing.Mode
	BackendEndpoints       []string
	KeepAlive              bool
	RequestTimeout         time.Duration
	AffinityTTL            time.Duration
	AffinityMaxEntries     int
	AffinityInflightDelta  int64
	CircuitEWMAAlpha       float64
	CircuitErrorThreshold  float64
	CircuitMinimumRequests int
	CircuitOpenDuration    time.Duration
	MaxInflightRequests    int
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t testing.TB, config testRuntimeConfig) (*Server, error) {
	t.Helper()
	config = testDefaults(config)
	service, backends := testBackends(config)
	resolver, err := discovery.NewStatic(backends)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := testStrategy(config, service)
	if err != nil {
		t.Fatal(err)
	}
	breaker, err := circuit.New(circuit.Config{
		EWMAAlpha: config.CircuitEWMAAlpha, ErrorThreshold: config.CircuitErrorThreshold,
		MinimumRequests: config.CircuitMinimumRequests, OpenDuration: config.CircuitOpenDuration,
	})
	if err != nil {
		t.Fatal(err)
	}
	admissionController, err := admission.New(admission.Config{MaxInflight: config.MaxInflightRequests})
	if err != nil {
		t.Fatal(err)
	}
	pool := transport.New(transport.Config{KeepAlive: config.KeepAlive, RequestTimeout: config.RequestTimeout, MaxConnsPerHost: 32})
	metrics := NewMetrics()
	pathService, err := requestpath.New(requestpath.Config{Service: service}, requestpath.Dependencies{
		Resolver: resolver, Strategy: strategy, Circuits: breaker,
		OnBackendRemoved: func(id backend.ID) {
			pool.Remove(id)
			metrics.DeleteBackend(string(id), string(config.RoutingMode))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{RoutingMode: config.RoutingMode, KeepAlive: config.KeepAlive, RequestTimeout: config.RequestTimeout}, Dependencies{
		RequestPath: pathService, Admission: admissionController, Transport: pool, Metrics: metrics, Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pathService.Close()
		_ = resolver.Close()
		pool.Close()
	})
	return server, nil
}

func testBackends(config testRuntimeConfig) (backend.Backend, []backend.Backend) {
	service := backend.Backend{ID: serviceBackendID, URL: config.UpstreamURL}
	backends := make([]backend.Backend, 0, len(config.BackendEndpoints))
	for index, rawURL := range config.BackendEndpoints {
		backends = append(backends, backend.Backend{ID: backend.ID(fmt.Sprintf("backend-%d", index)), URL: rawURL})
	}
	if len(backends) == 0 {
		backends = []backend.Backend{service}
	}
	return service, backends
}

func testStrategy(config testRuntimeConfig, service backend.Backend) (routing.Strategy, error) {
	strategyConfig := routing.Config{Mode: config.RoutingMode, Service: service}
	if config.RoutingMode == routing.ModeBoundedAffinity {
		strategyConfig.BoundedAffinity = routing.BoundedAffinityConfig{
			TTL: config.AffinityTTL, MaxEntries: config.AffinityMaxEntries,
			InflightDelta: config.AffinityInflightDelta, QueueDepthDelta: 1,
		}
	}
	return routing.NewConfigured(strategyConfig)
}

func testDefaults(config testRuntimeConfig) testRuntimeConfig {
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.MaxInflightRequests == 0 {
		config.MaxInflightRequests = 128
	}
	if config.AffinityTTL == 0 {
		config.AffinityTTL = time.Minute
	}
	if config.AffinityMaxEntries == 0 {
		config.AffinityMaxEntries = 100
	}
	if config.CircuitEWMAAlpha == 0 {
		config.CircuitEWMAAlpha = 0.5
	}
	if config.CircuitErrorThreshold == 0 {
		config.CircuitErrorThreshold = 0.6
	}
	if config.CircuitMinimumRequests == 0 {
		config.CircuitMinimumRequests = 3
	}
	if config.CircuitOpenDuration == 0 {
		config.CircuitOpenDuration = 10 * time.Second
	}
	return config
}
