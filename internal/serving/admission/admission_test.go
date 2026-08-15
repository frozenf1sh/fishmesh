package admission

import (
	"errors"
	"testing"
	"time"
)

func TestControllerRejectsWithoutQueueingAndPermitIsIdempotent(t *testing.T) {
	controller, err := New(Config{MaxInflight: 1})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := controller.TryAcquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.TryAcquire(); !errors.Is(err, ErrHardLimit) || !errors.Is(err, ErrCapacity) {
		t.Fatalf("second acquire error = %v", err)
	}
	permit.Release()
	permit.Release()
	if controller.Inflight() != 0 {
		t.Fatalf("permit leaked: %d", controller.Inflight())
	}
}

func TestTargetDecreaseDoesNotRevokeExistingPermit(t *testing.T) {
	controller, err := New(Config{MaxInflight: 3, InitialTarget: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := controller.TryAcquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetTarget(1); err != nil {
		t.Fatal(err)
	}
	if controller.Inflight() != 1 || controller.Target() != 1 {
		t.Fatalf("target decrease revoked state: inflight=%d target=%d", controller.Inflight(), controller.Target())
	}
	if _, err := controller.TryAcquire(); !errors.Is(err, ErrSoftTarget) || !errors.Is(err, ErrCapacity) {
		t.Fatalf("new request was admitted below target: %v", err)
	}
	first.Release()
	second, err := controller.TryAcquire()
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestTunerActiveAdjustsOnlyNewAdmissions(t *testing.T) {
	controller, err := New(Config{MaxInflight: 4, InitialTarget: 4})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := controller.TryAcquire()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(100, 0)
	var decisions []Decision
	config := TuningConfig{Mode: TuningActive, MinTarget: 1, MaxTarget: 4, Step: 1, Interval: time.Second, Cooldown: 0, LowWatermark: 0.25, HighWatermark: 0.5}
	runner, err := NewTuner(config, controller, func() Signal { return Signal{} }, func(decision Decision) { decisions = append(decisions, decision) })
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	impl := runner.(*tuner)
	impl.clock = func() time.Time { return clock }
	impl.step(Signal{ObservedAt: clock, Inflight: 4}, clock)
	if controller.Target() != 4 || runner.Snapshot().Reason != "saturated" {
		t.Fatalf("high utilization without hard rejection changed target: target=%d decision=%+v", controller.Target(), runner.Snapshot())
	}
	impl.step(Signal{ObservedAt: clock.Add(time.Second), Inflight: 4, RejectedTotal: 1, HardRejectedTotal: 1}, clock.Add(time.Second))
	if controller.Target() != 3 || controller.Inflight() != 1 || len(decisions) < 2 || !decisions[len(decisions)-1].Changed {
		t.Fatalf("unexpected active decision: target=%d inflight=%d decisions=%+v", controller.Target(), controller.Inflight(), decisions)
	}
	permit.Release()
}

func TestTunerDoesNotReactToItsOwnSoftTargetRejection(t *testing.T) {
	controller, err := New(Config{MaxInflight: 4, InitialTarget: 2})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(100, 0)
	runner, err := NewTuner(TuningConfig{Mode: TuningActive, MinTarget: 1, MaxTarget: 4, Step: 1, Interval: time.Second, Cooldown: 0, LowWatermark: 0.25, HighWatermark: 0.5}, controller, func() Signal { return Signal{} }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	impl := runner.(*tuner)
	impl.clock = func() time.Time { return clock }
	impl.step(Signal{ObservedAt: clock, Inflight: 2}, clock)
	impl.step(Signal{ObservedAt: clock.Add(time.Second), Inflight: 2, RejectedTotal: 1, SoftRejectedTotal: 1}, clock.Add(time.Second))
	if controller.Target() != 2 || runner.Snapshot().Reason != "soft-target-pressure" {
		t.Fatalf("soft target rejection changed target: target=%d decision=%+v", controller.Target(), runner.Snapshot())
	}
}

func TestTunerShadowOnlySuggestsTarget(t *testing.T) {
	controller, err := New(Config{MaxInflight: 4})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(100, 0)
	config := TuningConfig{Mode: TuningShadow, MinTarget: 1, MaxTarget: 4, Step: 1, Interval: time.Second, Cooldown: 0, LowWatermark: 0.25, HighWatermark: 0.5}
	runner, err := NewTuner(config, controller, func() Signal { return Signal{} }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	impl := runner.(*tuner)
	impl.clock = func() time.Time { return clock }
	impl.step(Signal{ObservedAt: clock, Inflight: 4}, clock)
	if controller.Target() != 4 || runner.Snapshot().SuggestedTarget != 4 || runner.Snapshot().Reason != "saturated" || runner.Snapshot().AppliedTarget != 4 {
		t.Fatalf("shadow changed target: target=%d decision=%+v", controller.Target(), runner.Snapshot())
	}
}
