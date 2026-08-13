package routing

import (
	"fmt"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// maximumKVAwareCost 是 int64 的最大值（2^63-1），用作饱和运算的上界。
// 一旦某次乘/加会溢出，结果就钉死在这个值，防止翻转为负数
// （负数成本会让排序逻辑把最坏后端误判成最优）。
const maximumKVAwareCost = int64(^uint64(0) >> 1)

// 编译期断言：kvAwareStrategy 必须实现 Strategy 接口。
var _ Strategy = kvAwareStrategy{}

// kvAwareStrategy 是成本式 KV-aware 策略：把"KV 未命中 token 数"
// 与"各类已知负载"折算成同一量纲（等价 token 成本），选择总成本最低的后端。
type kvAwareStrategy struct {
	config KVAwareConfig
}

// NewConfiguredKVAware 构造成本式 KV-aware 策略。配置只描述同量纲的压力换算，不读取外部状态。
func NewConfiguredKVAware(config KVAwareConfig) (Strategy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return newKVAware(config), nil
}

// newKVAware 是无 error 的内部构造：配置已在上游校验过。
func newKVAware(config KVAwareConfig) Strategy {
	return kvAwareStrategy{config: config}
}

func (kvAwareStrategy) Name() Mode {
	return ModeKVAware
}

func (s kvAwareStrategy) Select(_ string, snapshot Snapshot) (Decision, error) {
	eligible := EligibleBackends(snapshot)
	if len(eligible) == 0 {
		return Decision{}, fmt.Errorf("kv-aware routing requires at least one backend")
	}
	// 门槛检查：任一候选缺失有效 KV match 时直接报错，
	// 由调用方（requestpath）显式降级到 load-balanced，
	// 而不是让本策略用猜测的缺失数据做决定。
	if !snapshot.KVAware.UsableFor(eligible) {
		return Decision{}, fmt.Errorf("kv-aware routing requires valid KV-aware matches for every eligible backend")
	}

	// 硬过载的后端直接出局，再在剩余候选里做成本比较。
	candidates := withoutHardOverload(eligible, snapshot.Loads)
	if len(candidates) == 0 {
		// 当所有健康 backend 都超过硬阈值时，保持可用性优先：cache locality 被否决，
		// 使用既有 load-balanced 顺序挑选一个 backend。requestpath 仍会记录 typed reason。
		selected := leastLoaded(snapshot, "")
		return Decision{
			Backend: selected, PreferredBackendID: selected.ID,
			Reason: ReasonKVAwareHardOverloadFallback, Policy: PolicyKVAwareV1,
		}, nil
	}
	if s.config.EstimatorMode == KVAwareEstimatorStatic && staticEstimatesUsable(candidates, snapshot.Estimates) {
		selected := candidates[0]
		for _, candidate := range candidates[1:] {
			if staticEstimateLess(candidate, selected, snapshot.Estimates) {
				selected = candidate
			}
		}
		return Decision{
			Backend: selected, PreferredBackendID: selected.ID,
			Reason: ReasonKVAwareStatic, Policy: PolicyKVAwareStaticV1,
		}, nil
	}

	// 线性扫描取成本最小者；词典序比较（成本 → ID）保证平局确定性，
	// 不依赖 discovery 返回顺序。cost 为负数不可能出现（饱和运算保证）。
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if kvAwareLess(candidate, selected, snapshot, s.config) {
			selected = candidate
		}
	}
	reason := ReasonKVAware
	if s.config.EstimatorMode == KVAwareEstimatorStatic {
		reason = ReasonKVAwareStaticFallback
	}
	return Decision{
		Backend: selected, PreferredBackendID: selected.ID,
		Reason: reason, Policy: PolicyKVAwareV1,
	}, nil
}

func staticEstimatesUsable(backends []backend.Backend, estimates map[backend.ID]LatencyEstimate) bool {
	for _, candidate := range backends {
		estimate, ok := estimates[candidate.ID]
		if !ok || !estimate.Valid || estimate.TTFT <= 0 || estimate.Version == "" || estimate.Confidence == EstimateConfidenceUncalibrated {
			return false
		}
	}
	return len(backends) > 0
}

