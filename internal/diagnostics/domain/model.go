package domain

import "time"

// Incident 是一次待诊断的短周期服务异常。它刻意只描述症状和时间窗口，
// 不把 Prometheus 原始样本直接暴露给上层；工具负责按需收集结构化信号。
type Incident struct {
	ID          string  `json:"id"`
	Metric      string  `json:"metric"`
	Description string  `json:"description,omitempty"`
	Baseline    float64 `json:"baseline,omitempty"`
	Current     float64 `json:"current,omitempty"`
	Unit        string  `json:"unit,omitempty"`
	Window      string  `json:"window,omitempty"`
}

// SignalStatus 表示一个观测工具是否成功得到可用事实。
type SignalStatus string

const (
	SignalOK          SignalStatus = "ok"
	SignalDegraded    SignalStatus = "degraded"
	SignalUnavailable SignalStatus = "unavailable"
)

// Signal 是工具输出的稳定契约。Values 只放有明确含义的数值，Attributes
// 用于 backend、Pod 或事件类型等低基数维度，避免把原始日志塞进上下文。
type Signal struct {
	Name       string             `json:"name"`
	Source     string             `json:"source"`
	Status     SignalStatus       `json:"status"`
	ObservedAt time.Time          `json:"observed_at"`
	Values     map[string]float64 `json:"values,omitempty"`
	Attributes map[string]string  `json:"attributes,omitempty"`
	Summary    string             `json:"summary,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// Evidence 是诊断结论中的可审计依据，必须能回指到某个 Signal。
type Evidence struct {
	Signal      string `json:"signal"`
	Observation string `json:"observation"`
	Impact      string `json:"impact"`
}

// Recommendation 是只读建议。MVP 不执行 Action，后续受控变更也必须先经过
// 策略门控、TTL 和审计层，因此这里没有 kubectl 命令字段。
type Recommendation struct {
	Code      string    `json:"code"`
	Summary   string    `json:"summary"`
	Risk      string    `json:"risk"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Diagnosis 是规则分析器或未来 LLM narrator 的统一输出格式。
type Diagnosis struct {
	Code           string         `json:"code"`
	Summary        string         `json:"summary"`
	Confidence     float64        `json:"confidence"`
	Evidence       []Evidence     `json:"evidence"`
	Recommendation Recommendation `json:"recommendation"`
}

// ToolDescriptor 用于 /v1/tools，帮助调用方知道当前运行时已注册哪些领域工具。
type ToolDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AnalysisReport 是 API 的主要结果。Tools 保留原始结构化信号，便于复盘和后续
// 替换 LLM；Diagnosis 则是面向人和自动化门控的稳定结论。
type AnalysisReport struct {
	ID          string    `json:"id"`
	Incident    Incident  `json:"incident"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Planner     string    `json:"planner"`
	Tools       []Signal  `json:"tools"`
	Diagnosis   Diagnosis `json:"diagnosis"`
}
