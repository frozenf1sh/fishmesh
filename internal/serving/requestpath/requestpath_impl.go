package requestpath

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/kvcache"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
)

var _ Path = (*service)(nil)

type service struct {
	config       Config
	resolver     discovery.Resolver
	observations observation.Reader
	strategy     routing.Strategy
	circuits     circuit.Breaker
	tokenizer    tokenization.Tokenizer
	kvCache      kvcache.Index
	kvReconcile  func(context.Context, []backend.Backend) error
	onRemove     func(backend.ID)
	predictor    prediction.Tracker
	cancel       context.CancelFunc
	done         chan struct{}
	close        sync.Once

	mu       sync.RWMutex // 保护 active 与 counters 的成员关系，不保护 atomic 计数值。
	active   map[backend.ID]struct{}
	counters map[backend.ID]*atomic.Int64
}

// New 校验依赖、初始化成员状态，并启动可关闭的定期 reconcile。
func New(config Config, dependencies Dependencies) (Path, error) {
	if err := config.Service.Validate(); err != nil {
		return nil, fmt.Errorf("requestpath service fallback: %w", err)
	}
	if dependencies.Resolver == nil || dependencies.Strategy == nil || dependencies.Circuits == nil {
		return nil, fmt.Errorf("requestpath resolver, strategy and circuits must not be nil")
	}
	if dependencies.Strategy.Name() == routing.ModeExactCacheLoad && (dependencies.Tokenizer == nil || dependencies.KVCache == nil) {
		return nil, fmt.Errorf("exact-cache-load requestpath requires tokenizer and KV cache")
	}
	if dependencies.Strategy.Name() == routing.ModeExactCacheLoad && dependencies.KVReconcile == nil {
		return nil, fmt.Errorf("exact-cache-load requestpath requires KV reconcile")
	}
	if config.RequireFreshDiscovery && config.DiscoveryMaxAge <= 0 {
		return nil, fmt.Errorf("requestpath discovery max age must be positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &service{
		config: config, resolver: dependencies.Resolver, observations: dependencies.Observations,
		strategy: dependencies.Strategy, circuits: dependencies.Circuits, tokenizer: dependencies.Tokenizer, kvCache: dependencies.KVCache, kvReconcile: dependencies.KVReconcile, onRemove: dependencies.OnBackendRemoved, predictor: dependencies.Predictor,
		cancel: cancel, done: make(chan struct{}), active: map[backend.ID]struct{}{config.Service.ID: {}},
		counters: map[backend.ID]*atomic.Int64{config.Service.ID: {}},
	}
	if config.ReconcileInterval > 0 {
		go service.reconcileLoop(ctx)
	} else {
		close(service.done)
	}
	return service, nil
}

func (s *service) Select(ctx context.Context, request Request) (Lease, error) {
	// 1. 读取 discovery，并先让所有成员范围状态看到同一份 backend 列表。
	backends, err := s.resolver.Snapshot(ctx)
	if err == nil {
		s.reconcileBackends(ctx, backends)
	} else {
		backends = nil
	}

	// 2. 合并观测、local in-flight 和 circuit，固定本次选择的健康候选。
	state, snapshot := s.buildSnapshot(backends)

	// 3. exact 模式使用真实 Token IDs 和 KV Match 构建纯 routing 输入；未知信号只降级，不伪装为零命中。
	exact, exactStatus, err := s.buildExactInput(ctx, tokenization.Input{Route: tokenization.Route(request.Route), Body: request.Body}, routing.EligibleBackends(snapshot))
	if err != nil {
		return Lease{}, err
	}
	state.Exact, snapshot.Exact = exactStatus, exact

	// 4. 先由纯策略选择，再为最终 backend 登记可幂等 lease。
	decision := s.selectDecision(request.RoutingKey, snapshot, state.Discovery)
	if exactStatus == ExactAvailable {
		state.CachedPrefixTokens = exact.Matches[decision.Backend.ID].MatchedTokens
	}

	// 所有退出路径都由 Complete 幂等释放。
	var ticket prediction.Ticket
	if s.predictor != nil && exactStatus == ExactAvailable && decision.Policy == routing.PolicyExactCacheLoadV2 {
		input := predictionInput(snapshot, exact, decision.Backend.ID)
		ticket, state.Prediction = s.predictor.Begin(input)
	}
	counter := s.inflightCounter(decision.Backend.ID)
	counter.Add(1)
	return Lease{
		Decision: decision, State: state,
		state: &leaseState{service: s, backendID: decision.Backend.ID, counter: counter, ticket: ticket},
	}, nil
}

func (s *service) State(ctx context.Context) State {
	backends, err := s.resolver.Snapshot(ctx)
	if err != nil {
		state := State{Discovery: s.resolver.Status(), CircuitOpen: map[backend.ID]bool{}}
		if s.observations != nil {
			// metrics scrape 遇到瞬时 discovery 错误时保留最后观测；真正的
			// 请求选择仍使用空 membership 并走显式 Service fallback。
			state.Observations = s.observations.Snapshot()
		}
		return state
	}
	s.reconcileBackends(ctx, backends)
	state, _ := s.buildSnapshot(backends)
	return state
}

func (s *service) Ready() bool {
	status := s.resolver.Status()
	if status.Status == discovery.StatusUnavailable {
		return false
	}
	return !s.config.RequireFreshDiscovery || status.Freshness <= s.config.DiscoveryMaxAge
}

func (s *service) Close() error {
	s.close.Do(func() {
		s.cancel()
		<-s.done
	})
	return nil
}

func (s *service) buildSnapshot(backends []backend.Backend) (State, routing.Snapshot) {
	state := State{
		Backends: append([]backend.Backend(nil), backends...), Observations: s.activeObservations(backends),
		CircuitOpen: make(map[backend.ID]bool, len(backends)), Discovery: s.resolver.Status(), Exact: ExactNotRequested,
	}
	if s.kvCache != nil {
		state.KVCache = projectKVCacheState(s.kvCache.State())
	}
	snapshot := routing.Snapshot{
		Backends: state.Backends, Inflight: s.inflightSnapshot(), Observations: state.Observations,
		Ineligible: make(map[backend.ID]routing.Reason), Loads: routingLoads(backends, state.Observations),
	}
	for _, candidate := range backends {
		open := s.circuits.IsOpen(candidate.ID)
		state.CircuitOpen[candidate.ID] = open
		if open {
			snapshot.Ineligible[candidate.ID] = routing.ReasonCircuitOpen
		}
	}
	return state, snapshot
}

func projectKVCacheState(snapshot kvcache.StateSnapshot) map[backend.ID]KVCacheState {
	instances := snapshot.Instances()
	if len(instances) == 0 {
		return nil
	}
	states := make(map[backend.ID]KVCacheState, len(instances))
	for backendID, instance := range instances {
		states[backendID] = KVCacheState{
			Valid: instance.Valid, Reason: instance.Reason, Freshness: instance.Freshness,
			LastSequence: instance.LastSequence, AppliedBatches: instance.AppliedBatches, ReplayBatches: instance.ReplayBatches,
		}
	}
	return states
}

func (s *service) buildExactInput(ctx context.Context, input tokenization.Input, candidates []backend.Backend) (routing.ExactInput, ExactStatus, error) {
	if s.strategy.Name() != routing.ModeExactCacheLoad {
		return routing.ExactInput{}, ExactNotRequested, nil
	}
	profile, err := s.tokenizer.Tokenize(ctx, input)
	if err != nil {
		if isContextError(err) {
			return routing.ExactInput{}, ExactTokenizationFailed, err
		}
		return routing.ExactInput{}, ExactTokenizationFailed, nil
	}
	if err := s.kvReconcile(ctx, candidates); err != nil {
		if isContextError(err) {
			return routing.ExactInput{}, ExactLookupFailed, err
		}
		return routing.ExactInput{}, ExactLookupFailed, nil
	}

	query := kvcache.Query{Model: profile.Model(), CacheSalt: profile.CacheSalt(), Backends: backendIDs(candidates)}
	for _, prompt := range profile.Prompts() {
		query.TokenGroups = append(query.TokenGroups, prompt.TokenIDs())
	}
	snapshot, err := s.kvCache.Lookup(ctx, query)
	if err != nil {
		if isContextError(err) {
			return routing.ExactInput{}, ExactLookupFailed, err
		}
		return routing.ExactInput{}, ExactLookupFailed, nil
	}
	exact := routingInputFromMatches(profile.TotalTokens(), snapshot.Matches())
	if !exact.UsableFor(candidates) {
		return exact, ExactMatchUnavailable, nil
	}
	return exact, ExactAvailable, nil
}

func (s *service) selectDecision(routingKey string, snapshot routing.Snapshot, status discovery.ResolverStatus) routing.Decision {
	if !s.directRoutingEligible(status) {
		return s.fallback(routing.ReasonDiscoveryFallback)
	}
	if len(routing.EligibleBackends(snapshot)) == 0 {
		return s.fallback(routing.ReasonCircuitFallback)
	}
	if s.strategy.Name() == routing.ModeExactCacheLoad && !snapshot.Exact.UsableFor(routing.EligibleBackends(snapshot)) {
		return s.exactLoadFallback(routingKey, snapshot)
	}
	decision, err := s.strategy.Select(routingKey, snapshot)
	if err != nil || decision.Backend.ID == "" {
		return s.fallback(routing.ReasonStrategyFallback)
	}
	if err := decision.Backend.Validate(); err != nil {
		return s.fallback(routing.ReasonBackendFallback)
	}
	return decision
}

func (s *service) exactLoadFallback(routingKey string, snapshot routing.Snapshot) routing.Decision {
	decision, err := routing.NewLoadAware().Select(routingKey, snapshot)
	if err != nil || decision.Backend.ID == "" {
		return s.fallback(routing.ReasonStrategyFallback)
	}
	decision.Reason = routing.ReasonExactSignalUnavailable
	decision.Policy = routing.PolicyExactLoadFallbackV1
	return decision
}

func (s *service) directRoutingEligible(status discovery.ResolverStatus) bool {
	if !s.config.RequireFreshDiscovery {
		return true
	}
	return status.Status != discovery.StatusUnavailable && status.ReadyBackends > 0 && status.Freshness <= s.config.DiscoveryMaxAge
}

func (s *service) fallback(reason routing.Reason) routing.Decision {
	return routing.Decision{
		Backend: s.config.Service, PreferredBackendID: s.config.Service.ID,
		Reason: reason, Policy: routing.PolicyServiceFallbackV1,
	}
}

func (s *service) activeObservations(backends []backend.Backend) map[backend.ID]observation.Backend {
	if s.observations == nil {
		return nil
	}
	active := make(map[backend.ID]struct{}, len(backends))
	for _, candidate := range backends {
		active[candidate.ID] = struct{}{}
	}
	result := make(map[backend.ID]observation.Backend, len(active))
	for id, state := range s.observations.Snapshot() {
		if _, ok := active[id]; ok {
			result[id] = state
		}
	}
	return result
}

func routingLoads(backends []backend.Backend, observations map[backend.ID]observation.Backend) map[backend.ID]routing.Load {
	loads := make(map[backend.ID]routing.Load, len(backends))
	for _, candidate := range backends {
		observation, ok := observations[candidate.ID]
		if !ok {
			continue
		}
		queue, queueOK := nonNegativeCount(observation.QueueLength)
		running, runningOK := nonNegativeCount(observation.RunningRequests)
		if queueOK && runningOK {
			loads[candidate.ID] = routing.Load{QueueDepth: queue, Running: running, Valid: true}
		}
	}
	return loads
}

// predictionInput 在 orchestration 层把 routing 的纯快照投影为预测域的数值值对象。
// hard-overload backend 沿用 exact 的安全排除；unknown load 保持 LoadValid=false。
func predictionInput(snapshot routing.Snapshot, exact routing.ExactInput, selected backend.ID) prediction.BeginInput {
	candidates := make([]prediction.Candidate, 0, len(snapshot.Backends))
	for _, candidate := range routing.EligibleBackends(snapshot) {
		load := snapshot.Loads[candidate.ID]
		if load.HardOverload {
			continue
		}
		match := exact.Matches[candidate.ID]
		features := prediction.Features{
			UncachedTokens: int64(exact.PromptTokens - match.MatchedTokens), QueueDepth: load.QueueDepth,
			Running: load.Running, LocalInflight: snapshot.Inflight[candidate.ID], LoadValid: load.Valid,
		}
		candidates = append(candidates, prediction.Candidate{Backend: candidate.ID, Features: features})
	}
	for _, candidate := range candidates {
		if candidate.Backend == selected {
			return prediction.BeginInput{Selected: selected, Features: candidate.Features, Candidates: candidates}
		}
	}
	return prediction.BeginInput{}
}

func nonNegativeCount(sample observation.Sample[float64]) (int64, bool) {
	if !sample.Valid || sample.Value < 0 || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) || sample.Value > float64(math.MaxInt64) {
		return 0, false
	}
	// Prometheus 样本是 float；向上取整避免把分数/瞬时估算值截断成更低的压力。
	return int64(math.Ceil(sample.Value)), true
}

func routingInputFromMatches(promptTokens int, matches map[backend.ID]kvcache.Match) routing.ExactInput {
	result := routing.ExactInput{PromptTokens: promptTokens, Matches: make(map[backend.ID]routing.CacheMatch, len(matches))}
	for backendID, match := range matches {
		result.Matches[backendID] = routing.CacheMatch{Valid: match.Valid, MatchedTokens: match.MatchedTokens}
	}
	return result
}

func backendIDs(backends []backend.Backend) []backend.ID {
	ids := make([]backend.ID, len(backends))
	for index, candidate := range backends {
		ids[index] = candidate.ID
	}
	return ids
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *service) inflightSnapshot() map[backend.ID]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[backend.ID]int64, len(s.counters))
	for id, counter := range s.counters {
		result[id] = counter.Load()
	}
	return result
}
