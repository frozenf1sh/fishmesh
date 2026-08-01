package routing

import (
	"fmt"
	"sync"
	"time"
)

// CircuitConfig controls a small per-backend transport-error circuit. It uses
// an exponentially weighted moving average (EWMA): recent failures matter
// more than old ones, while a minimum sample count prevents one transient
// connection error from ejecting an endpoint.
type CircuitConfig struct {
	EWMAAlpha       float64
	ErrorThreshold  float64
	MinimumRequests int
	OpenDuration    time.Duration
	Clock           func() time.Time
}

func DefaultCircuitConfig() CircuitConfig {
	return CircuitConfig{
		EWMAAlpha:       0.5,
		ErrorThreshold:  0.6,
		MinimumRequests: 3,
		OpenDuration:    10 * time.Second,
		Clock:           time.Now,
	}
}

type circuitState struct {
	errorEWMA float64
	samples   int
	openUntil time.Time
}

// CircuitRegistry owns bounded local outcome state. Membership reconciliation
// removes state for deleted endpoints; no request key is stored here.
type CircuitRegistry struct {
	config CircuitConfig
	mu     sync.Mutex
	states map[string]circuitState
}

func NewCircuitRegistry(config CircuitConfig) (*CircuitRegistry, error) {
	if config.EWMAAlpha <= 0 || config.EWMAAlpha > 1 {
		return nil, fmt.Errorf("circuit EWMA alpha must be in (0, 1]")
	}
	if config.ErrorThreshold <= 0 || config.ErrorThreshold > 1 {
		return nil, fmt.Errorf("circuit error threshold must be in (0, 1]")
	}
	if config.MinimumRequests <= 0 || config.OpenDuration <= 0 {
		return nil, fmt.Errorf("circuit minimum requests and open duration must be positive")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &CircuitRegistry{config: config, states: make(map[string]circuitState)}, nil
}

// Record adds one completed transport outcome and reports whether this call
// opened the circuit. HTTP 4xx/5xx and downstream cancellation are not
// transport outcomes and must not be passed here as failures.
func (r *CircuitRegistry) Record(backendID string, failed bool) bool {
	if backendID == "" {
		return false
	}
	now := r.config.Clock()
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.states[backendID]
	if !state.openUntil.IsZero() && !now.Before(state.openUntil) {
		state = circuitState{}
	}
	sample := 0.0
	if failed {
		sample = 1
	}
	if state.samples == 0 {
		state.errorEWMA = sample
	} else {
		state.errorEWMA = r.config.EWMAAlpha*sample + (1-r.config.EWMAAlpha)*state.errorEWMA
	}
	state.samples++
	opened := false
	if failed && state.samples >= r.config.MinimumRequests && state.errorEWMA >= r.config.ErrorThreshold {
		state.openUntil = now.Add(r.config.OpenDuration)
		opened = true
	}
	r.states[backendID] = state
	return opened
}

func (r *CircuitRegistry) IsOpen(backendID string) bool {
	now := r.config.Clock()
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[backendID]
	if !ok || state.openUntil.IsZero() {
		return false
	}
	if now.Before(state.openUntil) {
		return true
	}
	// A short open interval is followed by a clean probe window. Successes keep
	// it closed; repeated failures can open it again after MinimumRequests.
	r.states[backendID] = circuitState{}
	return false
}

// Reconcile removes circuit state for endpoints no longer present in the
// discovery snapshot and returns their IDs for observability cleanup.
func (r *CircuitRegistry) Reconcile(backends []Backend) []string {
	active := make(map[string]struct{}, len(backends))
	for _, backend := range backends {
		active[backend.ID] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed []string
	for id := range r.states {
		if _, ok := active[id]; ok {
			continue
		}
		delete(r.states, id)
		removed = append(removed, id)
	}
	return removed
}

func (r *CircuitRegistry) Remove(backendID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, backendID)
}

func (r *CircuitRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}
