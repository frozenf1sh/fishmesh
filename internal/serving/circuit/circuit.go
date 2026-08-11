// Package circuit owns bounded per-backend transport outcome state.
package circuit

import (
	"fmt"
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

// Validate 检查 circuit 的固定阈值和时间边界。Clock 允许为空，由构造函数补入标准时钟。
func (c Config) Validate() error {
	if c.EWMAAlpha <= 0 || c.EWMAAlpha > 1 {
		return fmt.Errorf("circuit EWMA alpha must be in (0, 1]")
	}
	if c.ErrorThreshold <= 0 || c.ErrorThreshold > 1 {
		return fmt.Errorf("circuit error threshold must be in (0, 1]")
	}
	if c.MinimumRequests <= 0 || c.OpenDuration <= 0 {
		return fmt.Errorf("circuit minimum requests and open duration must be positive")
	}
	return nil
}

// Breaker stores and reconciles circuit state for active backends.
type Breaker interface {
	Record(backend.ID, Outcome) bool
	IsOpen(backend.ID) bool
	Reconcile([]backend.Backend) []backend.ID
	Remove(backend.ID)
	Len() int
}