func staticEstimateLess(candidate, current backend.Backend, estimates map[backend.ID]LatencyEstimate) bool {
	candidateTTFT := estimates[candidate.ID].TTFT
	currentTTFT := estimates[current.ID].TTFT
	if candidateTTFT != currentTTFT {
		return candidateTTFT < currentTTFT
	}
	return candidate.ID < current.ID
}

// withoutHardOverload 过滤掉已越过硬过载阈值的后端。
// 硬过载意味着后端已无法安全承接新请求，即便它的 KV 命中再好也必须出局。
func withoutHardOverload(backends []backend.Backend, loads map[backend.ID]Load) []backend.Backend {
	result := make([]backend.Backend, 0, len(backends))
	for _, candidate := range backends {
		if !loads[candidate.ID].HardOverload {
			result = append(result, candidate)
		}
	}
	return result
}

// kvAwareLess 按词典序比较两个候选：先比等价未缓存 token 成本，成本相同再比 ID。
// ID 作为确定性平局消解，不依赖 discovery 返回顺序。unknown external load 只是不贡献
// queue/running 项，不是零负载声明；KV unknown 已在策略入口前由 KVAwareInput.UsableFor 拒绝。
func kvAwareLess(candidate, current backend.Backend, snapshot Snapshot, config KVAwareConfig) bool {
	candidateCost := kvAwareCost(candidate, snapshot, config)
	currentCost := kvAwareCost(current, snapshot, config)
	if candidateCost != currentCost {
		return candidateCost < currentCost
	}
	return candidate.ID < current.ID
}

// kvAwareCost 计算单个后端的等价 token 成本：
//
//	未缓存 token 数
//	+ 排队请求数 × QueueTokenPenalty   （尚未开始 prefill，最难受）
//	+ 运行中请求数 × RunningTokenPenalty
//	+ 本地新增在途数 × InflightTokenPenalty（外部负载有效时只补采样滞后）
//	+ 本地在途请求数 × InflightTokenPenalty（外部负载未知时）
//
// queue/running 新鲜且完整时，它们已经包含 vLLM 接收的工作，不再叠加完整
// local in-flight；外部负载未知时才用 local in-flight 作为单 Gateway fallback。
func kvAwareCost(candidate backend.Backend, snapshot Snapshot, config KVAwareConfig) int64 {
	match := snapshot.KVAware.Matches[candidate.ID]
	cost := int64(snapshot.KVAware.PromptTokens - match.MatchedTokens)
	load := snapshot.Loads[candidate.ID]
	if load.Valid {
		cost = saturatingAdd(cost, saturatingMultiply(load.QueueDepth, config.QueueTokenPenalty))
		cost = saturatingAdd(cost, saturatingMultiply(load.Running, config.RunningTokenPenalty))
		cost = saturatingAdd(cost, saturatingMultiply(load.LocalDelta, config.InflightTokenPenalty))
		return cost
	}
	return saturatingAdd(cost, saturatingMultiply(snapshot.Inflight[candidate.ID], config.InflightTokenPenalty))
}

// saturatingMultiply 做饱和乘法：count 或 penalty 非正时返回 0
// （零 penalty 是受控实验中"关闭该维度"的合法手段），
// 溢出时返回 maximumKVAwareCost 而不是回绕。
func saturatingMultiply(count, penalty int64) int64 {
	if count <= 0 || penalty <= 0 {
		return 0
	}
	if count > maximumKVAwareCost/penalty {
		return maximumKVAwareCost
	}
	return count * penalty
}

// saturatingAdd 做饱和加法：结果超过 int64 上限时钉死在上限，
// 保证成本函数单调且永不出现负值。
func saturatingAdd(left, right int64) int64 {
	if left >= maximumKVAwareCost-right {
		return maximumKVAwareCost
	}
	return left + right
}
