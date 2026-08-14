package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	defaultGatewayMetricsInterval = 250 * time.Millisecond
	metricAdmittedRequests        = "fishmesh_gateway_admitted_requests_total"
	metricCompletedRequests       = "fishmesh_gateway_requests_total"
	metricAdmissionRejections     = "fishmesh_gateway_admission_rejections_total"
	metricInflightRequests        = "fishmesh_gateway_inflight_requests"
)

// GatewayMetricsWindow is an optional, low-cardinality snapshot of the Gateway
// counters observed during one benchmark run. It is deliberately separate from
// the client completion rate: only this window can support Gateway Little's Law.
type GatewayMetricsWindow struct {
	Valid              bool      `json:"valid"`
	Error              string    `json:"error,omitempty"`
	StartAt            time.Time `json:"start_at,omitempty"`
	EndAt              time.Time `json:"end_at,omitempty"`
	ElapsedMS          float64   `json:"elapsed_ms,omitempty"`
	Samples            int       `json:"samples,omitempty"`
	WarmupRequests     int       `json:"warmup_requests,omitempty"`
	Segments           int       `json:"segments,omitempty"`
	WarmupExcluded     bool      `json:"warmup_excluded"`
	AdmittedDelta      float64   `json:"admitted_delta,omitempty"`
	CompletedDelta     float64   `json:"completed_delta,omitempty"`
	RejectionsDelta    float64   `json:"admission_rejections_delta,omitempty"`
	AcceptedRateQPS    float64   `json:"accepted_rate_qps,omitempty"`
	CompletedRateQPS   float64   `json:"completed_rate_qps,omitempty"`
	RejectionRateQPS   float64   `json:"admission_rejection_rate_qps,omitempty"`
	AverageInflight    float64   `json:"average_inflight,omitempty"`
	LittleLawWaitMS    float64   `json:"little_law_wait_ms,omitempty"`
	LittleLawWaitValid bool      `json:"little_law_wait_valid"`
}

// GatewayMetricsSnapshot is one point read from the Gateway /metrics endpoint.
// The parser accepts only the three metrics needed for the capacity window.
type GatewayMetricsSnapshot struct {
	ObservedAt               time.Time
	AdmittedRequestsTotal    float64
	CompletedRequestsTotal   float64
	AdmissionRejectionsTotal float64
	InflightRequests         float64
}

// GatewayMetricsReader supplies one Gateway metrics snapshot.
type GatewayMetricsReader interface {
	Snapshot(context.Context) (GatewayMetricsSnapshot, error)
}

// PrometheusGatewayMetricsConfig configures the optional benchmark metrics reader.
type PrometheusGatewayMetricsConfig struct {
	Endpoint   string
	HTTPClient *http.Client
}

// PrometheusGatewayMetricsReader reads the Gateway's own Prometheus endpoint.
type PrometheusGatewayMetricsReader struct {
	httpClient *http.Client
	endpoint   string
}

var _ GatewayMetricsReader = PrometheusGatewayMetricsReader{}

// NewPrometheusGatewayMetricsReader constructs an optional benchmark metrics reader.
func NewPrometheusGatewayMetricsReader(config PrometheusGatewayMetricsConfig) (GatewayMetricsReader, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("gateway metrics endpoint must be an absolute URL: %q", config.Endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("gateway metrics endpoint scheme must be http or https: %q", config.Endpoint)
	}
	return PrometheusGatewayMetricsReader{httpClient: config.HTTPClient, endpoint: parsed.String()}, nil
}

