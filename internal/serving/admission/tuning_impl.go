package admission

import (
	"sync"
	"time"
)

var _ Tuner = (*tuner)(nil)

type tuner struct {
	config   TuningConfig
	targeter TargetController
	source   SignalSource
	observer DecisionObserver
	clock    func() time.Time
	cancel   func()
	done     chan struct{}

	mu         sync.RWMutex
	previous   Signal
	hasSample  bool
	lastAction time.Time
	snapshot   Decision
	close      sync.Once
}

// NewTuner creates an optional, closeable admission control loop. Off mode still
// publishes the initial target so metrics show the active hard/soft boundary.
func NewTuner(config TuningConfig, targeter TargetController, source SignalSource, observer DecisionObserver) (Tuner, error) {
	if targeter == nil {
		return nil, ErrTarget
	}
	if err := config.Validate(targeter.MaxInflight()); err != nil {
		return nil, err
	}
	t := &tuner{config: config, targeter: targeter, source: source, observer: observer, clock: time.Now, done: make(chan struct{})}
	t.snapshot = Decision{Mode: config.Mode, PreviousTarget: targeter.Target(), SuggestedTarget: targeter.Target(), AppliedTarget: targeter.Target(), HardLimit: targeter.MaxInflight(), Valid: true, Reason: "initial"}
	if observer != nil {
		observer(t.snapshot)
	}
	if config.Mode == TuningOff {
		close(t.done)
		return t, nil
	}
	if source == nil {
		return nil, ErrTarget
	}
	ctxDone := make(chan struct{})
	t.cancel = func() { close(ctxDone) }
	go t.loop(ctxDone)
	return t, nil
}

func (t *tuner) loop(done <-chan struct{}) {
	ticker := time.NewTicker(t.config.Interval)
	defer ticker.Stop()
	defer close(t.done)
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			t.step(t.source(), t.clock())
		}
	}
}

func (t *tuner) step(signal Signal, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	decision := Decision{
		Mode: t.config.Mode, ObservedAt: signal.ObservedAt, PreviousTarget: t.targeter.Target(),
		SuggestedTarget: t.targeter.Target(), AppliedTarget: t.targeter.Target(), HardLimit: t.targeter.MaxInflight(), Inflight: signal.Inflight,
		Valid: true, Reason: "hold",
	}
	if signal.ObservedAt.IsZero() || signal.Inflight < 0 || (now.Sub(signal.ObservedAt) > 2*t.config.Interval) {
		decision.Valid = false
		decision.Reason = "signal-stale-or-invalid"
		t.publishLocked(decision)
		return
	}
	if t.hasSample {
		if signal.ObservedAt.Before(t.previous.ObservedAt) || signal.AcceptedTotal < t.previous.AcceptedTotal || signal.CompletedTotal < t.previous.CompletedTotal || signal.RejectedTotal < t.previous.RejectedTotal {
			decision.Valid = false
			decision.Reason = "signal-counter-reset"
			t.publishLocked(decision)
			return
		}
		decision.AcceptedDelta = signal.AcceptedTotal - t.previous.AcceptedTotal
		decision.CompletedDelta = signal.CompletedTotal - t.previous.CompletedTotal
		decision.RejectedDelta = signal.RejectedTotal - t.previous.RejectedTotal
	}
	t.previous, t.hasSample = signal, true
	current := decision.PreviousTarget
	ratio := float64(signal.Inflight) / float64(current)
	suggested := current
	switch {
	case decision.RejectedDelta > 0 || ratio >= t.config.HighWatermark:
		suggested = maxInt(t.config.MinTarget, current-t.config.Step)
		decision.Reason = "overloaded"
	case decision.AcceptedDelta > 0 && ratio <= t.config.LowWatermark:
		suggested = minInt(t.config.MaxTarget, current+t.config.Step)
		decision.Reason = "underutilized"
	}
	decision.SuggestedTarget = suggested
	if suggested != current && !t.lastAction.IsZero() && now.Sub(t.lastAction) < t.config.Cooldown {
		decision.Reason += "-cooldown"
		t.publishLocked(decision)
		return
	}
	if suggested != current && t.config.Mode == TuningActive {
		if err := t.targeter.SetTarget(suggested); err != nil {
			decision.Valid = false
			decision.Reason = "target-update-failed"
			t.publishLocked(decision)
			return
		}
		decision.Changed = true
		t.lastAction = now
	}
	decision.AppliedTarget = t.targeter.Target()
	t.publishLocked(decision)
}

func (t *tuner) publishLocked(decision Decision) {
	t.snapshot = decision
	observer := t.observer
	if observer != nil {
		observer(decision)
	}
}

func (t *tuner) Snapshot() Decision {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot
}

func (t *tuner) Close() error {
	t.close.Do(func() {
		if t.cancel != nil {
			t.cancel()
			<-t.done
		}
	})
	return nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
