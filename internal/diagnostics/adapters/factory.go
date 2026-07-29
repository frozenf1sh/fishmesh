package adapters

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/config"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

// NewRegistryForConfig 负责组装运行模式，不让 cmd 包了解具体工具实现。
// demo 模式用于本地和 CI 验证；gateway 模式接入第一条真实 Gateway 指标链路，
// 其余数据源明确标记 unavailable，避免用样例数据掩盖生产缺口。
func NewRegistryForConfig(runtimeConfig config.Config, client *http.Client, clock application.Clock) (*application.Registry, error) {
	if clock == nil {
		clock = time.Now
	}
	if runtimeConfig.Mode == "gateway" {
		return application.NewRegistry(
			GatewayMetricsTool{URL: runtimeConfig.GatewayMetricsURL, HTTPClient: client, Clock: clock},
			UnavailableTool{DescriptorValue: domain.ToolDescriptor{Name: "query_ebpf_network", Description: "读取节点 TCP RTT、重传和 socket stall"}, Clock: clock},
			UnavailableTool{DescriptorValue: domain.ToolDescriptor{Name: "query_gpu_status", Description: "读取 GPU 显存、利用率、温度和 OOM 状态"}, Clock: clock},
			UnavailableTool{DescriptorValue: domain.ToolDescriptor{Name: "query_kubernetes_events", Description: "读取 namespace-scoped Kubernetes Warning 事件"}, Clock: clock},
			UnavailableTool{DescriptorValue: domain.ToolDescriptor{Name: "query_llm_metrics", Description: "读取 vLLM queue、running、TTFT 和 Prefix Cache 指标"}, Clock: clock},
		)
	}
	if runtimeConfig.Mode == "observability" {
		kubernetesClient := client
		needsKubernetesTLS := strings.HasPrefix(runtimeConfig.KubernetesAPIURL, "https://") || os.Getenv("KUBERNETES_SERVICE_HOST") != ""
		if needsKubernetesTLS {
			var err error
			kubernetesClient, err = NewKubernetesHTTPClient(client, runtimeConfig.KubernetesCAFile)
			if err != nil {
				return nil, err
			}
		}
		return application.NewRegistry(
			GatewayMetricsTool{URL: runtimeConfig.GatewayMetricsURL, HTTPClient: client, Clock: clock},
			VLLMMetricsTool{URLs: runtimeConfig.VLLMMetricsURLs, HTTPClient: client, Clock: clock},
			GPUStatusTool{URL: runtimeConfig.GPUMetricsURL, HTTPClient: client, Clock: clock},
			KubernetesEventsTool{Namespace: runtimeConfig.KubernetesNamespace, BaseURL: runtimeConfig.KubernetesAPIURL, TokenFile: runtimeConfig.KubernetesTokenFile, HTTPClient: kubernetesClient, Clock: clock},
			UnavailableTool{DescriptorValue: domain.ToolDescriptor{Name: "query_ebpf_network", Description: "读取节点 TCP RTT、重传和 socket stall"}, Clock: clock},
		)
	}
	return NewDemoRegistry(clock)
}

// NewDemoRegistry 返回一个可重复的故障场景：Prefix Cache 命中率下降，但
// 网络、GPU 和队列正常。它对应最终方案中的第一个只读诊断 demo。
func NewDemoRegistry(clock application.Clock) (*application.Registry, error) {
	if clock == nil {
		clock = time.Now
	}
	return application.NewRegistry(
		StaticTool{DescriptorValue: domain.ToolDescriptor{Name: "query_ebpf_network", Description: "读取节点 TCP RTT、重传和 socket stall"}, SignalValue: domain.Signal{Name: "query_ebpf_network", Source: "demo-fixture", Status: domain.SignalOK, Values: map[string]float64{"tcp_rtt_ms": 12, "retransmission_rate": 0.001, "socket_stall": 0}, Summary: "network healthy"}, Clock: clock},
		StaticTool{DescriptorValue: domain.ToolDescriptor{Name: "query_gpu_status", Description: "读取 GPU 显存、利用率、温度和 OOM 状态"}, SignalValue: domain.Signal{Name: "query_gpu_status", Source: "demo-fixture", Status: domain.SignalOK, Values: map[string]float64{"gpu_memory_percent": 68, "gpu_utilization_percent": 74, "gpu_temperature_celsius": 67}, Summary: "GPU has headroom"}, Clock: clock},
		StaticTool{DescriptorValue: domain.ToolDescriptor{Name: "query_kubernetes_events", Description: "读取 namespace-scoped Kubernetes Warning 事件"}, SignalValue: domain.Signal{Name: "query_kubernetes_events", Source: "demo-fixture", Status: domain.SignalOK, Values: map[string]float64{"warning_events": 0}, Summary: "no warning events"}, Clock: clock},
		StaticTool{DescriptorValue: domain.ToolDescriptor{Name: "query_llm_metrics", Description: "读取 vLLM queue、running、TTFT 和 Prefix Cache 指标"}, SignalValue: domain.Signal{Name: "query_llm_metrics", Source: "demo-fixture", Status: domain.SignalOK, Values: map[string]float64{"ttft_p95_ms": 2300, "queue_length": 2, "running_requests": 4, "prefix_cache_hit_rate": 0.35}, Summary: "prefix cache locality degraded"}, Clock: clock},
		StaticTool{DescriptorValue: domain.ToolDescriptor{Name: "query_gateway_stats", Description: "读取 FishMesh Gateway 的请求、fallback 和 in-flight 聚合指标"}, SignalValue: domain.Signal{Name: "query_gateway_stats", Source: "demo-fixture", Status: domain.SignalOK, Values: map[string]float64{"requests_total": 200, "route_fallbacks_total": 0, "inflight_requests": 1}, Summary: "gateway serving normally"}, Clock: clock},
	)
}
