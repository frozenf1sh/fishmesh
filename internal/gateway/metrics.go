package gateway

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	inflight       prometheus.Gauge
	requests       *prometheus.CounterVec
	requestSeconds *prometheus.HistogramVec
	ttftSeconds    prometheus.Histogram
	upstreamErrors prometheus.Counter
}

func newMetrics(registry *prometheus.Registry) *metrics {
	m := &metrics{
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "inflight_requests", Help: "Requests currently proxied by the gateway.",
		}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "requests_total", Help: "Completed gateway requests.",
		}, []string{"method", "status"}),
		requestSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "request_duration_seconds", Help: "End-to-end gateway request duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "status"}),
		ttftSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "first_sse_event_seconds", Help: "Time from request receipt until the first non-terminal SSE event.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
		upstreamErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "fishmesh", Subsystem: "gateway", Name: "upstream_errors_total", Help: "Upstream transport failures.",
		}),
	}
	registry.MustRegister(m.inflight, m.requests, m.requestSeconds, m.ttftSeconds, m.upstreamErrors)
	return m
}
