// Package circuit owns bounded per-backend transport outcome state.
package circuit

import (
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// Outcome is a completed upstream transport result. Client cancellation and
// HTTP response status are classified before this boundary.
type Outcome string

// Config controls the transport-error EWMA and temporary open interval.
type Config struct {
	EWMAAlpha       float64
	ErrorThreshold  float64
	MinimumRequests int
	OpenDuration    time.Duration
	Clock           func() time.Time
}

// Breaker stores and reconciles circuit state for active backends.
type Breaker interface {
	Record(backend.ID, Outcome) bool
	IsOpen(backend.ID) bool
	Reconcile([]backend.Backend) []backend.ID
	Remove(backend.ID)
	Len() int
}

// DefaultConfig returns the production circuit defaults.
func DefaultConfig() Config {
	return Config{
		EWMAAlpha:       0.5,
		ErrorThreshold:  0.6,
		MinimumRequests: 3,
		OpenDuration:    10 * time.Second,
		Clock:           time.Now,
	}
}
