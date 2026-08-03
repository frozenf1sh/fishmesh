package requestpath

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/discovery"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

var _ Path = (*service)(nil)

type service struct {
	config       Config
	resolver     discovery.Resolver
	observations observation.Reader
	strategy     routing.Strategy
	circuits     circuit.Breaker
	onRemove     func(backend.ID)
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
	if config.RequireFreshDiscovery && config.DiscoveryMaxAge <= 0 {
		return nil, fmt.Errorf("requestpath discovery max age must be positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &service{
		config: config, resolver: dependencies.Resolver, observations: dependencies.Observations,
		strategy: dependencies.Strategy, circuits: dependencies.Circuits, onRemove: dependencies.OnBackendRemoved,
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
		s.reconcileBackends(backends)
	} else {
		backends = nil
	}

	// 2. 合并观测、local in-flight 和 circuit，执行显式 fallback 选择。
	state, snapshot := s.buildSnapshot(backends)
	decision := s.selectDecision(request.RoutingKey, snapshot, state.Discovery)

	// 3. 为最终 backend 登记 lease；所有退出路径都由 Complete 幂等释放。
	counter := s.inflightCounter(decision.Backend.ID)
	counter.Add(1)
	return Lease{
		Decision: decision, State: state,
		state: &leaseState{service: s, backendID: decision.Backend.ID, counter: counter},
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
	s.reconcileBackends(backends)
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
		CircuitOpen: make(map[backend.ID]bool, len(backends)), Discovery: s.resolver.Status(),
	}
	snapshot := routing.Snapshot{
		Backends: state.Backends, Inflight: s.inflightSnapshot(), Observations: state.Observations,
		Ineligible: make(map[backend.ID]routing.Reason),
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

func (s *service) selectDecision(routingKey string, snapshot routing.Snapshot, status discovery.ResolverStatus) routing.Decision {
	if !s.directRoutingEligible(status) {
		return s.fallback(routing.ReasonDiscoveryFallback)
	}
	if len(routing.EligibleBackends(snapshot)) == 0 {
		return s.fallback(routing.ReasonCircuitFallback)
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

func (s *service) inflightSnapshot() map[backend.ID]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[backend.ID]int64, len(s.counters))
	for id, counter := range s.counters {
		result[id] = counter.Load()
	}
	return result
}
