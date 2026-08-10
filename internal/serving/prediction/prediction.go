// Package prediction 负责以有界、逐 backend 的首 token 观测构造只读预测。
package prediction

import (
	"fmt"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	// ModeOff 保持既有路由行为，既不保存样本也不产生影子结论。
	ModeOff Mode = "off"
	// ModeShadow 只计算 would-select；它绝不参与实际 backend 选择。
	ModeShadow Mode = "shadow"

	StatusDisabled         Status = "disabled"
	StatusLoadUnavailable  Status = "load-unavailable"
	StatusInsufficientData Status = "insufficient-data"
	StatusAvailable        Status = "available"
)

// Mode 控制预测域是否仅以影子方式工作。
type Mode string

// Status 解释影子结论是否可用；不可用不是零 TTFT 或零负载。
type Status string

// Config 是 prediction domain 自己拥有的有界保留与置信度阈值。
type Config struct {
	Mode           Mode
	MaxSamples     int
	MaxSampleAge   time.Duration
	MinimumSamples int
	Clock          func() time.Time
}

// DefaultConfig 返回保守的本地 profile 边界。默认关闭，不改变现有路由。
func DefaultConfig() Config {
	return Config{Mode: ModeOff, MaxSamples: 128, MaxSampleAge: 15 * time.Minute, MinimumSamples: 16}
}

// Validate 拒绝会造成无界保存或伪置信度的配置。
func (c Config) Validate() error {
	if c.Mode != ModeOff && c.Mode != ModeShadow {
		return fmt.Errorf("unsupported prediction mode %q", c.Mode)
	}
	if c.MaxSamples <= 0 || c.MaxSampleAge <= 0 || c.MinimumSamples <= 0 || c.MinimumSamples > c.MaxSamples {
		return fmt.Errorf("prediction sample bounds must be positive and minimum must not exceed maximum")
	}
	return nil
}

// Features 是不含 prompt、token IDs 或请求身份的同量纲数值输入。
// LoadValid=false 表示 queue/running 未知，不能被解释为二者均为零。
type Features struct {
	UncachedTokens int64
	QueueDepth     int64
	Running        int64
	LocalInflight  int64
	LoadValid      bool
}

// Candidate 是一次影子比较中的一个可路由 backend 投影。
type Candidate struct {
	Backend  backend.ID
	Features Features
}

// BeginInput 固定实际选择和同一时刻的全部候选特征。
type BeginInput struct {
	Selected   backend.ID
	Features   Features
	Candidates []Candidate
}

// Shadow 是预测域给 delivery 的稳定值对象。WouldSelect 仅表示影子模型的输出。
type Shadow struct {
	Status            Status
	WouldSelect       backend.ID
	SelectedEstimate  time.Duration
	WouldSelectTTFT   time.Duration
	SamplesPerBackend int
}

// Observation 是首个 SSE 事件发生后可安全投影的预测误差。
type Observation struct {
	Valid     bool
	Backend   backend.ID
	Predicted time.Duration
	Actual    time.Duration
	Error     time.Duration
}

// Tracker 是 requestpath 注入的真实替换边界。它只能记录数值和时长，不能影响 routing 决策。
type Tracker interface {
	Begin(BeginInput) (Ticket, Shadow)
	Reconcile([]backend.ID)
}

// Ticket 把一次选择与之后首个 SSE 事件关联；重复 ObserveFirstToken 是幂等的。
type Ticket struct{ state *ticketState }

// ObserveFirstToken 写入一次有界训练样本，并返回该实际 backend 的预测误差。
func (t Ticket) ObserveFirstToken(ttft time.Duration) Observation {
	if t.state == nil {
		return Observation{}
	}
	return t.state.observe(ttft)
}
