// Package routing 拥有确定性、不依赖基础设施的端点选择策略。
//
// 每个策略都是纯决策函数：输入一张不可变的 Snapshot（后端列表、熔断状态、
// 本地与外部负载、KV 缓存命中投影），输出一个 Decision（选中谁、为什么）。
// routing 不发起任何网络调用，除粘性策略的记忆表外也不持有运行状态。
package routing

import (
	"fmt"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
)

const (
	// ModeLoadBalanced 按本 Gateway 视角的在途请求数做普通负载均衡。
	ModeLoadBalanced Mode = "load-balanced"
	// ModeSessionKey 使用客户端传入的 session key 建立有界粘性，并允许压力或熔断时临时溢出。
	ModeSessionKey Mode = "session-key"
	// ModeKVAware 联合真实 KV locality 与已知负载，选择等价成本最低的后端。
	ModeKVAware Mode = "kv-aware"

	// Policy 常量带版本号：同一策略更换算法或语义时必须升版本，
	// 让监控指标和历史数据能够区分不同时期的行为。
	PolicyLoadBalancedV1        Policy = "load-balanced-v1"
	PolicySessionKeyV1          Policy = "session-key-v1"
	PolicyServiceFallbackV1     Policy = "service-fallback-v1" // 固定 fallback，不是可配置 routing mode
	PolicyKVAwareV1             Policy = "kv-aware-v1"
	PolicyKVAwareStaticV1       Policy = "kv-aware-ttft-static-v1"
	PolicyKVAwareLoadFallbackV1 Policy = "kv-aware-load-fallback-v1"

	// Reason 是决策的可解释标签，由 requestpath 投影到观测与调试输出。

	// —— session key 相关 ——
	ReasonSessionKeyHit       Reason = "session-key-hit"       // session-key 记忆表命中且未溢出
	ReasonSessionKeyMiss      Reason = "session-key-miss"      // 首次见到该 key，刚建立绑定
	ReasonSessionKeySpillover Reason = "session-key-spillover" // 偏好后端受限，临时溢出到其他后端

	// —— 负载相关 ——
	ReasonLoadBalanced                  Reason = "load-balanced"                     // load-balanced 选择在途最少者
	ReasonMissingSessionKeyLoadBalanced Reason = "missing-session-key-load-balanced" // 请求没有 session key，直接选最少负载者
	ReasonQueueDepth                    Reason = "queue-depth"                       // 溢出原因：偏好后端外部队列过深
	ReasonLocalInflight                 Reason = "local-inflight"                    // 溢出原因：偏好后端本地在途过多
	ReasonIneligible                    Reason = "ineligible"                        // 被熔断/生命周期挡掉，且没有具体原因
	ReasonCircuitOpen                   Reason = "circuit-open"                      // 溢出原因：偏好后端熔断器打开

	// —— fallback 相关 ——
	ReasonDiscoveryFallback Reason = "discovery-fallback" // discovery 不可用，退回 service
	ReasonCircuitFallback   Reason = "circuit-fallback"   // 所有候选都被熔断，退回 service
	ReasonStrategyFallback  Reason = "strategy-fallback"  // 策略自身失败（如缺少必要信号）
	ReasonBackendFallback   Reason = "backend-fallback"   // 决策携带的后端不完整
	ReasonAdmissionCapacity Reason = "admission-capacity" // 准入许可耗尽

	// —— KV-aware 相关 ——
	ReasonKVAware                     Reason = "kv-aware"                        // KV-aware 策略正常完成成本式选择
	ReasonKVAwareStatic               Reason = "kv-aware-static-ttft"            // 使用 calibrated-static TTFT estimate 选择
	ReasonKVAwareStaticFallback       Reason = "kv-aware-static-fallback"        // static estimate 不完整，回到 token cost
	ReasonKVAwareSignalUnavailable    Reason = "kv-aware-signal-unavailable"     // KV/负载信号缺失，KV-aware 无法成立
	ReasonKVAwareHardOverloadFallback Reason = "kv-aware-hard-overload-fallback" // 所有健康后端硬过载，保可用性优先
)

// Mode 标识一种已配置的端点选择策略。
type Mode string

// Validate 检查模式是否属于当前支持的策略集合。
//
// 空模式保留为 load-balanced 的兼容默认值；它是配置层的合法输入，而不是一个需要在
// 请求路径中反复解释的特殊错误状态。
func (m Mode) Validate() error {
	switch m {
	case "", ModeLoadBalanced, ModeSessionKey, ModeKVAware:
		return nil
	default:
		return fmt.Errorf("unsupported routing mode %q", m)
	}
}

// Policy 标识决策使用的带版本算法语义。
type Policy string

// Reason 解释一次选择、溢出、拒绝或回退。
type Reason string

