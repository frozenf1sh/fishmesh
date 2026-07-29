package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

// Clock 让报告时间和测试可控，避免业务代码直接依赖 time.Now。
type Clock func() time.Time

// Engine 是慢速控制面的分析入口。MVP 采用“并行收集全部领域工具 -> 规则
// 评估”的单轮 harness；未来可把 Planner 插入同一端口实现真正的 tool-calling loop。
type Engine struct {
	registry *Registry
	policy   domain.DiagnosisPolicy
	clock    Clock
}

func NewEngine(registry *Registry, policy domain.DiagnosisPolicy, clock Clock) (*Engine, error) {
	if registry == nil || policy == nil {
		return nil, fmt.Errorf("registry and policy are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Engine{registry: registry, policy: policy, clock: clock}, nil
}

func (e *Engine) Analyze(ctx context.Context, incident domain.Incident) domain.AnalysisReport {
	startedAt := e.clock()
	if incident.ID == "" {
		incident.ID = newID()
	}
	if incident.Window == "" {
		incident.Window = "5m"
	}
	signals := e.registry.Collect(ctx, incident)
	completedAt := e.clock()
	return domain.AnalysisReport{ID: newID(), Incident: incident, StartedAt: startedAt, CompletedAt: completedAt, Planner: e.policy.Name(), Tools: signals, Diagnosis: e.policy.Evaluate(incident, signals, completedAt)}
}

func newID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "local"
	}
	return hex.EncodeToString(bytes)
}
