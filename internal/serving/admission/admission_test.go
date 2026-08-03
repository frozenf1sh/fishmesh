package admission

import (
	"errors"
	"testing"
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
	if _, err := controller.TryAcquire(); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second acquire error = %v", err)
	}
	permit.Release()
	permit.Release()
	if controller.Inflight() != 0 {
		t.Fatalf("permit leaked: %d", controller.Inflight())
	}
}
