package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// 编译期断言：sessionKeyStrategy 同时实现 Strategy 与 BackendReconciler。
// 后者意味着该策略持有成员范围的状态（记忆表），discovery 变更时会通知它清理。
var (
	_ Strategy          = (*sessionKeyStrategy)(nil)
	_ BackendReconciler = (*sessionKeyStrategy)(nil)
)

// sessionKeyEntry 是记忆表中的一条记录：某个 key 当前绑定的后端及过期时间。
type sessionKeyEntry struct {
	backendID backend.ID
	expiresAt time.Time
}

// sessionKeyStrategy 是"带约束的粘性"策略：
// 每个 key 记住一个偏好后端以保住 KV 缓存局部性，但允许在熔断或
// 压力过大时临时溢出（spillover）到其他后端；溢出不重写记忆，
// 压力消退后请求自动回到偏好后端。
type sessionKeyStrategy struct {
	config  SessionKeyConfig
	mu      sync.Mutex                   // 保护 entries 的并发读写
	entries map[[32]byte]sessionKeyEntry // key 的 sha256 哈希 -> 绑定记录
}

// NewSessionKey 校验配置后创建策略；对外返回 Strategy，测试可断言回具体类型。
func NewSessionKey(config SessionKeyConfig) (Strategy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return newSessionKey(config), nil
}

// newSessionKey 是无 error 的内部构造：配置已在上游校验过。
// 组合根与 New 共用此入口，避免重复填充默认值。
func newSessionKey(config SessionKeyConfig) Strategy {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &sessionKeyStrategy{config: config, entries: make(map[[32]byte]sessionKeyEntry)}
}

func (*sessionKeyStrategy) Name() Mode { return ModeSessionKey }

func (s *sessionKeyStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	eligible := EligibleBackends(snapshot)
	if len(eligible) == 0 {
		return Decision{}, fmt.Errorf("session-key requires at least one backend")
	}

	// 无 key 的请求（调用方没有可用路由意图）不建立任何记忆，
	// 直接选全局最少负载者，避免污染记忆表。
	least := leastLoaded(snapshot, "")
	if routingKey == "" {
		return Decision{
			Backend: least, PreferredBackendID: least.ID,
			Reason: ReasonMissingSessionKeyLoadBalanced, Policy: PolicySessionKeyV1,
		}, nil
	}

	keyHash := sha256.Sum256([]byte(routingKey))
	// 先查记忆表；未命中（首次见到或已过期）就用 rendezvous 哈希
	// 选一个初始后端并记住它，保证成员变化时重映射最少。
	preferred, hit := s.preferred(keyHash, snapshot.Backends)
	if !hit {
		preferred = rendezvousBackend(keyHash, snapshot.Backends)
		s.remember(keyHash, preferred.ID)
	}

	// 在偏好后端上套用约束：熔断或压力超限则临时溢出。
	selected, spilloverReason := selectWithinSessionKeyBounds(preferred, snapshot, s.config)
	reason := ReasonSessionKeyHit
	if !hit {
		reason = ReasonSessionKeyMiss
	}
	if spilloverReason != "" {
		reason = ReasonSessionKeySpillover
	}
	// 活跃 key 使用滑动过期。溢出不会重写偏好后端，
	// 因此压力消退后局部性会自动恢复。
	s.remember(keyHash, preferred.ID)
	return Decision{
		Backend: selected, PreferredBackendID: preferred.ID, Reason: reason,
		SpilloverReason: spilloverReason, Policy: PolicySessionKeyV1,
	}, nil
}

// ReconcileBackends 在 discovery 成员变化时清掉指向已下线后端的记忆，
// 防止后续请求命中一个不再存在的偏好。
func (s *sessionKeyStrategy) ReconcileBackends(backends []backend.Backend) {
	active := make(map[backend.ID]struct{}, len(backends))
	for _, backend := range backends {
		active[backend.ID] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if _, ok := active[entry.backendID]; !ok {
			delete(s.entries, key)
		}
	}
}

// preferred 查询 key 的绑定：过期或绑定的后端已不在列表中时，
// 删除记录并返回未命中，让调用方重新执行 rendezvous。
func (s *sessionKeyStrategy) preferred(key [32]byte, backends []backend.Backend) (backend.Backend, bool) {
	now := s.config.Clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		delete(s.entries, key)
		return backend.Backend{}, false
	}
	for _, backend := range backends {
		if backend.ID == entry.backendID {
			return backend, true
		}
	}
	delete(s.entries, key)
	return backend.Backend{}, false
}

// remember 写入（或滑动刷新）key 的绑定。容量满时先清过期条目，
// 仍满则驱逐最老的一条，保证记忆表不会无限膨胀。
func (s *sessionKeyStrategy) remember(key [32]byte, backendID backend.ID) {
	now := s.config.Clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.config.MaxEntries {
		s.collectOrEvictOldest(now)
	}
	s.entries[key] = sessionKeyEntry{backendID: backendID, expiresAt: now.Add(s.config.TTL)}
}

// collectOrEvictOldest 先删除全部已过期条目；若容量仍满，
// 再删除过期时间最早的一个，为新 key 腾出位置。
func (s *sessionKeyStrategy) collectOrEvictOldest(now time.Time) {
	var oldestKey [32]byte
	var oldestExpiry time.Time
	foundOldest := false
	for key, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, key)
			continue
		}
		if !foundOldest || entry.expiresAt.Before(oldestExpiry) {
			oldestKey, oldestExpiry, foundOldest = key, entry.expiresAt, true
		}
	}
	if len(s.entries) >= s.config.MaxEntries && foundOldest {
		delete(s.entries, oldestKey)
	}
}

