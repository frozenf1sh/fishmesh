package observation

import (
	"context"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
)

var _ Reader = (*service)(nil)

// service maintains the latest observation for the current EndpointSlice
// snapshot. The resolver remains the source of backend identity; this service
// only enriches those identities with telemetry.
type service struct {
	resolver       BackendSource
	collector      Collector
	identity       identity.Enricher
	interval       time.Duration
	maxAge         time.Duration
	requestTimeout time.Duration
	clock          Clock
	cancel         context.CancelFunc
	done           chan struct{}

	mu     sync.RWMutex
	states map[backend.ID]Backend
	close  sync.Once
}

func New(config Config, dependencies Dependencies) (Reader, error) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if err := dependencies.Validate(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &service{
		resolver: dependencies.Resolver, collector: dependencies.Collector, identity: dependencies.Identity, interval: config.Interval,
		maxAge: config.MaxAge, requestTimeout: config.RequestTimeout, clock: config.Clock,
		cancel: cancel, done: make(chan struct{}), states: make(map[backend.ID]Backend),
	}
	go service.loop(ctx)
	return service, nil
}

// Snapshot returns a copy and computes freshness at read time. A signal older
// than MaxAge is reported as degraded unless it is already unavailable.
func (s *service) Snapshot() map[backend.ID]Backend {
	now := s.clock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[backend.ID]Backend, len(s.states))
	for id, state := range s.states {
		state.Freshness = now.Sub(state.ObservedAt)
		if state.ObservedAt.IsZero() {
			state.Freshness = 0
		}
		if state.Status == StatusOK && state.Freshness > s.maxAge {
			state.Status = StatusDegraded
			if state.Error == "" {
				state.Error = "observation is stale"
			}
		}
		state.QueueLength = freshSample(state.QueueLength, now, s.maxAge)
		state.RunningRequests = freshSample(state.RunningRequests, now, s.maxAge)
		result[id] = state
	}
	return result
}

func (s *service) Close() error {
	s.close.Do(func() {
		s.cancel()
		<-s.done
	})
	return nil
}

func (s *service) loop(ctx context.Context) {
	defer close(s.done)
	s.refresh(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

func (s *service) refresh(ctx context.Context) {
	backends, err := s.resolver.Snapshot(ctx)
	if err != nil {
		return
	}
	next := make(map[backend.ID]Backend, len(backends))
	identities := map[backend.ID]identity.Identity{}
	identityErr := ""
	if s.identity != nil {
		identities, err = s.identity.Enrich(ctx, backends)
		if err != nil {
			identityErr = err.Error()
		}
	}
	var wait sync.WaitGroup
	var mu sync.Mutex
	for _, backend := range backends {
		backend := backend
		wait.Add(1)
		go func() {
			defer wait.Done()
			collectContext, cancel := context.WithTimeout(ctx, s.requestTimeout)
			state := s.collector.Collect(collectContext, backend)
			cancel()
			if identityState, ok := identities[backend.ID]; ok {
				state.Identity = identityState
				if identityState.Status == identity.StatusUnavailable && state.Status == StatusOK {
					state.Status = StatusDegraded
				}
				if identityState.Error != "" {
					state.Error = joinErrors(state.Error, identityState.Error)
				}
			} else if identityErr != "" {
				state.Status = StatusDegraded
				state.Error = joinErrors(state.Error, "identity: "+identityErr)
			}
			if state.ObservedAt.IsZero() {
				state.ObservedAt = s.clock()
			}
			state.QueueLength = normalizeSample(state.QueueLength, state.ObservedAt, state.Source)
			state.RunningRequests = normalizeSample(state.RunningRequests, state.ObservedAt, state.Source)
			mu.Lock()
			next[backend.ID] = state
			mu.Unlock()
		}()
	}
	wait.Wait()
	s.mu.Lock()
	s.states = next
	s.mu.Unlock()
}

func normalizeSample[T any](sample Sample[T], fallbackTime time.Time, fallbackSource string) Sample[T] {
	if !sample.Valid {
		return sample
	}
	if sample.ObservedAt.IsZero() {
		sample.ObservedAt = fallbackTime
	}
	if sample.Source == "" {
		sample.Source = fallbackSource
	}
	return sample
}

func freshSample[T any](sample Sample[T], now time.Time, maxAge time.Duration) Sample[T] {
	if !sample.Valid || sample.ObservedAt.IsZero() {
		return sample
	}
	if now.Sub(sample.ObservedAt) > maxAge {
		sample.Valid = false
		if sample.Error == "" {
			sample.Error = "sample is stale"
		}
	}
	return sample
}

func joinErrors(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "; " + second
}
