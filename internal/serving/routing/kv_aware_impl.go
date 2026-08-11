package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

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

func (s kvAwareStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
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

	// 线性扫描取成本最小者。cost 为负数不可能出现（饱和运算保证），
	// 因此普通 < 比较即等价于"严格更优"。
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if kvAwareCostLess(candidate, selected, snapshot, s.config) {
			selected = candidate
		}
	}
	// 平局消解放在比较之外：只有成本完全相同的后端才用 session hint，
	// 保证 session-key 永远不会覆盖真实的 cache/load 差异。
	selected = kvAwareTieBreak(routingKey, selected, candidates, snapshot, s.config)
	return Decision{
		Backend: selected, PreferredBackendID: selected.ID,
		Reason: ReasonKVAware, Policy: PolicyKVAwareV1,
	}, nil
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

// kvAwareCostLess 比较等价未缓存 token 成本。unknown external load 只是不贡献 queue/running 项，
// 不是零负载声明；KV unknown 已在策略入口前由 KVAwareInput.UsableFor 拒绝。
func kvAwareCostLess(candidate, current backend.Backend, snapshot Snapshot, config KVAwareConfig) bool {
	return kvAwareCost(candidate, snapshot, config) < kvAwareCost(current, snapshot, config)
}

// kvAwareTieBreak 只在 cache 和全部 load 维度相同的 backend 间使用 session hint，避免 session-key
// 覆盖真实 cache/load。排序消除 discovery 返回顺序对确定性的影响。
func kvAwareTieBreak(routingKey string, selected backend.Backend, candidates []backend.Backend, snapshot Snapshot, config KVAwareConfig) backend.Backend {
	// 收集与 selected 成本完全相同的所有候选。
	tied := []backend.Backend{selected}
	for _, candidate := range candidates {
		if candidate.ID != selected.ID && kvAwareCost(candidate, snapshot, config) == kvAwareCost(selected, snapshot, config) {
			tied = append(tied, candidate)
		}
	}
	// 没有平局就原样返回，避免无谓的哈希与排序。
	if len(tied) == 1 {
		return selected
	}
	// 先按 ID 排序，再用 routingKey 哈希取下标：
	// 无论 discovery 返回什么顺序，同一 key + 同一成本集都得到同一结果。
	sort.Slice(tied, func(i, j int) bool { return tied[i].ID < tied[j].ID })
	hash := sha256.Sum256([]byte(routingKey))
	return tied[int(binary.BigEndian.Uint32(hash[:4])%uint32(len(tied)))]
}

// kvAwareCost 计算单个后端的等价 token 成本：
//
//	未缓存 token 数
//	+ 排队请求数 × QueueTokenPenalty   （尚未开始 prefill，最难受）
//	+ 运行中请求数 × RunningTokenPenalty
//	+ 本地在途请求数 × InflightTokenPenalty
//
// 外部负载项只在 Load.Valid 时贡献，未知不是零；本地 in-flight 恒可信。
func kvAwareCost(candidate backend.Backend, snapshot Snapshot, config KVAwareConfig) int64 {
	match := snapshot.KVAware.Matches[candidate.ID]
	cost := int64(snapshot.KVAware.PromptTokens - match.MatchedTokens)
	load := snapshot.Loads[candidate.ID]
	if load.Valid {
		cost = saturatingAdd(cost, saturatingMultiply(load.QueueDepth, config.QueueTokenPenalty))
		cost = saturatingAdd(cost, saturatingMultiply(load.Running, config.RunningTokenPenalty))
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