func (r PrometheusGatewayMetricsReader) Snapshot(ctx context.Context) (GatewayMetricsSnapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, nil)
	if err != nil {
		return GatewayMetricsSnapshot{}, err
	}
	client := r.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return GatewayMetricsSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GatewayMetricsSnapshot{}, fmt.Errorf("gateway metrics endpoint returned %s", response.Status)
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return GatewayMetricsSnapshot{}, fmt.Errorf("parse gateway metrics: %w", err)
	}
	admitted, err := metricValue(families, metricAdmittedRequests)
	if err != nil {
		return GatewayMetricsSnapshot{}, err
	}
	completed, err := metricSumOrZero(families, metricCompletedRequests)
	if err != nil {
		return GatewayMetricsSnapshot{}, err
	}
	rejections, err := metricValue(families, metricAdmissionRejections)
	if err != nil {
		return GatewayMetricsSnapshot{}, err
	}
	inflight, err := metricValue(families, metricInflightRequests)
	if err != nil {
		return GatewayMetricsSnapshot{}, err
	}
	return GatewayMetricsSnapshot{
		ObservedAt: time.Now().UTC(), AdmittedRequestsTotal: admitted, CompletedRequestsTotal: completed, AdmissionRejectionsTotal: rejections, InflightRequests: inflight,
	}, nil
}

func metricValue(families map[string]*dto.MetricFamily, name string) (float64, error) {
	family := families[name]
	if family == nil || len(family.Metric) != 1 {
		return 0, fmt.Errorf("gateway metrics missing singleton %q", name)
	}
	metric := family.Metric[0]
	if metric.Gauge != nil {
		return metric.Gauge.GetValue(), nil
	}
	if metric.Counter != nil {
		return metric.Counter.GetValue(), nil
	}
	return 0, fmt.Errorf("gateway metric %q has no gauge or counter value", name)
}

func metricSum(families map[string]*dto.MetricFamily, name string) (float64, error) {
	family := families[name]
	if family == nil || len(family.Metric) == 0 {
		return 0, fmt.Errorf("gateway metrics missing %q", name)
	}
	var total float64
	for _, metric := range family.Metric {
		if metric.Counter == nil {
			return 0, fmt.Errorf("gateway metric %q contains a non-counter sample", name)
		}
		total += metric.Counter.GetValue()
	}
	return total, nil
}

func metricSumOrZero(families map[string]*dto.MetricFamily, name string) (float64, error) {
	if families[name] == nil {
		return 0, nil
	}
	return metricSum(families, name)
}

type gatewayMetricsRun struct {
	reader   GatewayMetricsReader
	interval time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}

	mu      sync.Mutex
	samples []GatewayMetricsSnapshot
	err     error
}

func beginGatewayMetricsRun(ctx context.Context, reader GatewayMetricsReader, interval time.Duration) *gatewayMetricsRun {
	if interval <= 0 {
		interval = defaultGatewayMetricsInterval
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &gatewayMetricsRun{reader: reader, interval: interval, ctx: runCtx, cancel: cancel, done: make(chan struct{})}
	first, err := reader.Snapshot(runCtx)
	if err != nil {
		run.err = err
		close(run.done)
		return run
	}
	run.samples = append(run.samples, first)
	go run.poll()
	return run
}

func (r *gatewayMetricsRun) poll() {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			sample, err := r.reader.Snapshot(r.ctx)
			r.mu.Lock()
			if err != nil {
				if !errors.Is(err, context.Canceled) || r.ctx.Err() == nil {
					r.err = err
				}
			} else {
				r.samples = append(r.samples, sample)
			}
			r.mu.Unlock()
		}
	}
}

func (r *gatewayMetricsRun) stop(ctx context.Context) GatewayMetricsWindow {
	r.cancel()
	<-r.done
	last, err := r.reader.Snapshot(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.samples = append(r.samples, last)
	} else if r.err == nil {
		r.err = err
	}
	if r.err != nil {
		return GatewayMetricsWindow{Error: r.err.Error()}
	}
	return gatewayMetricsWindow(r.samples)
}