// SessionKeyConfig 是 session-key 策略的启动配置。
// 两个 delta 是"允许偏好后端比全局最少负载差多少"的容忍阈值：
// 差值未超阈值就留在偏好后端，优先保住缓存局部性。
type SessionKeyConfig struct {
	TTL             time.Duration    // 每个 key 记忆的滑动过期时长
	MaxEntries      int              // 记忆表容量上限，超过时驱逐最老条目
	InflightDelta   int64            // 本地 in-flight 的容忍差值
	QueueDepthDelta float64          // 外部队列深度的容忍差值
	Clock           func() time.Time // 时间源；nil 时由构造函数注入 time.Now
}

// KVAwareConfig 将各类已知压力折算为等价未缓存 token。它只决定同一已知 KV 状态下的
// 取舍，不接受或掩盖未知 load；具体数值必须在目标硬件/profile 上校准。
type KVAwareConfig struct {
	EstimatorMode        KVAwareEstimatorMode
	QueueTokenPenalty    int64 // 每个排队请求折算的 token 成本（尚未开始 prefill，惩罚最重）
	RunningTokenPenalty  int64 // 每个运行中请求折算的 token 成本
	InflightTokenPenalty int64 // 每个本地在途请求折算的 token 成本
}

const (
	KVAwareEstimatorTokenCost KVAwareEstimatorMode = "token-cost"
	KVAwareEstimatorStatic    KVAwareEstimatorMode = "static-ttft"
)

// KVAwareEstimatorMode 选择 KV-aware 候选比较使用的受控估算契约。
type KVAwareEstimatorMode string

func (m KVAwareEstimatorMode) Validate() error {
	switch m {
	case "", KVAwareEstimatorTokenCost, KVAwareEstimatorStatic:
		return nil
	default:
		return fmt.Errorf("unsupported KV-aware estimator mode %q", m)
	}
}

// Config 是组合根创建具体 routing strategy 的配置。
// 只有 Mode 对应的嵌套配置会被读取，其余字段允许留零值。
type Config struct {
	Mode       Mode
	Service    backend.Backend // 无 discovery 可用时的最终 fallback 端点
	SessionKey SessionKeyConfig
	KVAware    KVAwareConfig
}

// Validate 拒绝会把压力变成负成本的配置；零允许在受控实验中单独关闭一个已知项。
func (c KVAwareConfig) Validate() error {
	if err := c.EstimatorMode.Validate(); err != nil {
		return err
	}
	if c.QueueTokenPenalty < 0 || c.RunningTokenPenalty < 0 || c.InflightTokenPenalty < 0 {
		return fmt.Errorf("kv-aware token penalties must not be negative")
	}
	return nil
}

// Validate 检查有界亲和策略的启动配置。Clock 允许为空，因为构造函数会注入标准时钟；
// 这样测试可以显式提供时钟，而生产配置不必重复声明默认实现。
func (c SessionKeyConfig) Validate() error {
	if c.TTL <= 0 {
		return fmt.Errorf("session key TTL must be positive")
	}
	if c.MaxEntries <= 0 {
		return fmt.Errorf("session key max entries must be positive")
	}
	if c.InflightDelta < 0 || c.QueueDepthDelta < 0 {
		return fmt.Errorf("session key deltas must not be negative")
	}
	return nil
}

// Validate 检查 routing 组合配置。各策略自己的数值约束由嵌套配置的 Validate 方法负责，
// 这里统一负责模式、fallback backend 以及模式对应配置的选择。
func (c Config) Validate() error {
	if err := c.Mode.Validate(); err != nil {
		return err
	}
	if err := c.Service.Validate(); err != nil {
		return fmt.Errorf("routing service fallback: %w", err)
	}
	switch c.Mode {
	case ModeSessionKey:
		return c.SessionKey.Validate()
	case ModeKVAware:
		return c.KVAware.Validate()
	default:
		return nil
	}
}

// Snapshot 是一次路由决策的全部不可变输入。requestpath 在每次选择前组装一份：
// discovery 提供 Backends，circuit 提供 Ineligible，observation 提供 Loads 与
// Observations，kvcache 提供 KV，而 Inflight 由本 Gateway 自己的计数器维护。
type Snapshot struct {
	Backends     []backend.Backend                  // 当前可见的全部后端
	Inflight     map[backend.ID]int64               // 本 Gateway 视角的每后端在途请求数
	Observations map[backend.ID]observation.Backend // 外部观测（队列长度等）
	Ineligible   map[backend.ID]Reason              // 被熔断/生命周期挡掉的后端及原因
	Loads        map[backend.ID]Load                // 外部负载快照（queue/running）
	KVAware      KVAwareInput                       // KV 缓存命中的只读投影
	Estimates    map[backend.ID]LatencyEstimate     // requestpath 投影的逐 backend TTFT estimate
}

// Load 是 routing 解释的逐 backend 负载值；缺失观测必须显式标记，而不能伪装成零负载。
type Load struct {
	QueueDepth   int64 // 排队请求数（外部观测）
	Running      int64 // 正在执行的请求数（外部观测）
	LocalDelta   int64 // 尚未被外部 running 覆盖的本 Gateway 在途增量
	Valid        bool  // 观测是否有效；false 表示未知，绝不能当作 0 参与比较
	HardOverload bool  // 是否越过硬过载阈值；越过者在 KV-aware 策略中直接出局
}

