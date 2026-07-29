package adapters

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
	dto "github.com/prometheus/client_model/go"
)

// VLLMMetricsTool aggregates the low-cardinality serving signals exposed by
// one or more vLLM /metrics endpoints. Endpoint identity remains an adapter
// concern until EndpointSlice discovery is available.
type VLLMMetricsTool struct {
	URLs       []string
	HTTPClient *http.Client
	Clock      application.Clock
}

func (t VLLMMetricsTool) Descriptor() domain.ToolDescriptor {
	return domain.ToolDescriptor{Name: "query_llm_metrics", Description: "读取 vLLM queue、running、TTFT 和 Prefix Cache 指标"}
}

func (t VLLMMetricsTool) Collect(ctx context.Context, _ domain.Incident) domain.Signal {
	clock := defaultClock(t.Clock)
	signal := domain.Signal{Name: t.Descriptor().Name, Source: strings.Join(t.URLs, ","), Status: domain.SignalUnavailable, ObservedAt: clock()}
	if len(t.URLs) == 0 {
		signal.Error = "no vLLM metrics URL configured"
		return signal
	}
	var successes int
	var failures []string
	var queue, running, cacheHits, cacheQueries float64
	var cacheHitSamples int
	var maxTTFTP95 float64
	var maxGPUCache float64
	for _, targetURL := range t.URLs {
		families, err := fetchMetricFamilies(ctx, t.HTTPClient, targetURL)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", targetURL, err))
			continue
		}
		successes++
		if value, ok := metricSum(families, "vllm:num_requests_waiting", "vllm:request_queue_size"); ok {
			queue += value
		}
		if value, ok := metricSum(families, "vllm:num_requests_running"); ok {
			running += value
		}
		if value, ok := metricSum(families, "vllm:prefix_cache_hits_total"); ok {
			cacheHits += value
		}
		if value, ok := metricSum(families, "vllm:prefix_cache_queries_total"); ok {
			cacheQueries += value
		}
		if cacheQueries > 0 && cacheHits >= 0 {
			cacheHitSamples++
		}
		if family := firstMetricFamily(families, "vllm:time_to_first_token_seconds", "vllm:request_time_to_first_token_seconds"); family != nil {
			if value, ok := histogramQuantile(family, 0.95); ok && value*1000 > maxTTFTP95 {
				maxTTFTP95 = value * 1000
			}
		}
		if value, ok := metricAverage(families, "vllm:gpu_cache_usage_perc"); ok {
			if value <= 1 {
				value *= 100
			}
			if value > maxGPUCache {
				maxGPUCache = value
			}
		}
	}
	if successes == 0 {
		signal.Error = strings.Join(failures, "; ")
		return signal
	}
	signal.Status = domain.SignalOK
	if len(failures) > 0 {
		signal.Status = domain.SignalDegraded
		signal.Error = strings.Join(failures, "; ")
	}
	signal.Values = map[string]float64{"backend_count": float64(successes), "queue_length": queue, "running_requests": running}
	if cacheHitSamples > 0 && cacheQueries > 0 {
		signal.Values["prefix_cache_hit_rate"] = cacheHits / cacheQueries
	}
	if maxTTFTP95 > 0 {
		signal.Values["ttft_p95_ms"] = maxTTFTP95
	}
	if maxGPUCache > 0 {
		signal.Values["gpu_cache_usage_percent"] = maxGPUCache
	}
	signal.Summary = fmt.Sprintf("vLLM metrics collected from %d/%d endpoint(s)", successes, len(t.URLs))
	return signal
}

// GPUStatusTool consumes DCGM/NVIDIA exporter metrics. Keeping it HTTP-based
// means the analyst remains CPU-only and does not require privileged GPU access.
type GPUStatusTool struct {
	URL        string
	HTTPClient *http.Client
	Clock      application.Clock
}

func (t GPUStatusTool) Descriptor() domain.ToolDescriptor {
	return domain.ToolDescriptor{Name: "query_gpu_status", Description: "读取 GPU 显存、利用率、温度和 OOM 状态"}
}

func (t GPUStatusTool) Collect(ctx context.Context, _ domain.Incident) domain.Signal {
	clock := defaultClock(t.Clock)
	signal := domain.Signal{Name: t.Descriptor().Name, Source: t.URL, Status: domain.SignalUnavailable, ObservedAt: clock()}
	if strings.TrimSpace(t.URL) == "" {
		signal.Error = "GPU metrics URL is not configured"
		return signal
	}
	families, err := fetchMetricFamilies(ctx, t.HTTPClient, t.URL)
	if err != nil {
		signal.Error = err.Error()
		return signal
	}
	signal.Status = domain.SignalOK
	signal.Values = map[string]float64{}
	if value, ok := metricAverage(families, "DCGM_FI_DEV_GPU_UTIL", "nvidia_gpu_utilization_percent"); ok {
		signal.Values["gpu_utilization_percent"] = value
	}
	if value, ok := metricSum(families, "DCGM_FI_DEV_FB_USED"); ok {
		if free, freeOK := metricSum(families, "DCGM_FI_DEV_FB_FREE"); freeOK && value+free > 0 {
			signal.Values["gpu_memory_percent"] = value / (value + free) * 100
		}
	}
	if value, ok := metricAverage(families, "DCGM_FI_DEV_MEMORY_TEMP", "DCGM_FI_DEV_GPU_TEMP", "nvidia_gpu_temperature_celsius"); ok {
		signal.Values["gpu_temperature_celsius"] = value
	}
	if value, ok := metricSum(families, "DCGM_FI_DEV_XID_ERRORS", "nvidia_gpu_oom_events_total"); ok {
		signal.Values["gpu_oom_events"] = value
	}
	signal.Summary = "GPU metrics collected"
	return signal
}

func firstMetricFamily(families map[string]*dto.MetricFamily, names ...string) *dto.MetricFamily {
	for _, name := range names {
		if family := families[name]; family != nil {
			return family
		}
	}
	return nil
}

func defaultClock(clock application.Clock) application.Clock {
	if clock == nil {
		return time.Now
	}
	return clock
}
