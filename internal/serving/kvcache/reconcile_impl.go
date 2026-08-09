package kvcache

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// Reconcile 以 Pod UID 为事务边界对齐 subscriber 和 index ownership。
func (s *service) Reconcile(ctx context.Context, desired []Instance) error {
	normalized, err := s.validateInstances(desired)
	if err != nil {
		return err
	}

	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	if s.isClosed() {
		return &Error{Code: CodeClosed, Err: errors.New("kvcache is closed")}
	}

	current := s.streamSnapshot()
	next, additions, retired := s.reconcileChanges(normalized, current)

	// 旧流必须先停止，再清理它的 Pod identifier；否则迟到事件可能在 Clear 后重新写回旧 locality。
	for _, stream := range retired {
		stream.Invalidate(ReasonLifecycleChanging, "instance removed or replaced")
		stream.Close()
		if err := s.store.Clear(ctx, stream.Instance().PodIdentifier); err != nil {
			return &Error{Code: CodeLifecycle, Err: err}
		}
	}

	s.mu.Lock()
	s.streams = next
	s.mu.Unlock()
	for _, stream := range additions {
		stream.Start()
	}
	return nil
}

func (s *service) reconcileChanges(
	desired map[backend.ID]Instance,
	current map[backend.ID]*eventStream,
) (map[backend.ID]*eventStream, []*eventStream, []*eventStream) {
	next := make(map[backend.ID]*eventStream, len(desired))
	additions := make([]*eventStream, 0)
	retired := make([]*eventStream, 0)
	for backendID, instance := range desired {
		if existing := current[backendID]; existing != nil && existing.Same(instance) && !existing.Closed() {
			next[backendID] = existing
			continue
		}
		if existing := current[backendID]; existing != nil {
			retired = append(retired, existing)
		}
		stream := newEventStream(s.ctx, s.config, instance, s.source, s.store, s.clock, s.observer)
		next[backendID] = stream
		additions = append(additions, stream)
	}
	for backendID, existing := range current {
		if _, keep := desired[backendID]; !keep {
			retired = append(retired, existing)
		}
	}
	return next, additions, retired
}

func (s *service) validateInstances(desired []Instance) (map[backend.ID]Instance, error) {
	if len(desired) > s.config.MaxInstances {
		return nil, &Error{Code: CodeCapacity, Err: fmt.Errorf("instances exceed limit %d", s.config.MaxInstances)}
	}
	normalized := make(map[backend.ID]Instance, len(desired))
	uids := make(map[WorkloadUID]struct{}, len(desired))
	podIDs := make(map[string]struct{}, len(desired))
	for _, raw := range desired {
		instance := normalizeInstance(raw)
		if err := instance.Validate(); err != nil {
			return nil, &Error{Code: CodeLifecycle, Err: err}
		}
		if _, exists := normalized[instance.Backend]; exists {
			return nil, &Error{Code: CodeLifecycle, Err: fmt.Errorf("backend %q is duplicated", instance.Backend)}
		}
		if _, exists := uids[instance.PodUID]; exists {
			return nil, &Error{Code: CodeLifecycle, Err: fmt.Errorf("Pod UID %q is duplicated", instance.PodUID)}
		}
		if _, exists := podIDs[instance.PodIdentifier]; exists {
			return nil, &Error{Code: CodeLifecycle, Err: fmt.Errorf("Pod identifier %q is duplicated", instance.PodIdentifier)}
		}
		normalized[instance.Backend] = instance
		uids[instance.PodUID] = struct{}{}
		podIDs[instance.PodIdentifier] = struct{}{}
	}
	return normalized, nil
}

func (s *service) streamSnapshot() map[backend.ID]*eventStream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[backend.ID]*eventStream, len(s.streams))
	for backendID, stream := range s.streams {
		result[backendID] = stream
	}
	return result
}

func (s *service) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

func normalizeInstance(instance Instance) Instance {
	instance.PodIdentifier = strings.TrimSpace(instance.PodIdentifier)
	instance.Model = strings.TrimSpace(instance.Model)
	instance.EventsEndpoint = strings.TrimSpace(instance.EventsEndpoint)
	instance.ReplayEndpoint = strings.TrimSpace(instance.ReplayEndpoint)
	return instance
}
