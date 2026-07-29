package adapters

import (
	"context"
	"net/http"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

// StaticTool 是开发和演示用的可注入工具实现。它模拟领域工具的输出，但
// 不伪装成线上采集；API 会明确返回 source=demo，方便验证数据契约。
type StaticTool struct {
	DescriptorValue domain.ToolDescriptor
	SignalValue     domain.Signal
	Clock           application.Clock
}

func (t StaticTool) Descriptor() domain.ToolDescriptor { return t.DescriptorValue }

func (t StaticTool) Collect(context.Context, domain.Incident) domain.Signal {
	signal := t.SignalValue
	if signal.ObservedAt.IsZero() {
		clock := t.Clock
		if clock == nil {
			clock = time.Now
		}
		signal.ObservedAt = clock()
	}
	return signal
}

// UnavailableTool 保留未来数据源的领域边界，避免直接把“尚未接入”误报为健康。
type UnavailableTool struct {
	DescriptorValue domain.ToolDescriptor
	Clock           application.Clock
}

func (t UnavailableTool) Descriptor() domain.ToolDescriptor { return t.DescriptorValue }

func (t UnavailableTool) Collect(context.Context, domain.Incident) domain.Signal {
	clock := t.Clock
	if clock == nil {
		clock = time.Now
	}
	return domain.Signal{Name: t.DescriptorValue.Name, Source: "not-configured", Status: domain.SignalUnavailable, Summary: "collector is not configured", Error: "data source is not configured", ObservedAt: clock()}
}

// GatewayMetricsTool 读取 Gateway 的 Prometheus exposition，作为第一条真实
// 数据链路。它只抽取低基数聚合值，不把 request ID、prompt 或原始 label 放进上下文。
type GatewayMetricsTool struct {
	URL        string
	HTTPClient *http.Client
	Clock      application.Clock
}

func (t GatewayMetricsTool) Descriptor() domain.ToolDescriptor {
	return domain.ToolDescriptor{Name: "query_gateway_stats", Description: "读取 FishMesh Gateway 的请求、fallback 和 in-flight 聚合指标"}
}

func (t GatewayMetricsTool) Collect(ctx context.Context, _ domain.Incident) domain.Signal {
	clock := t.Clock
	if clock == nil {
		clock = time.Now
	}
	signal := domain.Signal{Name: t.Descriptor().Name, Source: t.URL, Status: domain.SignalUnavailable, ObservedAt: clock()}
	families, err := fetchMetricFamilies(ctx, t.HTTPClient, t.URL)
	if err != nil {
		signal.Error = err.Error()
		return signal
	}
	values := gatewayValues(families)
	signal.Status = domain.SignalOK
	signal.Values = values
	signal.Summary = "gateway metrics collected"
	return signal
}
