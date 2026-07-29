package application

import (
	"context"
	"fmt"
	"sort"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

// Tool 是集群分析工具的最小端口。工具只读、领域化，并返回结构化 Signal；
// 禁止把 kubectl shell 或任意命令执行器作为 Tool 暴露给分析器。
type Tool interface {
	Descriptor() domain.ToolDescriptor
	Collect(context.Context, domain.Incident) domain.Signal
}

// Registry 管理工具生命周期和稳定执行顺序。稳定顺序让 JSON artifact 易于
// 对比，也让未来的 tool-calling planner 能使用同一套注册表。
type Registry struct {
	tools []Tool
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool == nil {
			return nil, fmt.Errorf("tool must not be nil")
		}
		name := tool.Descriptor().Name
		if name == "" {
			return nil, fmt.Errorf("tool name must not be empty")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate tool: %s", name)
		}
		seen[name] = struct{}{}
	}
	ordered := append([]Tool(nil), tools...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Descriptor().Name < ordered[j].Descriptor().Name })
	return &Registry{tools: ordered}, nil
}

func (r *Registry) Descriptors() []domain.ToolDescriptor {
	descriptors := make([]domain.ToolDescriptor, 0, len(r.tools))
	for _, tool := range r.tools {
		descriptors = append(descriptors, tool.Descriptor())
	}
	return descriptors
}

func (r *Registry) Collect(ctx context.Context, incident domain.Incident) []domain.Signal {
	results := make([]domain.Signal, 0, len(r.tools))
	for _, tool := range r.tools {
		results = append(results, tool.Collect(ctx, incident))
	}
	return results
}
