package llmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestNewNormalizesConfigurationAndSharesClock(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	config := DefaultConfig()
	config.RoutingKeyHeader = " X-FishMesh-Session "
	config.Clock = clock

	plugin, err := New("bounded-affinity", config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created := plugin.(*scorer)
	if created.routingKeyHeader != "x-fishmesh-session" {
		t.Fatalf("routingKeyHeader = %q", created.routingKeyHeader)
	}
	if got := created.clock(); !got.Equal(now) {
		t.Fatalf("metrics clock = %v, want %v", got, now)
	}
}

func TestFactoryParsesParameters(t *testing.T) {
	decoder := json.NewDecoder(bytes.NewBufferString(`{
		"routingKeyHeader":"X-Session",
		"metricsMaxAge":"20s",
		"inFlightLoadProducerName":"inflight",
		"affinityTTL":"2m",
		"maxAffinityEntries":500,
		"inflightDelta":3,
		"queueDepthDelta":2
	}`))

	plugin, err := factory("configured", decoder, nil)
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	created := plugin.(*scorer)
	if created.routingKeyHeader != "x-session" || created.metricsMaxAge != 20*time.Second {
		t.Fatalf("factory config = %+v", created)
	}
	if got := created.inflightKey.String(); got != "InFlightLoadDataKey/inflight" {
		t.Fatalf("inflight key = %q", got)
	}
}

func TestFactoryRejectsUnknownAndInvalidParameters(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"unknown":true}`},
		{name: "invalid metrics age", body: `{"metricsMaxAge":"soon"}`},
		{name: "invalid affinity ttl", body: `{"affinityTTL":"later"}`},
		{name: "invalid limits", body: `{"maxAffinityEntries":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := factory("invalid", json.NewDecoder(bytes.NewBufferString(test.body)), nil)
			if err == nil {
				t.Fatal("factory() error = nil")
			}
		})
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		plugin string
		config Config
	}{
		{name: "empty name", config: DefaultConfig()},
		{name: "metrics age", plugin: "invalid", config: Config{BoundedAffinity: routing.DefaultBoundedAffinityConfig()}},
		{name: "bounded affinity", plugin: "invalid", config: Config{MetricsMaxAge: time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.plugin, test.config)
			if err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}
