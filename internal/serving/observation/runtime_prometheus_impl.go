package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
)

const (
	runtimePrometheusSource = "prometheus-runtime"
	runtimeQueryPath        = "/api/v1/query"
	maxRuntimeResponseBytes = 2 << 20
)

var _ RuntimeCollector = runtimePrometheusCollector{}

// runtimePrometheusCollector reads instant Prometheus query results using Pod
// identity selectors. It deliberately accepts only the first scalar/vector
// result from each configured query; aggregation and label selection belong to
// the operator-provided PromQL, not to this adapter.
type runtimePrometheusCollector struct {
	config RuntimePrometheusConfig
	client *http.Client
	clock  Clock
}

// NewPrometheusRuntime constructs the optional runtime resource adapter.
func NewPrometheusRuntime(config RuntimePrometheusConfig) (RuntimeCollector, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return runtimePrometheusCollector{config: config, client: client, clock: config.Clock}, nil
}

func (c runtimePrometheusCollector) Collect(ctx context.Context, candidate backend.Backend, workload identity.Identity) Runtime {
	now := c.clock()
	state := Runtime{Source: runtimePrometheusSource, ObservedAt: now}
	if strings.TrimSpace(workload.PodName) == "" {
		state.Error = "backend has no Pod identity for runtime metrics"
		return state
	}
	queries := []struct {
		name   string
		query  string
		assign func(Sample[float64])
	}{
		{name: "cpu", query: c.config.CPUQuery, assign: func(sample Sample[float64]) { state.CPUUsageCores = sample }},
		{name: "memory", query: c.config.MemoryQuery, assign: func(sample Sample[float64]) { state.MemoryUsageBytes = sample }},
		{name: "gpu utilization", query: c.config.GPUUtilizationQuery, assign: func(sample Sample[float64]) { state.GPUUtilizationPercent = sample }},
		{name: "gpu memory", query: c.config.GPUMemoryQuery, assign: func(sample Sample[float64]) { state.GPUMemoryUsedBytes = sample }},
		{name: "gpu temperature", query: c.config.GPUTemperatureQuery, assign: func(sample Sample[float64]) { state.GPUTemperatureCelsius = sample }},
	}
	for _, item := range queries {
		if strings.TrimSpace(item.query) == "" {
			continue
		}
		value, err := c.query(ctx, item.query, workload.PodName)
		if err != nil {
			item.assign(Sample[float64]{ObservedAt: now, Source: runtimePrometheusSource, Error: item.name + ": " + err.Error()})
			state.Error = joinErrors(state.Error, item.name+": "+err.Error())
			continue
		}
		item.assign(Sample[float64]{Value: value, Valid: true, ObservedAt: now, Source: runtimePrometheusSource})
	}
	_ = candidate // candidate identity is intentionally not used as a metric selector.
	return state
}

func (c runtimePrometheusCollector) query(ctx context.Context, template, pod string) (float64, error) {
	query := strings.NewReplacer(
		"$namespace", quotePromQL(c.config.Namespace),
		"$pod", quotePromQL(pod),
	).Replace(template)
	parsed, err := url.Parse(strings.TrimRight(c.config.Endpoint, "/") + runtimeQueryPath)
	if err != nil {
		return 0, err
	}
	values := parsed.Query()
	values.Set("query", query)
	parsed.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("runtime Prometheus returned %s", response.Status)
	}
	var payload runtimeQueryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxRuntimeResponseBytes)).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode runtime Prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("runtime Prometheus query status %q", payload.Status)
	}
	if len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Value) != 2 {
		return 0, fmt.Errorf("runtime Prometheus query returned no scalar result")
	}
	return parsePrometheusValue(payload.Data.Result[0].Value[1])
}

func quotePromQL(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value) + `"`
}

func parsePrometheusValue(raw json.RawMessage) (float64, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strconv.ParseFloat(text, 64)
	}
	return strconv.ParseFloat(string(raw), 64)
}

type runtimeQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Value []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}
