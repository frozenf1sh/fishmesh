package requestpath

import (
	"context"
	"errors"
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
	config          Config
	resolver        discovery.Resolver
	observations    observation.Reader
	strategy        routing.Strategy
	circuits        circuit.Breaker
	tokenizer       tokenization.Tokenizer
	kvCache         kvcache.Index
	kvReconcile     func(context.Context, []backend.Backend) error
	onRemove        func(backend.ID)
	predictor       prediction.Tracker
	staticEstimator *prediction.StaticEstimator
	cancel          context.CancelFunc
	done            chan struct{}
	close           sync.Once
	selection       sync.Mutex // 将最终快照、选择与 lease reservation 作为一个短临界区提交。

	mu       sync.RWMutex // 保护 active 与 counters 的成员关系，不保护 atomic 计数值。
	active   map[backend.ID]struct{}
	counters map[backend.ID]*atomic.Int64
}

// New 校验依赖、初始化成员状态，并启动可关闭的定期 reconcile。
func New(config Config, dependencies Dependencies) (Path, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := dependencies.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &service{
		config: config, resolver: dependencies.Resolver, observations: dependencies.Observations,
		strategy: dependencies.Strategy, circuits: dependencies.Circuits, tokenizer: dependencies.Tokenizer, kvCache: dependencies.KVCache, kvReconcile: dependencies.KVReconcile, onRemove: dependencies.OnBackendRemoved, predictor: dependencies.Predictor, staticEstimator: dependencies.StaticEstimator,
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
	_, snapshot := s.buildSnapshot(backends)

	// 3. KV-aware 模式使用真实 Token IDs 和 KV Match 构建纯 routing 输入；未知信号只降级，不伪装为零命中。
	kvInput, kvStatus, err := s.buildKVAwareInput(ctx, tokenization.Input{Route: tokenization.Route(request.Route), Body: request.Body}, routing.EligibleBackends(snapshot))
	if err != nil {
		return Lease{}, err
	}
	// Tokenize/KV reconcile 可并发执行；KV lookup 依赖 Token IDs，必须在两者完成后执行。只有最终候选快照、决策和 counter reservation 串行提交，
	// 防止同一批请求全部读取相同 local in-flight 后形成 thundering herd。
	s.selection.Lock()
	state, snapshot := s.buildSnapshot(backends)
	state.KV, snapshot.KVAware = kvStatus, kvInput
	if kvStatus == KVAvailable && s.staticEstimator != nil {
		snapshot.Estimates = staticLatencyEstimates(snapshot, kvInput, s.staticEstimator)
	}

	// 4. 先由纯策略选择，再为最终 backend 登记可幂等 lease。
	decision := s.selectDecision(request.RoutingKey, snapshot, state.Discovery, kvStatus)
	if kvStatus == KVAvailable {
		state.CachedPrefixTokens = kvInput.Matches[decision.Backend.ID].MatchedTokens
		state.Estimate = selectedEstimateEvidence(snapshot, state.Observations, kvInput, decision.Backend.ID)
	}

	// 所有退出路径都由 Complete 幂等释放。
	var ticket prediction.Ticket
	if s.predictor != nil && kvStatus == KVAvailable && (decision.Policy == routing.PolicyKVAwareV1 || decision.Policy == routing.PolicyKVAwareStaticV1) {
		input := predictionInput(snapshot, kvInput, decision.Backend.ID)
		ticket, state.Prediction = s.predictor.Begin(input)
	}
	counter := s.inflightCounter(decision.Backend.ID)
	counter.Add(1)
	s.selection.Unlock()
	return Lease{
		Decision: decision, State: state,
		state: &leaseState{service: s, backendID: decision.Backend.ID, counter: counter, ticket: ticket},
	}, nil
}

func selectedEstimateEvidence(snapshot routing.Snapshot, observations map[backend.ID]observation.Backend, kvInput routing.KVAwareInput, selected backend.ID) EstimateEvidence {
	match := kvInput.Matches[selected]
	load := snapshot.Loads[selected]
	estimate := snapshot.Estimates[selected]
	evidence := EstimateEvidence{
		PromptTokens: kvInput.PromptTokens, CachedPrefixTokens: match.MatchedTokens,
		UncachedTokens: kvInput.PromptTokens - match.MatchedTokens,
		EstimatedTTFT:  estimate.TTFT, Valid: estimate.Valid, Confidence: estimate.Confidence,
		Version: estimate.Version, Reason: estimate.Reason, LoadValid: load.Valid,
		QueueDepth: load.QueueDepth, Running: load.Running, LocalDelta: load.LocalDelta, LocalInflight: snapshot.Inflight[selected],
	}
	if observed, ok := observations[selected]; ok {
		evidence.LoadSampleAge = observed.Freshness
	}
	for _, candidate := range routing.EligibleBackends(snapshot) {
		if snapshot.Loads[candidate.ID].HardOverload {
			evidence.HardOverloadedCandidates++
		}
	}
	return evidence
}

func staticLatencyEstimates(snapshot routing.Snapshot, kvInput routing.KVAwareInput, estimator *prediction.StaticEstimator) map[backend.ID]routing.LatencyEstimate {
	estimates := make(map[backend.ID]routing.LatencyEstimate, len(snapshot.Backends))
	identity := estimator.Identity()
	for _, candidate := range routing.EligibleBackends(snapshot) {
		match := kvInput.Matches[candidate.ID]
		load := snapshot.Loads[candidate.ID]
		estimate := estimator.Estimate(prediction.StaticInput{
			Identity: identity,
			Prompt: prediction.PromptWork{
				PromptTokens: int64(kvInput.PromptTokens), CachedPrefixTokens: int64(match.MatchedTokens),
			},
			Load: prediction.LoadWork{
				QueueDepth: load.QueueDepth, Running: load.Running, LocalDelta: load.LocalDelta,
				LocalInflight: snapshot.Inflight[candidate.ID], Valid: load.Valid,
			},
		})
		estimates[candidate.ID] = routing.LatencyEstimate{
			TTFT: estimate.TTFT, Valid: estimate.Valid, Confidence: routing.EstimateConfidence(estimate.Confidence),
			Version: estimate.Version, Reason: string(estimate.Reason),
		}
	}
	return estimates
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
		CircuitOpen: make(map[backend.ID]bool, len(backends)), Discovery: s.resolver.Status(), KV: KVNotRequested,
	}
	if s.kvCache != nil {
		state.KVCache = projectKVCacheState(s.kvCache.State())
	}
	inflight := s.inflightSnapshot()
	snapshot := routing.Snapshot{
		Backends: state.Backends, Inflight: inflight, Observations: state.Observations,
		Ineligible: make(map[backend.ID]routing.Reason), Loads: routingLoads(backends, state.Observations, inflight, s.config),
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

func (s *service) buildKVAwareInput(ctx context.Context, input tokenization.Input, candidates []backend.Backend) (routing.KVAwareInput, KVStatus, error) {
	if s.strategy.Name() != routing.ModeKVAware {
		return routing.KVAwareInput{}, KVNotRequested, nil
	}
	type tokenizationResult struct {
		profile tokenization.Result
		err     error
	}
	parallelContext, cancel := context.WithCancel(ctx)
	defer cancel()
	profileResults := make(chan tokenizationResult, 1)
	reconcileResults := make(chan error, 1)
	go func() {
		profile, err := s.tokenizer.Tokenize(parallelContext, input)
		profileResults <- tokenizationResult{profile: profile, err: err}
	}()
	go func() {
		reconcileResults <- s.kvReconcile(parallelContext, candidates)
	}()
	profileResult := <-profileResults
	reconcileErr := <-reconcileResults
	if profileResult.err != nil {
		if requestContextCanceled(ctx, profileResult.err) || !canDegradeTokenization(profileResult.err) {
			return routing.KVAwareInput{}, KVTokenizationFailed, profileResult.err
		}
		return routing.KVAwareInput{}, KVTokenizationFailed, nil
	}
	if reconcileErr != nil {
		if requestContextCanceled(ctx, reconcileErr) {
			return routing.KVAwareInput{}, KVLookupFailed, reconcileErr
		}
		return routing.KVAwareInput{}, KVLookupFailed, nil
	}
	promptTokens := profileResult.profile.TotalTokens()
	if s.config.ShortPromptTokens > 0 && promptTokens <= s.config.ShortPromptTokens {
		// Tokenization still runs because the exact token count is the safe threshold
		// input. KV reconcile remains concurrent so Pod membership and replay state do
		// not become stale, but the request skips the per-request KV lookup and uses
		// the same load-aware fallback as an unavailable KV signal.
		return routing.KVAwareInput{PromptTokens: promptTokens}, KVShortContextBypassed, nil
	}

	query := kvcache.Query{Model: profileResult.profile.Model(), CacheSalt: profileResult.profile.CacheSalt(), Backends: backendIDs(candidates)}
	for _, prompt := range profileResult.profile.Prompts() {
		query.TokenGroups = append(query.TokenGroups, prompt.TokenIDs())
	}
	snapshot, err := s.kvCache.Lookup(ctx, query)
	if err != nil {
		if requestContextCanceled(ctx, err) {
			return routing.KVAwareInput{}, KVLookupFailed, err
		}
		return routing.KVAwareInput{}, KVLookupFailed, nil
	}
	kvInput := routingInputFromMatches(profileResult.profile.TotalTokens(), snapshot.Matches())
	if !kvInput.UsableFor(candidates) {
		return kvInput, KVMatchUnavailable, nil
	}
	return kvInput, KVAvailable, nil
}

func (s *service) selectDecision(routingKey string, snapshot routing.Snapshot, status discovery.ResolverStatus, kvStatus KVStatus) routing.Decision {
	if !s.directRoutingEligible(status) {
		return s.fallback(routing.ReasonDiscoveryFallback)
	}
	if len(routing.EligibleBackends(snapshot)) == 0 {
		return s.fallback(routing.ReasonCircuitFallback)
	}
	if s.strategy.Name() == routing.ModeKVAware {
		if kvStatus == KVShortContextBypassed {
			return s.kvAwareLoadFallback(routingKey, snapshot, routing.ReasonKVAwareShortContextFallback, true)
		}
		if !snapshot.KVAware.UsableFor(routing.EligibleBackends(snapshot)) {
			return s.kvAwareLoadFallback(routingKey, snapshot, routing.ReasonKVAwareSignalUnavailable, false)
		}
	}
	decision, err := s.strategy.Select(routingKey, snapshot)
	if err != nil || decision.Backend.ID == "" {
		return s.fallback(routing.ReasonStrategyFallback)
	}
	if err := decision.Validate(); err != nil {
		return s.fallback(routing.ReasonBackendFallback)
	}
	return decision
}

func (s *service) kvAwareLoadFallback(routingKey string, snapshot routing.Snapshot, reason routing.Reason, shortContext bool) routing.Decision {
	// KV signal 只决定是否能使用 locality；fallback 仍消费同一份 load-aware 普通策略，
	// 因而 Render/KV index 暂时不可用时不会丢弃仍然新鲜的 vLLM queue/running 事实；如果
	// 外部观测也不完整，load-aware 自己返回 local load-balanced policy，保留完整 provenance。
	decision, err := routing.NewLoadAware().Select(routingKey, snapshot)
	if err != nil || decision.Backend.ID == "" {
		return s.fallback(routing.ReasonStrategyFallback)
	}
	decision.Reason = reason
	decision.Policy = kvAwareFallbackPolicy(decision.Policy, shortContext)
	return decision
}

func kvAwareFallbackPolicy(underlying routing.Policy, shortContext bool) routing.Policy {
	if underlying == routing.PolicyLoadAwareV1 {
		if shortContext {
			return routing.PolicyKVAwareShortContextLoadAwareV1
		}
		return routing.PolicyKVAwareLoadAwareFallbackV1
	}
	if shortContext {
		return routing.PolicyKVAwareShortContextLoadBalancedV2
	}
	return routing.PolicyKVAwareLoadBalancedFallbackV2
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

func routingLoads(backends []backend.Backend, observations map[backend.ID]observation.Backend, inflight map[backend.ID]int64, config Config) map[backend.ID]routing.Load {
	loads := make(map[backend.ID]routing.Load, len(backends))
	for _, candidate := range backends {
		state := routing.Load{}
		observed, ok := observations[candidate.ID]
		queue, queueOK := nonNegativeCount(observed.QueueLength)
		running, runningOK := nonNegativeCount(observed.RunningRequests)
		if queueOK && runningOK {
			state.QueueDepth = queue
			state.Running = running
			state.Valid = true
			state.LocalDelta = inflight[candidate.ID] - running
			if state.LocalDelta < 0 {
				state.LocalDelta = 0
			}
		}
		state.RuntimeHardOverload = runtimeHardOverloaded(observed.Runtime, config)
		state.HardOverload = hardOverloaded(state, inflight[candidate.ID], config)
		if ok || state.HardOverload {
			loads[candidate.ID] = state
		}
	}
	return loads
}

func hardOverloaded(load routing.Load, localInflight int64, config Config) bool {
	queueOverloaded := load.Valid && config.HardQueueDepth > 0 && load.QueueDepth >= config.HardQueueDepth
	localOverloaded := config.HardLocalInflight > 0 && localInflight >= config.HardLocalInflight
	return queueOverloaded || localOverloaded || load.RuntimeHardOverload
}

func runtimeHardOverloaded(runtime observation.Runtime, config Config) bool {
	return sampleAtOrAbove(runtime.CPUUsageCores, config.RuntimeCPUHardLimitCores) ||
		sampleAtOrAbove(runtime.MemoryUsageBytes, config.RuntimeMemoryHardLimitBytes) ||
		sampleAtOrAbove(runtime.GPUUtilizationPercent, config.RuntimeGPUUtilizationHardLimitPct) ||
		sampleAtOrAbove(runtime.GPUMemoryUsedBytes, config.RuntimeGPUMemoryHardLimitBytes) ||
		sampleAtOrAbove(runtime.GPUTemperatureCelsius, config.RuntimeGPUTemperatureHardLimitC)
}

func sampleAtOrAbove(sample observation.Sample[float64], limit float64) bool {
	return limit > 0 && sample.Valid && !math.IsNaN(sample.Value) && !math.IsInf(sample.Value, 0) && sample.Value >= limit
}

// predictionInput 在 orchestration 层把 routing 的纯快照投影为预测域的数值值对象。
// hard-overload backend 沿用 KV-aware 的安全排除；unknown load 保持 LoadValid=false。
func predictionInput(snapshot routing.Snapshot, kvInput routing.KVAwareInput, selected backend.ID) prediction.BeginInput {
	candidates := make([]prediction.Candidate, 0, len(snapshot.Backends))
	for _, candidate := range routing.EligibleBackends(snapshot) {
		load := snapshot.Loads[candidate.ID]
		if load.HardOverload {
			continue
		}
		match := kvInput.Matches[candidate.ID]
		features := prediction.Features{
			UncachedTokens: int64(kvInput.PromptTokens - match.MatchedTokens), QueueDepth: load.QueueDepth,
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

func routingInputFromMatches(promptTokens int, matches map[backend.ID]kvcache.Match) routing.KVAwareInput {
	result := routing.KVAwareInput{PromptTokens: promptTokens, Matches: make(map[backend.ID]routing.KVMatch, len(matches))}
	for backendID, match := range matches {
		result.Matches[backendID] = routing.KVMatch{Valid: match.Valid, MatchedTokens: match.MatchedTokens}
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

func requestContextCanceled(ctx context.Context, err error) bool {
	// Render adapter 内部产生的 deadline 是可以降级的上游故障。只有调用方已经
	// 取消，或明确返回 context.Canceled，才必须在创建 backend lease 前终止请求。
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func canDegradeTokenization(err error) bool {
	var typed *tokenization.Error
	if errors.As(err, &typed) {
		return typed.IsTransient()
	}
	// 没有 typed code 的 adapter 错误保持历史上的可降级行为；调用方取消已经
	// 在上一个判断中单独保护。
	return !errors.Is(err, context.Canceled)
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
