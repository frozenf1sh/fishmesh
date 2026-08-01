// Package observation owns the slow, read-only telemetry loop used by the
// Serving context. It converts infrastructure data into routing snapshots;
// policies remain synchronous and never perform network I/O themselves.
package observation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/endpoint"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

const (
	defaultInterval = 15 * time.Second
	defaultMaxAge   = 45 * time.Second
)

// Clock is injectable to make freshness behavior deterministic in tests.
type Clock func() time.Time

// Collector reads telemetry for one resolved backend. A collector must return
// a signal even when collection fails; failures are part of the snapshot and
// must never be silently converted to healthy zeroes.
type Collector interface {
	Collect(context.Context, routing.Backend) routing.BackendObservation
}

// IdentityProvider enriches a backend batch in one API call. Batch semantics
// avoid issuing one Kubernetes request per backend and make partial identity
// failures visible in every affected observation.
type IdentityProvider interface {
	Enrich(context.Context, []routing.Backend) (map[string]routing.BackendIdentity, error)
}

// Config controls the background snapshot service.
type Config struct {
	Resolver       endpoint.Resolver
	Collector      Collector
	Identity       IdentityProvider
	Interval       time.Duration
	MaxAge         time.Duration
	RequestTimeout time.Duration
	Clock          Clock
}

// Service maintains the latest observation for the current EndpointSlice
// snapshot. The resolver remains the source of backend identity; this service
// only enriches those identities with telemetry.
type Service struct {
	resolver       endpoint.Resolver
	collector      Collector
	identity       IdentityProvider
	interval       time.Duration
	maxAge         time.Duration
	requestTimeout time.Duration
	clock          Clock
	cancel         context.CancelFunc
	done           chan struct{}

	mu     sync.RWMutex
	states map[string]routing.BackendObservation
	close  sync.Once
}

func New(config Config) (*Service, error) {
	if config.Resolver == nil {
		return nil, fmt.Errorf("observation resolver must not be nil")
	}
	if config.Collector == nil {
		return nil, fmt.Errorf("observation collector must not be nil")
	}
	if config.Interval <= 0 {
		config.Interval = defaultInterval
	}
	if config.MaxAge <= 0 {
		config.MaxAge = defaultMaxAge
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 5 * time.Second
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		resolver: config.Resolver, collector: config.Collector, identity: config.Identity, interval: config.Interval,
		maxAge: config.MaxAge, requestTimeout: config.RequestTimeout, clock: config.Clock,
		cancel: cancel, done: make(chan struct{}), states: make(map[string]routing.BackendObservation),
	}
	go service.loop(ctx)
	return service, nil
}

// Snapshot returns a copy and computes freshness at read time. A signal older
// than MaxAge is reported as degraded unless it is already unavailable.
func (s *Service) Snapshot() map[string]routing.BackendObservation {
	now := s.clock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]routing.BackendObservation, len(s.states))
	for id, state := range s.states {
		state.Freshness = now.Sub(state.ObservedAt)
		if state.ObservedAt.IsZero() {
			state.Freshness = 0
		}
		if state.Status == routing.ObservationOK && state.Freshness > s.maxAge {
			state.Status = routing.ObservationDegraded
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

func (s *Service) Close() error {
	s.close.Do(func() {
		s.cancel()
		<-s.done
	})
	return nil
}

func (s *Service) loop(ctx context.Context) {
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

func (s *Service) refresh(ctx context.Context) {
	backends, err := s.resolver.Snapshot(ctx)
	if err != nil {
		return
	}
	next := make(map[string]routing.BackendObservation, len(backends))
	identities := map[string]routing.BackendIdentity{}
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
			if identity, ok := identities[backend.ID]; ok {
				state.Identity = identity
				if identity.Status == routing.ObservationUnavailable && state.Status == routing.ObservationOK {
					state.Status = routing.ObservationDegraded
				}
				if identity.Error != "" {
					state.Error = joinErrors(state.Error, identity.Error)
				}
			} else if identityErr != "" {
				state.Status = routing.ObservationDegraded
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

func normalizeSample[T any](sample routing.Sample[T], fallbackTime time.Time, fallbackSource string) routing.Sample[T] {
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

func freshSample[T any](sample routing.Sample[T], now time.Time, maxAge time.Duration) routing.Sample[T] {
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
