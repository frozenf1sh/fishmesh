package adapters

import (
	"fmt"
	"io"
	"math"
	"sort"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func parseMetricFamilies(input io.Reader) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(input)
	if err != nil {
		return nil, fmt.Errorf("parse prometheus metrics: %w", err)
	}
	return families, nil
}

func parseGatewayMetrics(input io.Reader) (map[string]float64, error) {
	families, err := parseMetricFamilies(input)
	if err != nil {
		return nil, err
	}
	return gatewayValues(families), nil
}

func gatewayValues(families map[string]*dto.MetricFamily) map[string]float64 {
	values := map[string]float64{}
	for name, family := range families {
		var total float64
		for _, metric := range family.Metric {
			switch {
			case metric.Gauge != nil:
				total += metric.Gauge.GetValue()
			case metric.Counter != nil:
				total += metric.Counter.GetValue()
			}
		}
		switch name {
		case "fishmesh_gateway_inflight_requests":
			values["inflight_requests"] = total
		case "fishmesh_gateway_upstream_errors_total":
			values["upstream_errors_total"] = total
		case "fishmesh_gateway_route_fallbacks_total":
			values["route_fallbacks_total"] = total
		case "fishmesh_gateway_requests_total":
			values["requests_total"] = total
		}
	}
	return values
}

func metricSum(families map[string]*dto.MetricFamily, names ...string) (float64, bool) {
	for _, name := range names {
		family, ok := families[name]
		if !ok {
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

func metricAverage(families map[string]*dto.MetricFamily, names ...string) (float64, bool) {
	for _, name := range names {
		family, ok := families[name]
		if !ok || len(family.Metric) == 0 {
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
		return total / float64(len(family.Metric)), true
	}
	return 0, false
}

// histogramQuantile computes a bounded quantile from Prometheus histogram
// buckets. It is intentionally generic so vLLM metric name changes stay in the
// adapter, not in the domain policy.
func histogramQuantile(family *dto.MetricFamily, quantile float64) (float64, bool) {
	if family == nil || len(family.Metric) == 0 {
		return 0, false
	}
	buckets := make(map[float64]uint64)
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
	target := total * quantile
	for _, upperBound := range upperBounds {
		if float64(buckets[upperBound]) >= target {
			return upperBound, true
		}
	}
	return upperBounds[len(upperBounds)-1], true
}
