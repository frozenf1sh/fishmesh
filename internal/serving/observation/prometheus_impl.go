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

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	maxMetricsBodyBytes = 8 << 20
	headerAccept        = "Accept"
	prometheusTextMedia = "text/plain; version=0.0.4"
	httpURLScheme       = "http"
	httpsURLScheme      = "https"

	metricRequestsWaiting     = "vllm:num_requests_waiting"
	metricRequestQueueSize    = "vllm:request_queue_size"
	metricRequestsRunning     = "vllm:num_requests_running"
	metricPrefixCacheHits     = "vllm:prefix_cache_hits_total"
	metricPrefixCacheQueries  = "vllm:prefix_cache_queries_total"
	metricTTFT                = "vllm:time_to_first_token_seconds"
	metricRequestTTFT         = "vllm:request_time_to_first_token_seconds"
	metricKVCacheUsage        = "vllm:kv_cache_usage_perc"
	metricLegacyGPUCacheUsage = "vllm:gpu_cache_usage_perc"
	prometheusTTFTQuantile    = 0.95
	fractionToPercentage      = 100
	secondsToMilliseconds     = 1000
)

var _ Collector = prometheusCollector{}

// prometheusCollector reads the vLLM metrics endpoint directly from each
// EndpointSlice address. It intentionally reports only per-backend vLLM
// values; node-level GPU and Kubernetes signals are not incorrectly attached
// to a Pod until an explicit identity mapping exists.
type prometheusCollector struct {
	httpClient  *http.Client
	metricsPath string
	clockSource Clock
}

// NewPrometheus constructs the vLLM Prometheus implementation behind Collector.
func NewPrometheus(config PrometheusConfig) Collector {
	return prometheusCollector{httpClient: config.HTTPClient, metricsPath: config.MetricsPath, clockSource: config.Clock}
}

func (c prometheusCollector) Collect(ctx context.Context, candidate backend.Backend) Backend {
	now := c.clock()
	state := Backend{Status: StatusUnavailable, Source: candidate.URL, ObservedAt: now}
	metricsURL, err := metricsURL(candidate.URL, c.metricsPath)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	families, err := fetch(ctx, c.httpClient, metricsURL)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	observed := 0
	if value, ok := sum(families, metricRequestsWaiting, metricRequestQueueSize); ok {
		state.QueueLength, observed = metricSample(value, now, candidate.URL), observed+1
	}
	if value, ok := sum(families, metricRequestsRunning); ok {
		state.RunningRequests, observed = metricSample(value, now, candidate.URL), observed+1
	}
	var cacheHits, cacheQueries float64
	if value, ok := sum(families, metricPrefixCacheHits); ok {
		cacheHits, observed = value, observed+1
	}
	if value, ok := sum(families, metricPrefixCacheQueries); ok {
		cacheQueries = value
	}
	if cacheQueries > 0 {
		state.PrefixCacheHitRate = cacheHits / cacheQueries
	}
	if family := first(families, metricTTFT, metricRequestTTFT); family != nil {
		if value, ok := quantile(family, prometheusTTFTQuantile); ok {
			state.TTFTP95Milliseconds, observed = value*secondsToMilliseconds, observed+1
		}
	}
	if value, ok := average(families, metricKVCacheUsage, metricLegacyGPUCacheUsage); ok {
		if value <= 1 {
			value *= fractionToPercentage
		}
		state.KVCacheUsagePercent, observed = value, observed+1
	}
	if observed == 0 {
		state.Status = StatusDegraded
		state.Error = "vLLM metrics endpoint had no recognized serving metrics"
		return state
	}
	state.Status = StatusOK
	return state
}

func metricSample(value float64, observedAt time.Time, source string) Sample[float64] {
	return Sample[float64]{Value: value, Valid: true, ObservedAt: observedAt, Source: source}
}

func (c prometheusCollector) clock() time.Time {
	if c.clockSource == nil {
		return time.Now()
	}
	return c.clockSource()
}

func metricsURL(raw, metricsPath string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("backend URL must be absolute: %q", raw)
	}
	if parsed.Scheme != httpURLScheme && parsed.Scheme != httpsURLScheme {
		return "", fmt.Errorf("backend URL scheme must be http or https: %q", raw)
	}
	if strings.TrimSpace(metricsPath) == "" {
		return "", fmt.Errorf("metrics path must not be empty")
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
	request.Header.Set(headerAccept, prometheusTextMedia)
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
