// Package admission 负责进程级有界请求许可，并提供不排队的过载拒绝能力。
package admission

import (
	"errors"
	"fmt"
	"time"
)

var ErrCapacity = errors.New("admission capacity reached")
var ErrTarget = errors.New("admission target is outside hard limit")

// Config 设置进程允许同时进入推理请求路径的最大请求数。
type Config struct {
	MaxInflight   int
	InitialTarget int
}

// Validate 检查准入控制器的固定容量。容量一旦用于创建 channel 就不会改变，
// 因此该约束只应在初始化阶段执行。
func (c Config) Validate() error {
	if c.MaxInflight <= 0 {
		return fmt.Errorf("admission max inflight must be positive: %d", c.MaxInflight)
	}
	if c.InitialTarget < 0 || c.InitialTarget > c.MaxInflight {
		return fmt.Errorf("admission initial target must be between zero and hard limit: %d", c.InitialTarget)
	}
	return nil
}

// Permit 表示一个已占用的准入名额；Release 必须幂等。
type Permit interface {
	Release()
}

// Controller 非阻塞地获取准入名额，不在 Gateway 内建立隐式等待队列。
type Controller interface {
	TryAcquire() (Permit, error)
	Inflight() int
}

// TargetController exposes a mutable soft target without allowing callers to
// alter the immutable process hard limit or revoke existing permits.
type TargetController interface {
	Controller
	Target() int
	MaxInflight() int
	SetTarget(int) error
}

// TuningMode controls whether a controller suggestion changes admission.
type TuningMode string

const (
	TuningOff    TuningMode = "off"
	TuningShadow TuningMode = "shadow"
	TuningActive TuningMode = "active"
)

func (m TuningMode) Validate() error {
	switch m {
	case TuningOff, TuningShadow, TuningActive:
		return nil
	default:
		return fmt.Errorf("unsupported admission tuning mode %q", m)
	}
}

// TuningConfig contains bounded control-loop parameters. Watermarks are ratios
// of the current target and apply only to new admissions.
type TuningConfig struct {
	Mode          TuningMode
	MinTarget     int
	MaxTarget     int
	Step          int
	Interval      time.Duration
	Cooldown      time.Duration
	HighWatermark float64
	LowWatermark  float64
}

func (c TuningConfig) Validate(hardLimit int) error {
	if err := c.Mode.Validate(); err != nil {
		return err
	}
	if c.MinTarget <= 0 || c.MaxTarget <= 0 || c.MinTarget > c.MaxTarget || c.MaxTarget > hardLimit || c.Step <= 0 {
		return fmt.Errorf("admission tuning target bounds are invalid: min=%d max=%d step=%d hard=%d", c.MinTarget, c.MaxTarget, c.Step, hardLimit)
	}
	if c.Interval <= 0 || c.Cooldown < 0 {
		return fmt.Errorf("admission tuning interval must be positive and cooldown must not be negative")
	}
	if c.LowWatermark <= 0 || c.HighWatermark >= 1 || c.LowWatermark >= c.HighWatermark {
		return fmt.Errorf("admission tuning watermarks must satisfy 0 < low < high < 1")
	}
	return nil
}

// Signal is a process-local, monotonic admission observation. It deliberately
// does not include a Prometheus dependency or runtime GPU data.
type Signal struct {
	ObservedAt     time.Time
	Inflight       int
	AcceptedTotal  uint64
	CompletedTotal uint64
	RejectedTotal  uint64
}

// Decision is low-cardinality controller evidence suitable for metrics/logs.
type Decision struct {
	Mode            TuningMode
	ObservedAt      time.Time
	PreviousTarget  int
	SuggestedTarget int
	AppliedTarget   int
	HardLimit       int
	Inflight        int
	AcceptedDelta   uint64
	CompletedDelta  uint64
	RejectedDelta   uint64
	Valid           bool
	Changed         bool
	Reason          string
}

// SignalSource and DecisionObserver are the real substitution boundary between
// admission control and Gateway metrics; neither package owns the other.
type SignalSource func() Signal
type DecisionObserver func(Decision)

// Tuner owns the closeable control-loop lifecycle.
type Tuner interface {
	Snapshot() Decision
	Close() error
}
