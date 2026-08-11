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
	config := testConfig()
	config.SessionKeyHeader = " X-FishMesh-Session "
	config.Clock = clock

	plugin, err := New("session-key", config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created := plugin.(*scorer)
	if created.sessionKeyHeader != "x-fishmesh-session" {
		t.Fatalf("sessionKeyHeader = %q", created.sessionKeyHeader)
	}
	if got := created.clock(); !got.Equal(now) {
		t.Fatalf("metrics clock = %v, want %v", got, now)
	}
}

func TestFactoryParsesParameters(t *testing.T) {
	decoder := json.NewDecoder(bytes.NewBufferString(`{
		"sessionKeyHeader":"X-Session",
		"metricsMaxAge":"20s",
		"inFlightLoadProducerName":"inflight",
		"sessionKeyTTL":"2m",
		"maxSessionKeyEntries":500,
		"inflightDelta":3,
		"queueDepthDelta":2
	}`))

	plugin, err := factory("configured", decoder, nil)
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	created := plugin.(*scorer)
	if created.sessionKeyHeader != "x-session" || created.metricsMaxAge != 20*time.Second {
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
		{name: "invalid session-key ttl", body: `{"sessionKeyTTL":"later"}`},
		{name: "invalid limits", body: `{"maxSessionKeyEntries":0}`},
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
		{name: "empty name", config: testConfig()},
		{name: "metrics age", plugin: "invalid", config: Config{SessionKey: testSessionKeyConfig()}},
		{name: "session-key", plugin: "invalid", config: Config{MetricsMaxAge: time.Second}},
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

func testConfig() Config {
	return Config{
		SessionKeyHeader: "x-fishmesh-session-key",
		MetricsMaxAge:    45 * time.Second,
		SessionKey:       testSessionKeyConfig(),
		Clock:            time.Now,
	}
}

func testSessionKeyConfig() routing.SessionKeyConfig {
	return routing.SessionKeyConfig{
		TTL:             5 * time.Minute,
		MaxEntries:      10_000,
		InflightDelta:   2,
		QueueDepthDelta: 1,
		Clock:           time.Now,
	}
}