const (
	EstimateConfidenceCalibrated   EstimateConfidence = "calibrated"
	EstimateConfidenceDegraded     EstimateConfidence = "degraded"
	EstimateConfidenceUncalibrated EstimateConfidence = "uncalibrated"
)

// EstimateConfidence 是 routing 可解释但不拟合的置信度值。
type EstimateConfidence string

// LatencyEstimate 不暴露 estimator 实现，只保存一次选择使用的稳定毫秒值和 provenance。
type LatencyEstimate struct {
	TTFT       time.Duration
	Valid      bool
	Confidence EstimateConfidence
	Version    string
	Reason     string
}

// KVMatch 是 routing 对真实 KV 状态的只读投影。它不暴露 kvcache 的实现或第三方类型。
// Valid=false 表示信号未知或过期；MatchedTokens=0 只有在 Valid=true 时才代表真实零命中。
type KVMatch struct {
	Valid         bool // 命中信息是否可信
	MatchedTokens int  // 与请求前缀匹配的 token 数
}

// KVAwareInput 是一次 kv-aware 决策的请求侧值对象。调用方必须用 tokenization 的
// TokenIDs 计算 PromptTokens，并把 kvcache.Match 逐字段投影为 Matches；routing 不拥有两者。
type KVAwareInput struct {
	PromptTokens int                    // 本次请求的总 prompt token 数
	Matches      map[backend.ID]KVMatch // 每个后端对本请求前缀的命中情况
}

// UsableFor 只有在每个候选都有有效 KV match 时返回 true。未知/过期不是零命中，调用方
// 必须据此显式降级到 load-balanced，而非让 KV-aware 策略猜测缺失数据。
func (i KVAwareInput) UsableFor(backends []backend.Backend) bool {
	// 空 prompt 或没有候选时，KV-aware 决策无从谈起。
	if i.PromptTokens <= 0 || len(backends) == 0 {
		return false
	}
	// 逐个检查：缺条目、条目无效、数值越界都视为不可用。
	for _, candidate := range backends {
		match, ok := i.Matches[candidate.ID]
		if !ok || !match.Valid || match.MatchedTokens < 0 || match.MatchedTokens > i.PromptTokens {
			return false
		}
	}
	return true
}

// Decision 记录选中的后端与可解释的策略结果。
//
// Backend 是实际要用的后端；PreferredBackendID 是策略"理想中"的后端——
// 两者只在 spillover/fallback 场景下不同，供观测区分"策略想选谁"与"最终用了谁"。
type Decision struct {
	Backend            backend.Backend
	PreferredBackendID backend.ID
	Reason             Reason // 主原因：本次决策的定性解释
	SpilloverReason    Reason // 溢出子原因；非溢出决策为空
	Policy             Policy // 带版本的算法标识
}

// Validate 检查策略返回的复合决策。
//
// Backend 是跨策略共享的核心值对象，必须在策略边界确认其完整性；把该判断放在
// Decision 上，可以让 requestpath 只调用一次复合参数校验，并保留对自定义策略的保护。
func (d Decision) Validate() error {
	if err := d.Backend.Validate(); err != nil {
		return fmt.Errorf("routing decision backend: %w", err)
	}
	return nil
}

// Strategy 同步地从一张快照中选出一个后端。
//
// Select 必须具有确定性：相同 routingKey 与相同 Snapshot 应产生可复现的决策。
// 有状态策略（session-key）的差异仅来自其记忆表，且记忆表不影响该保证。
type Strategy interface {
	Name() Mode
	Select(routingKey string, snapshot Snapshot) (Decision, error)
}

// BackendReconciler 由持有成员范围状态的策略实现（目前只有 session-key）。
// discovery 在成员变化时调用它，让策略清理指向已下线后端的记忆。
type BackendReconciler interface {
	ReconcileBackends([]backend.Backend)
}

// NewConfigured 创建显式配置的策略。所有非无状态策略都必须由组合根提供完整配置。
func NewConfigured(config Config) (Strategy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	switch config.Mode {
	case ModeKVAware:
		return newKVAware(config.KVAware), nil
	case ModeSessionKey:
		return newSessionKey(config.SessionKey), nil
	case "", ModeLoadBalanced:
		return NewLoadBalanced(), nil
	default:
		return nil, fmt.Errorf("unsupported routing mode %q", config.Mode)
	}
}

// EligibleBackends 返回不含被生命周期或熔断挡掉条目的后端副本。
// 所有策略在比较前都应先经此过滤，保证被挡后端绝不参与决策。
func EligibleBackends(snapshot Snapshot) []backend.Backend {
	result := make([]backend.Backend, 0, len(snapshot.Backends))
	for _, candidate := range snapshot.Backends {
		if _, blocked := snapshot.Ineligible[candidate.ID]; !blocked {
			result = append(result, candidate)
		}
	}
	return result
}
