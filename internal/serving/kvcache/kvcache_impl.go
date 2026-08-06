package kvcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

var _ Index = (*service)(nil)

// service 组合有界 KV store 与逐 Pod 可靠事件流。
// reconcileMu 串行化 Pod 生命周期事务；mu 只保护 streams publication 和 closed。
type service struct {
	config Config
	store  cacheStore
	source EventSource
	clock  Clock
	ctx    context.Context
	cancel context.CancelFunc

	reconcileMu sync.Mutex
	mu          sync.RWMutex
	streams     map[backend.ID]*eventStream
	closed      bool
}

// NewVLLM 构造一个本地、进程内的 vLLM exact KV index。
// 构造函数不会发现 Pod；组合根必须通过 Reconcile 显式提供当前实例，并在退出时调用 Close。
func NewVLLM(ctx context.Context, config Config, dependencies Dependencies) (Index, error) {
	if ctx == nil {
		return nil, fmt.Errorf("kvcache context must not be nil")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if dependencies.EventSource == nil {
		return nil, fmt.Errorf("kvcache event source must not be nil")
	}
	store, err := newVLLMStore(config)
	if err != nil {
		return nil, err
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = defaultClock
	}
	return newService(ctx, config, dependencies.EventSource, clock, store), nil
}

func newService(ctx context.Context, config Config, source EventSource, clock Clock, store cacheStore) *service {
	serviceContext, cancel := context.WithCancel(ctx)
	return &service{
		config:  config,
		store:   store,
		source:  source,
		clock:   clock,
		ctx:     serviceContext,
		cancel:  cancel,
		streams: make(map[backend.ID]*eventStream),
	}
}

// Lookup 返回所有候选 backend 的 exact 状态；未知、过期或模型不一致的候选仍会出现在 Snapshot，
// 但 Valid=false，调用方必须据此降级。
func (s *service) Lookup(ctx context.Context, query Query) (Snapshot, error) {
	// 1. 在执行 block hash 前先保护 query 的 CPU 和内存边界。
	totalTokens, totalBlocks, err := s.validateQuery(query)
	if err != nil {
		return Snapshot{}, err
	}

	// 2. 固定本次 lookup 使用的有效实例，并为无效候选建立显式 Match。
	lookupAt := s.clock()
	selected, instances, matches, err := s.lookupInputs(query, totalTokens, totalBlocks)
	if err != nil {
		return Snapshot{}, err
	}

	// 3. 只查询 freshness 有效且模型一致的 Pod identifier。
	matchedBlocks := make(map[backend.ID]int)
	if len(instances) > 0 && totalBlocks > 0 {
		matchedBlocks, totalBlocks, err = s.store.Lookup(ctx, query, instances)
		if err != nil {
			return Snapshot{}, &Error{Code: CodeIndex, Err: err}
		}
	}

	// 4. 发布结果前再次检查 lifecycle/freshness，避免并发 Pod 替换后发布旧实例命中。
	for backendID, stream := range selected {
		state, current := s.currentState(backendID, stream)
		if !current || !state.Valid {
			matches[backendID] = invalidMatch(backendID, state, totalTokens, totalBlocks)
			continue
		}
		matches[backendID] = validMatch(backendID, state, matchedBlocks[backendID], totalTokens, totalBlocks, s.config.BlockSizeTokens)
	}
	return newSnapshot(lookupAt, totalTokens, totalBlocks, matches), nil
}

// State 返回逐实例 freshness/sequence 的不可变快照。
func (s *service) State() StateSnapshot {
	s.mu.RLock()
	streams := make(map[backend.ID]*eventStream, len(s.streams))
	for backendID, stream := range s.streams {
		streams[backendID] = stream
	}
	closed := s.closed
	s.mu.RUnlock()

	states := make(map[backend.ID]InstanceState, len(streams))
	for backendID, stream := range streams {
		states[backendID] = stream.Snapshot()
	}
	return StateSnapshot{observedAt: s.clock(), closed: closed, instances: states}
}

// Close 幂等停止并等待全部 subscriber，然后清理本地索引归属。
func (s *service) Close() error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	streams := make([]*eventStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()

	var closeErrors []error
	for _, stream := range streams {
		stream.Invalidate(ReasonClosed, "kvcache closed")
		stream.Close()
		if err := s.store.Clear(context.Background(), stream.Instance().PodIdentifier); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func (s *service) validateQuery(query Query) (int, int, error) {
	if err := query.Validate(); err != nil {
		return 0, 0, &Error{Code: CodeInvalidQuery, Err: err}
	}
	if len(query.CacheSalt) > s.config.MaxCacheSaltBytes {
		return 0, 0, &Error{Code: CodeCapacity, Err: fmt.Errorf("cache salt exceeds %d bytes", s.config.MaxCacheSaltBytes)}
	}
	totalTokens := 0
	totalBlocks := 0
	for _, tokens := range query.TokenGroups {
		totalTokens += len(tokens)
		if totalTokens > s.config.MaxQueryTokens {
			return 0, 0, &Error{Code: CodeCapacity, Err: fmt.Errorf("query exceeds %d tokens", s.config.MaxQueryTokens)}
		}
		totalBlocks += len(tokens) / s.config.BlockSizeTokens
	}
	return totalTokens, totalBlocks, nil
}

func (s *service) lookupInputs(
	query Query,
	totalTokens int,
	totalBlocks int,
) (map[backend.ID]*eventStream, map[backend.ID]Instance, map[backend.ID]Match, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, nil, nil, &Error{Code: CodeClosed, Err: errors.New("kvcache is closed")}
	}

	selected := make(map[backend.ID]*eventStream, len(query.Backends))
	instances := make(map[backend.ID]Instance, len(query.Backends))
	matches := make(map[backend.ID]Match, len(query.Backends))
	for _, backendID := range query.Backends {
		stream := s.streams[backendID]
		if stream == nil {
			matches[backendID] = Match{Backend: backendID, Reason: ReasonBackendUnknown, TotalTokens: totalTokens, TotalBlocks: totalBlocks}
			continue
		}
		state := stream.Snapshot()
		if state.Instance.Model != query.Model {
			state.Valid = false
			state.Reason = ReasonModelMismatch
		}
		if !state.Valid {
			matches[backendID] = invalidMatch(backendID, state, totalTokens, totalBlocks)
			continue
		}
		selected[backendID] = stream
		instances[backendID] = state.Instance
	}
	return selected, instances, matches, nil
}

func (s *service) currentState(backendID backend.ID, expected *eventStream) (InstanceState, bool) {
	s.mu.RLock()
	current := s.streams[backendID]
	s.mu.RUnlock()
	if current == nil || current != expected {
		return InstanceState{Reason: ReasonLifecycleChanging}, false
	}
	return current.Snapshot(), true
}

func newSnapshot(lookupAt time.Time, totalTokens, totalBlocks int, matches map[backend.ID]Match) Snapshot {
	return Snapshot{lookupAt: lookupAt, totalTokens: totalTokens, totalBlocks: totalBlocks, matches: matches}
}

func validMatch(backendID backend.ID, state InstanceState, blocks, totalTokens, totalBlocks, blockSize int) Match {
	return Match{
		Backend:       backendID,
		Valid:         true,
		MatchedBlocks: blocks,
		MatchedTokens: blocks * blockSize,
		TotalBlocks:   totalBlocks,
		TotalTokens:   totalTokens,
		ObservedAt:    state.LastReplayAt,
		Freshness:     state.Freshness,
	}
}

func invalidMatch(backendID backend.ID, state InstanceState, totalTokens, totalBlocks int) Match {
	return Match{
		Backend:     backendID,
		Reason:      state.Reason,
		TotalBlocks: totalBlocks,
		TotalTokens: totalTokens,
		ObservedAt:  state.LastReplayAt,
		Freshness:   state.Freshness,
	}
}

func defaultClock() time.Time { return time.Now() }
