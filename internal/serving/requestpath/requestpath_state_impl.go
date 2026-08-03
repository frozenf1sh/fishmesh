package requestpath

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/circuit"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

type leaseState struct {
	service   *service
	backendID backend.ID
	counter   *atomic.Int64

	once   sync.Once
	result Completion
}

func (s *service) reconcileLoop(ctx context.Context) {
	defer close(s.done)
	s.reconcileFromResolver(ctx)
	ticker := time.NewTicker(s.config.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileFromResolver(ctx)
		}
	}
}

func (s *service) reconcileFromResolver(ctx context.Context) {
	backends, err := s.resolver.Snapshot(ctx)
	if err == nil {
		s.reconcileBackends(backends)
	}
}

func (s *service) reconcileBackends(backends []backend.Backend) {
	// 1. 先把策略和 circuit 对齐到同一份 discovery membership。
	if reconciler, ok := s.strategy.(routing.BackendReconciler); ok {
		reconciler.ReconcileBackends(backends)
	}
	s.circuits.Reconcile(backends)

	// 2. 仅回收已经离开 membership 且没有在途请求的本地计数器。
	active := make(map[backend.ID]struct{}, len(backends)+1)
	active[s.config.Service.ID] = struct{}{}
	for _, candidate := range backends {
		active[candidate.ID] = struct{}{}
	}
	var removed []backend.ID
	s.mu.Lock()
	s.active = active
	for id, counter := range s.counters {
		if _, ok := active[id]; ok || counter.Load() != 0 {
			continue
		}
		delete(s.counters, id)
		removed = append(removed, id)
	}
	s.mu.Unlock()

	// 3. 锁外通知 transport/metrics，避免跨 domain 回调形成锁顺序。
	s.notifyRemoved(removed)
}

func (s *service) complete(backendID backend.ID, counter *atomic.Int64, outcome Outcome) Completion {
	completion := Completion{}
	if backendID != s.config.Service.ID {
		switch outcome {
		case OutcomeResponseCompleted:
			completion.CircuitOpened = s.circuits.Record(backendID, circuit.OutcomeSuccess)
		case OutcomeTransportFailure, OutcomeDeadlineExceeded, OutcomeUpstreamStream:
			completion.CircuitOpened = s.circuits.Record(backendID, circuit.OutcomeFailure)
		}
		completion.CircuitOpen = s.circuits.IsOpen(backendID)
	}
	completion.BackendRemoved = s.releaseBackend(backendID, counter)
	return completion
}

func (s *service) inflightCounter(backendID backend.ID) *atomic.Int64 {
	s.mu.RLock()
	counter := s.counters[backendID]
	s.mu.RUnlock()
	if counter != nil {
		return counter
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if counter = s.counters[backendID]; counter == nil {
		counter = &atomic.Int64{}
		s.counters[backendID] = counter
	}
	return counter
}

func (s *service) releaseBackend(backendID backend.ID, counter *atomic.Int64) bool {
	if counter.Add(-1) != 0 {
		return false
	}
	s.mu.Lock()
	_, active := s.active[backendID]
	if active || s.counters[backendID] != counter {
		s.mu.Unlock()
		return false
	}
	delete(s.counters, backendID)
	s.mu.Unlock()

	s.circuits.Remove(backendID)
	s.notifyRemoved([]backend.ID{backendID})
	return true
}

func (s *service) notifyRemoved(backends []backend.ID) {
	if s.onRemove == nil {
		return
	}
	for _, backendID := range backends {
		s.onRemove(backendID)
	}
}