// selectWithinSessionKeyBounds 决定实际选中的后端：优先偏好后端，
// 只有在它被熔断、或负载明显劣于全局最闲者时才溢出。
// 返回值中的 Reason 是溢出子原因；留在偏好后端时为空。
func selectWithinSessionKeyBounds(preferred backend.Backend, snapshot Snapshot, config SessionKeyConfig) (backend.Backend, Reason) {
	// tie-break 偏向 preferred：当它与全局最闲者负载相同时优先胜出，
	// 使无压力的正常路径稳定留在偏好后端。
	least := leastLoaded(snapshot, preferred.ID)
	// 熔断/生命周期挡掉的偏好后端没有商量余地，溢出原因即挡掉它的原因。
	if reason, blocked := snapshot.Ineligible[preferred.ID]; blocked {
		if reason == "" {
			reason = ReasonIneligible
		}
		return least, reason
	}
	// 偏好后端本身就是全局最闲者，无需比较。
	if least.ID == preferred.ID {
		return preferred, ""
	}
	// 队列比较只在所有合格后端都有有效观测时进行：
	// 部分/过期数据不能被当作可信信号参与决策。
	if queueAvailableForAll(snapshot) {
		preferredQueue := snapshot.Observations[preferred.ID].QueueLength.Value
		leastQueue := snapshot.Observations[least.ID].QueueLength.Value
		if preferredQueue > leastQueue+config.QueueDepthDelta {
			return least, ReasonQueueDepth
		}
	}
	// 本地 in-flight 比较。本地计数总是可信的（由本 Gateway 自己维护）。
	if snapshot.Inflight[preferred.ID] > snapshot.Inflight[least.ID]+config.InflightDelta {
		return least, ReasonLocalInflight
	}
	return preferred, ""
}

// leastLoaded 在合格后端中选出负载最小者。tieBreakerID 非空时，
// 该后端在平局中胜出（用于把偏好后端留在正常路径上）。
func leastLoaded(snapshot Snapshot, tieBreakerID backend.ID) backend.Backend {
	backends := EligibleBackends(snapshot)
	best := backends[0]
	useQueue := queueAvailableForAll(snapshot)
	for _, candidate := range backends[1:] {
		if lessLoaded(candidate, best, snapshot, tieBreakerID, useQueue) {
			best = candidate
		}
	}
	return best
}

// lessLoaded 按词典顺序比较两个后端：先比外部队列（仅当全员观测可用），
// 再比本地 in-flight，最后用 tie-breaker 与 ID 字典序保证确定性。
func lessLoaded(candidate, current backend.Backend, snapshot Snapshot, tieBreakerID backend.ID, useQueue bool) bool {
	if useQueue {
		candidateQueue := snapshot.Observations[candidate.ID].QueueLength.Value
		currentQueue := snapshot.Observations[current.ID].QueueLength.Value
		if candidateQueue != currentQueue {
			return candidateQueue < currentQueue
		}
	}
	candidateInflight := snapshot.Inflight[candidate.ID]
	currentInflight := snapshot.Inflight[current.ID]
	if candidateInflight != currentInflight {
		return candidateInflight < currentInflight
	}
	// 平局消解：tie-breaker 优先，其次按 ID 字典序。
	// 排序后的 ID 比较消除了 discovery 返回顺序对结果的影响。
	if candidate.ID == tieBreakerID {
		return true
	}
	if current.ID == tieBreakerID {
		return false
	}
	return candidate.ID < current.ID
}

// queueAvailableForAll 检查是否每个合格后端都有有效队列观测。
// 只要缺一个，队列维度就整体失效，调用方只能退而依赖本地 in-flight。
func queueAvailableForAll(snapshot Snapshot) bool {
	if len(snapshot.Observations) == 0 {
		return false
	}
	for _, backend := range EligibleBackends(snapshot) {
		observation, ok := snapshot.Observations[backend.ID]
		if !ok || !observation.QueueLength.Valid {
			return false
		}
	}
	return true
}

// rendezvousBackend 用 rendezvous（最高随机权重）哈希为 key 挑选初始后端。
// 相比简单取模，它的优势是 EndpointSlice 成员变化时重映射的 key 最少。
func rendezvousBackend(key [32]byte, backends []backend.Backend) backend.Backend {
	best := backends[0]
	bestScore := rendezvousScore(key, best.ID)
	for _, candidate := range backends[1:] {
		score := rendezvousScore(key, candidate.ID)
		if score > bestScore || (score == bestScore && candidate.ID < best.ID) {
			best, bestScore = candidate, score
		}
	}
	return best
}

// rendezvousScore 计算 key 对某个后端的稳定伪随机权重：
// sha256(key || 0x00 || backendID) 的前 8 字节大端整数。
// 分隔字节 0x00 防止 "ab"+"c" 与 "a"+"bc" 之类的拼接歧义。
func rendezvousScore(key [32]byte, backendID backend.ID) uint64 {
	hash := sha256.New()
	_, _ = hash.Write(key[:])
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(backendID))
	return binary.BigEndian.Uint64(hash.Sum(nil)[:8])
}