func gatewayMetricsWindow(samples []GatewayMetricsSnapshot) GatewayMetricsWindow {
	if len(samples) < 2 {
		return GatewayMetricsWindow{Error: "gateway metrics window needs at least two samples", Samples: len(samples)}
	}
	start, end := samples[0], samples[len(samples)-1]
	if !end.ObservedAt.After(start.ObservedAt) {
		return GatewayMetricsWindow{Error: "gateway metrics window is not positive", Samples: len(samples)}
	}
	admittedDelta := end.AdmittedRequestsTotal - start.AdmittedRequestsTotal
	completedDelta := end.CompletedRequestsTotal - start.CompletedRequestsTotal
	rejectionsDelta := end.AdmissionRejectionsTotal - start.AdmissionRejectionsTotal
	if admittedDelta < 0 || completedDelta < 0 || rejectionsDelta < 0 {
		return GatewayMetricsWindow{Error: "gateway counter reset during metrics window", Samples: len(samples)}
	}
	elapsed := end.ObservedAt.Sub(start.ObservedAt)
	area := 0.0
	for index := 1; index < len(samples); index++ {
		previous, current := samples[index-1], samples[index]
		if !current.ObservedAt.After(previous.ObservedAt) {
			return GatewayMetricsWindow{Error: "gateway metrics samples are not ordered", Samples: len(samples)}
		}
		area += (previous.InflightRequests + current.InflightRequests) / 2 * current.ObservedAt.Sub(previous.ObservedAt).Seconds()
	}
	averageInflight := area / elapsed.Seconds()
	acceptedRate := admittedDelta / elapsed.Seconds()
	completedRate := completedDelta / elapsed.Seconds()
	window := GatewayMetricsWindow{
		Valid: true, StartAt: start.ObservedAt, EndAt: end.ObservedAt, ElapsedMS: durationMS(elapsed), Samples: len(samples), Segments: 1,
		AdmittedDelta: admittedDelta, CompletedDelta: completedDelta, RejectionsDelta: rejectionsDelta, AcceptedRateQPS: acceptedRate, CompletedRateQPS: completedRate,
		RejectionRateQPS: rejectionsDelta / elapsed.Seconds(),
		AverageInflight:  averageInflight,
	}
	if acceptedRate > 0 {
		window.LittleLawWaitMS = averageInflight / acceptedRate * float64(time.Second/time.Millisecond)
		window.LittleLawWaitValid = true
	}
	return window
}

func combineGatewayMetricsWindows(windows []GatewayMetricsWindow) GatewayMetricsWindow {
	if len(windows) == 0 {
		return GatewayMetricsWindow{Error: "gateway metrics window has no measured segments"}
	}
	combined := GatewayMetricsWindow{Valid: true, Segments: len(windows)}
	activeSeconds := 0.0
	weightedInflight := 0.0
	for _, window := range windows {
		if !window.Valid {
			return GatewayMetricsWindow{Error: window.Error, Segments: len(windows)}
		}
		if combined.StartAt.IsZero() || window.StartAt.Before(combined.StartAt) {
			combined.StartAt = window.StartAt
		}
		if combined.EndAt.IsZero() || window.EndAt.After(combined.EndAt) {
			combined.EndAt = window.EndAt
		}
		active := window.ElapsedMS / float64(time.Second/time.Millisecond)
		activeSeconds += active
		weightedInflight += window.AverageInflight * active
		combined.Samples += window.Samples
		combined.AdmittedDelta += window.AdmittedDelta
		combined.CompletedDelta += window.CompletedDelta
		combined.RejectionsDelta += window.RejectionsDelta
	}
	if activeSeconds <= 0 {
		return GatewayMetricsWindow{Error: "gateway metrics segments have no positive duration", Segments: len(windows)}
	}
	combined.ElapsedMS = activeSeconds * float64(time.Second/time.Millisecond)
	combined.AcceptedRateQPS = combined.AdmittedDelta / activeSeconds
	combined.CompletedRateQPS = combined.CompletedDelta / activeSeconds
	combined.RejectionRateQPS = combined.RejectionsDelta / activeSeconds
	combined.AverageInflight = weightedInflight / activeSeconds
	if combined.AcceptedRateQPS > 0 {
		combined.LittleLawWaitMS = combined.AverageInflight / combined.AcceptedRateQPS * float64(time.Second/time.Millisecond)
		combined.LittleLawWaitValid = true
	}
	return combined
}

func benchmarkWarmupRequests(plan BenchmarkPlan) int {
	warmups := 0
	for _, scenario := range plan.Scenarios {
		warmups += scenario.WarmupRequests
	}
	return warmups
}
