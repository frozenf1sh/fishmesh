package circuit

import (
	"fmt"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

var _ Breaker = (*breaker)(nil)

type circuitState struct {
	errorEWMA float64
	samples   int
	openUntil time.Time
}

type breaker struct {
	config Config

	mu     sync.Mutex
	states map[backend.ID]circuitState
}

// New validates config and creates an empty circuit breaker.
func New(config Config) (Breaker, error) {
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
	return &breaker{config: config, states: make(map[backend.ID]circuitState)}, nil
}

func (b *breaker) Record(backendID backend.ID, outcome Outcome) bool {
	if backendID == "" || (outcome != OutcomeSuccess && outcome != OutcomeFailure) {
		return false
	}
	now := b.config.Clock()
	b.mu.Lock()
	defer b.mu.Unlock()

	state := b.states[backendID]
	if !state.openUntil.IsZero() && !now.Before(state.openUntil) {
		state = circuitState{}
	}
	sample := 0.0
	if outcome == OutcomeFailure {
		sample = 1
	}
	if state.samples == 0 {
		state.errorEWMA = sample
	} else {
		state.errorEWMA = b.config.EWMAAlpha*sample + (1-b.config.EWMAAlpha)*state.errorEWMA
	}
	state.samples++
	opened := false
	if outcome == OutcomeFailure && state.samples >= b.config.MinimumRequests && state.errorEWMA >= b.config.ErrorThreshold {
		state.openUntil = now.Add(b.config.OpenDuration)
		opened = true
	}
	b.states[backendID] = state
	return opened
}

func (b *breaker) IsOpen(backendID backend.ID) bool {
	now := b.config.Clock()
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[backendID]
	if !ok || state.openUntil.IsZero() {
		return false
	}
	if now.Before(state.openUntil) {
		return true
	}
	b.states[backendID] = circuitState{}
	return false
}

func (b *breaker) Reconcile(backends []backend.Backend) []backend.ID {
	active := make(map[backend.ID]struct{}, len(backends))
	for _, candidate := range backends {
		active[candidate.ID] = struct{}{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	var removed []backend.ID
	for id := range b.states {
		if _, ok := active[id]; ok {
			continue
		}
		delete(b.states, id)
		removed = append(removed, id)
	}
	return removed
}

func (b *breaker) Remove(backendID backend.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, backendID)
}

func (b *breaker) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.states)
}
