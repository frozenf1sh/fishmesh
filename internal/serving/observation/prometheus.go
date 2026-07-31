package observation

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const maxMetricsBodyBytes = 8 << 20

// PrometheusCollector reads the vLLM metrics endpoint directly from each
// EndpointSlice address. It intentionally reports only per-backend vLLM
// values; node-level GPU and Kubernetes signals are not incorrectly attached
// to a Pod until an explicit identity mapping exists.
type PrometheusCollector struct {
	HTTPClient  *http.Client
	MetricsPath string
	Clock       Clock
}

func (c PrometheusCollector) Collect(ctx context.Context, backend routing.Backend) routing.BackendObservation {
	now := c.clock()
	state := routing.BackendObservation{Status: routing.ObservationUnavailable, Source: backend.URL, ObservedAt: now}
	metricsURL, err := metricsURL(backend.URL, c.MetricsPath)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	families, err := fetch(ctx, c.HTTPClient, metricsURL)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	observed := 0
	if value, ok := sum(families, "vllm:num_requests_waiting", "vllm:request_queue_size"); ok {
		state.QueueLength, observed = value, observed+1
	}
	if value, ok := sum(families, "vllm:num_requests_running"); ok {
		state.RunningRequests, observed = value, observed+1
	}
	var cacheHits, cacheQueries float64
	if value, ok := sum(families, "vllm:prefix_cache_hits_total"); ok {
		cacheHits, observed = value, observed+1
	}
	if value, ok := sum(families, "vllm:prefix_cache_queries_total"); ok {
		cacheQueries = value
	}
	if cacheQueries > 0 {
		state.PrefixCacheHitRate = cacheHits / cacheQueries
	}
	if family := first(families, "vllm:time_to_first_token_seconds", "vllm:request_time_to_first_token_seconds"); family != nil {
		if value, ok := quantile(family, 0.95); ok {
			state.TTFTP95Milliseconds, observed = value*1000, observed+1
		}
	}
	if value, ok := average(families, "vllm:kv_cache_usage_perc", "vllm:gpu_cache_usage_perc"); ok {
		if value <= 1 {
			value *= 100
		}
		state.KVCacheUsagePercent, observed = value, observed+1
	}
	if observed == 0 {
		state.Status = routing.ObservationDegraded
		state.Error = "vLLM metrics endpoint had no recognized serving metrics"
		return state
	}
	state.Status = routing.ObservationOK
	return state
}

func (c PrometheusCollector) clock() time.Time {
	if c.Clock == nil {
		return time.Now()
	}
	return c.Clock()
}

func metricsURL(raw, metricsPath string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("backend URL must be absolute: %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("backend URL scheme must be http or https: %q", raw)
	}
	if strings.TrimSpace(metricsPath) == "" {
		metricsPath = "/metrics"
	}
	parsed.Path = metricsPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	return parsed.String(), nil
}

func fetch(ctx context.Context, client *http.Client, target string) (map[string]*dto.MetricFamily, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/plain; version=0.0.4")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned %s", response.Status)
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(io.LimitReader(response.Body, maxMetricsBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("parse prometheus metrics: %w", err)
	}
	return families, nil
}

func first(families map[string]*dto.MetricFamily, names ...string) *dto.MetricFamily {
	for _, name := range names {
		if family := families[name]; family != nil {
			return family
		}
	}
	return nil
}

func sum(families map[string]*dto.MetricFamily, names ...string) (float64, bool) {
	for _, name := range names {
		family := families[name]
		if family == nil {
			continue
		}
		var total float64
		for _, metric := range family.Metric {
			switch {
			case metric.Gauge != nil:
				total += metric.Gauge.GetValue()
			case metric.Counter != nil:
				total += metric.Counter.GetValue()
			}
		}
		return total, true
	}
	return 0, false
}

func average(families map[string]*dto.MetricFamily, names ...string) (float64, bool) {
	for _, name := range names {
		family := families[name]
		if family == nil || len(family.Metric) == 0 {
			continue
		}
		value, ok := sum(families, name)
		return value / float64(len(family.Metric)), ok
	}
	return 0, false
}

func quantile(family *dto.MetricFamily, target float64) (float64, bool) {
	buckets := map[float64]uint64{}
	for _, metric := range family.Metric {
		if metric.Histogram == nil {
			continue
		}
		for _, bucket := range metric.Histogram.Bucket {
			buckets[bucket.GetUpperBound()] += bucket.GetCumulativeCount()
		}
	}
	if len(buckets) == 0 {
		return 0, false
	}
	upperBounds := make([]float64, 0, len(buckets))
	for upperBound := range buckets {
		upperBounds = append(upperBounds, upperBound)
	}
	sort.Float64s(upperBounds)
	total := float64(buckets[upperBounds[len(upperBounds)-1]])
	if total == 0 || math.IsNaN(total) {
		return 0, false
	}
	for _, upperBound := range upperBounds {
		if float64(buckets[upperBound]) >= total*target {
			return upperBound, true
		}
	}
	return upperBounds[len(upperBounds)-1], true
}
