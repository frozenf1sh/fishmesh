package prediction

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestTrackerContractsShadowNeverChangesSelectionAndRecordsOnlyFirstEvent(t *testing.T) {
	now := time.Unix(100, 0)
	tracker, err := New(Config{Mode: ModeShadow, MaxSamples: 8, MaxSampleAge: time.Minute, MinimumSamples: 2, RefitEvery: 1, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	featuresA := Features{UncachedTokens: 10, QueueDepth: 0, Running: 0, LocalInflight: 0, LoadValid: true}
	featuresB := Features{UncachedTokens: 1, QueueDepth: 0, Running: 0, LocalInflight: 0, LoadValid: true}
	for range 2 {
		for _, item := range []struct {
			backend  backend.ID
			features Features
			ttft     time.Duration
		}{{"a", featuresA, 100 * time.Millisecond}, {"b", featuresB, 10 * time.Millisecond}} {
			ticket, _ := tracker.Begin(BeginInput{Selected: item.backend, Features: item.features, Candidates: []Candidate{{Backend: item.backend, Features: item.features}}})
			ticket.ObserveFirstToken(item.ttft)
		}
	}
	ticket, shadow := tracker.Begin(BeginInput{Selected: "a", Features: featuresA, Candidates: []Candidate{{Backend: "a", Features: featuresA}, {Backend: "b", Features: featuresB}}})
	if shadow.Status != StatusAvailable || shadow.WouldSelect != "b" {
		t.Fatalf("shadow = %+v, want available backend b", shadow)
	}
	if result := ticket.ObserveFirstToken(90 * time.Millisecond); !result.Valid {
		t.Fatalf("first observation = %+v, want valid prediction error", result)
	}
	if result := ticket.ObserveFirstToken(1 * time.Millisecond); result.Actual != 90*time.Millisecond {
		t.Fatalf("second observation = %+v, want first event retained", result)
	}
}

func TestTrackerContractsUnknownLoadAndStaleSamplesDoNotBecomePredictions(t *testing.T) {
	now := time.Unix(100, 0)
	tracker, err := New(Config{Mode: ModeShadow, MaxSamples: 4, MaxSampleAge: time.Second, MinimumSamples: 1, RefitEvery: 1, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	known := Features{LoadValid: true}
	ticket, _ := tracker.Begin(BeginInput{Selected: "a", Features: known, Candidates: []Candidate{{Backend: "a", Features: known}}})
	ticket.ObserveFirstToken(time.Millisecond)
	_, unknown := tracker.Begin(BeginInput{Selected: "a", Features: known, Candidates: []Candidate{{Backend: "a", Features: known}, {Backend: "b", Features: Features{}}}})
	if unknown.Status != StatusLoadUnavailable {
		t.Fatalf("unknown load status = %q, want %q", unknown.Status, StatusLoadUnavailable)
	}
	now = now.Add(2 * time.Second)
	_, stale := tracker.Begin(BeginInput{Selected: "a", Features: known, Candidates: []Candidate{{Backend: "a", Features: known}}})
	if stale.Status != StatusInsufficientData {
		t.Fatalf("stale status = %q, want %q", stale.Status, StatusInsufficientData)
	}
}

func TestConfigContractsRejectInvalidBounds(t *testing.T) {
	if _, err := New(Config{Mode: ModeShadow, MaxSamples: 1, MaxSampleAge: time.Second, MinimumSamples: 2, RefitEvery: 1}); err == nil {
		t.Fatal("New accepted minimum samples above maximum")
	}
}

func TestConfigRejectsUnboundedRefitInterval(t *testing.T) {
	if _, err := New(Config{Mode: ModeShadow, MaxSamples: 4, MaxSampleAge: time.Second, MinimumSamples: 1}); err == nil {
		t.Fatal("New accepted a non-positive refit interval")
	}
}
