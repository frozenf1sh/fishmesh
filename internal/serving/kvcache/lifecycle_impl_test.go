package kvcache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

type replaySource struct {
	mu           sync.Mutex
	replayEvents []Event
}

func (s *replaySource) Follow(ctx context.Context, _ Instance, _ func(Event) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *replaySource) Replay(_ context.Context, _ Instance, _ uint64, handler func(Event) error) error {
	s.mu.Lock()
	events := append([]Event(nil), s.replayEvents...)
	s.mu.Unlock()
	for _, event := range events {
		if err := handler(event); err != nil {
			return err
		}
	}
	return nil
}

func TestEventStreamDoesNotCommitFailedApply(t *testing.T) {
	config := testConfig()
	instance := testInstance("backend-a", "uid-a", "pod-a")
	store := &fakeStore{applyErr: errors.New("index write failed")}
	stream := newEventStream(context.Background(), config, instance, &replaySource{}, store, time.Now)

	if err := stream.accept(context.Background(), testEvent(instance, 0), false); err == nil {
		t.Fatal("failed index apply was accepted")
	}
	state := stream.Snapshot()
	if state.HasSequence || state.Reason != ReasonEventApplyFailed {
		t.Fatalf("sequence advanced before apply completed: %+v", state)
	}
	if len(store.cleared) != 1 {
		t.Fatalf("failed partial apply was not cleared: %v", store.cleared)
	}
}

func TestEventStreamObservesOnlyTimestampedSuccessfulApply(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	instance := testInstance("backend-a", "uid-a", "pod-a")
	observations := make([]EventObservation, 0, 1)
	observer := eventObserverFunc(func(observation EventObservation) {
		observations = append(observations, observation)
	})

	store := &fakeStore{applyResult: applyResult{publishedAt: now.Add(-3 * time.Millisecond)}}
	stream := newEventStream(context.Background(), testConfig(), instance, &replaySource{}, store, func() time.Time { return now }, observer)
	if err := stream.accept(context.Background(), testEvent(instance, 0), false); err != nil {
		t.Fatalf("accept timestamped event: %v", err)
	}
	if len(observations) != 1 || observations[0].Backend != instance.Backend || observations[0].Replayed || observations[0].PublishToApply != 3*time.Millisecond {
		t.Fatalf("timestamped success observation = %+v", observations)
	}
	if state := stream.Snapshot(); state.LastEventLag != 3*time.Millisecond {
		t.Fatalf("last event lag = %s, want 3ms", state.LastEventLag)
	}

	store.applyResult = applyResult{}
	if err := stream.accept(context.Background(), testEvent(instance, 1), true); err != nil {
		t.Fatalf("accept event without timestamp: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("event without publisher timestamp emitted observation: %+v", observations)
	}

	store.applyErr = errors.New("index write failed")
	if err := stream.accept(context.Background(), testEvent(instance, 2), false); err == nil {
		t.Fatal("failed apply was accepted")
	}
	if len(observations) != 1 {
		t.Fatalf("failed apply emitted observation: %+v", observations)
	}
}

func TestEventStreamDoesNotObserveFuturePublisherTimestampAsZeroLag(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	instance := testInstance("backend-a", "uid-a", "pod-a")
	observations := make([]EventObservation, 0, 1)
	store := &fakeStore{applyResult: applyResult{publishedAt: now.Add(time.Millisecond)}}
	stream := newEventStream(context.Background(), testConfig(), instance, &replaySource{}, store, func() time.Time { return now }, eventObserverFunc(func(observation EventObservation) {
		observations = append(observations, observation)
	}))

	if err := stream.accept(context.Background(), testEvent(instance, 0), false); err != nil {
		t.Fatalf("accept event with future publisher timestamp: %v", err)
	}
	if len(observations) != 0 || stream.Snapshot().LastEventLag != 0 {
		t.Fatalf("future publisher timestamp was converted into zero-lag observation: %+v / %+v", observations, stream.Snapshot())
	}
}

func (s *replaySource) setEvents(events ...Event) {
	s.mu.Lock()
	s.replayEvents = append([]Event(nil), events...)
	s.mu.Unlock()
}

func TestEventStreamRecoversLiveGapThroughReplay(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	config := testConfig()
	instance := testInstance("backend-a", "uid-a", "10.0.0.1:8000")
	source := &replaySource{}
	store := &fakeStore{}
	stream := newEventStream(context.Background(), config, instance, source, store, clock)

	if err := stream.accept(context.Background(), testEvent(instance, 0), false); err != nil {
		t.Fatalf("accept sequence 0: %v", err)
	}
	stream.replayOnce(context.Background())
	if state := stream.Snapshot(); !state.Valid || state.LastSequence != 0 {
		t.Fatalf("initial replay did not validate stream: %+v", state)
	}

	if err := stream.accept(context.Background(), testEvent(instance, 2), false); err == nil {
		t.Fatal("live sequence gap was accepted")
	}
	if state := stream.Snapshot(); state.Valid || state.Reason != ReasonSequenceGap || state.LastSequence != 0 {
		t.Fatalf("gap did not invalidate without advancing sequence: %+v", state)
	}

	source.setEvents(testEvent(instance, 1), testEvent(instance, 2))
	stream.replayOnce(context.Background())
	state := stream.Snapshot()
	if !state.Valid || state.LastSequence != 2 || state.AppliedBatches != 3 || state.ReplayBatches != 2 {
		t.Fatalf("replay did not restore a continuous applied sequence: %+v", state)
	}
}

func TestEventStreamDetectsSequenceResetAndClearsFreshness(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	instance := testInstance("backend-a", "uid-a", "10.0.0.1:8000")
	store := &fakeStore{}
	observer := &sequenceResetObserver{}
	stream := newEventStream(context.Background(), testConfig(), instance, &replaySource{}, store, func() time.Time { return now }, observer)
	stream.reason = ReasonNone

	for sequence := uint64(0); sequence <= 4; sequence++ {
		if err := stream.accept(context.Background(), testEvent(instance, sequence), false); err != nil {
			t.Fatalf("accept sequence %d: %v", sequence, err)
		}
	}
	stream.lastReplayAt = now
	if !stream.Snapshot().Valid {
		t.Fatal("pre-reset stream should be valid")
	}

	if err := stream.accept(context.Background(), testEvent(instance, 0), false); err != nil {
		t.Fatalf("accept reset sequence: %v", err)
	}
	state := stream.Snapshot()
	if !state.HasSequence || state.LastSequence != 0 || state.Valid || state.Reason != ReasonSequenceReset || !state.LastReplayAt.IsZero() {
		t.Fatalf("sequence reset did not rebuild invalid state: %+v", state)
	}
	if len(store.cleared) != 1 || observer.previous != 4 || observer.sequence != 0 {
		t.Fatalf("sequence reset cleanup/observation mismatch: cleared=%v observer=%+v", store.cleared, observer)
	}

	stream.replayOnce(context.Background())
	if state := stream.Snapshot(); !state.Valid || state.Reason != ReasonNone {
		t.Fatalf("replay heartbeat did not confirm reset generation: %+v", state)
	}
}

func TestEventStreamRejectsUnrecoverableReplayGap(t *testing.T) {
	config := testConfig()
	instance := testInstance("backend-a", "uid-a", "10.0.0.1:8000")
	source := &replaySource{}
	source.setEvents(testEvent(instance, 3))
	store := &fakeStore{}
	stream := newEventStream(context.Background(), config, instance, source, store, time.Now)

	stream.replayOnce(context.Background())
	state := stream.Snapshot()
	if state.Valid || state.Reason != ReasonUnrecoverableSequenceGap || state.HasSequence {
		t.Fatalf("unrecoverable replay gap was accepted: %+v", state)
	}
	if len(store.cleared) != 1 || store.cleared[0] != instance.PodIdentifier {
		t.Fatalf("partial index was not cleared: %v", store.cleared)
	}
}

func TestEventStreamFreshnessUsesReplayHeartbeat(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	config := testConfig()
	stream := newEventStream(context.Background(), config, testInstance("backend-a", "uid-a", "pod-a"), &replaySource{}, &fakeStore{}, func() time.Time { return now })
	stream.reason = ReasonNone
	stream.lastReplayAt = now

	if !stream.Snapshot().Valid {
		t.Fatal("fresh replay heartbeat was not valid")
	}
	now = now.Add(config.FreshnessTTL + time.Millisecond)
	if state := stream.Snapshot(); state.Valid || state.Reason != ReasonReplayHeartbeatStale {
		t.Fatalf("stale replay heartbeat remained valid: %+v", state)
	}
}

func TestReconcileReplacesPodUIDAfterStoppingAndClearingOldInstance(t *testing.T) {
	config := testConfig()
	config.ReplayPeriod = time.Hour
	source := blockingSource{}
	store := &fakeStore{}
	service := newService(context.Background(), config, source, time.Now, store)
	t.Cleanup(func() { _ = service.Close() })

	oldInstance := testInstance("backend-a", "uid-old", "10.0.0.1:8000")
	if err := service.Reconcile(context.Background(), []Instance{oldInstance}); err != nil {
		t.Fatalf("reconcile old instance: %v", err)
	}
	waitForState(t, service, "backend-a", func(state InstanceState) bool { return state.Valid })

	newInstance := testInstance("backend-a", "uid-new", "10.0.0.2:8000")
	if err := service.Reconcile(context.Background(), []Instance{newInstance}); err != nil {
		t.Fatalf("replace instance: %v", err)
	}
	waitForState(t, service, "backend-a", func(state InstanceState) bool {
		return state.Valid && state.Instance.PodUID == "uid-new"
	})
	if len(store.cleared) == 0 || store.cleared[0] != oldInstance.PodIdentifier {
		t.Fatalf("old Pod index was not cleared before replacement: %v", store.cleared)
	}
}

func testEvent(instance Instance, sequence uint64) Event {
	return Event{Topic: instanceTopic(instance), Sequence: sequence, Payload: []byte{1}}
}

type eventObserverFunc func(EventObservation)

func (f eventObserverFunc) ObserveKVEvent(observation EventObservation) {
	f(observation)
}

type sequenceResetObserver struct {
	previous uint64
	sequence uint64
}

func (*sequenceResetObserver) ObserveKVEvent(EventObservation) {}

func (o *sequenceResetObserver) ObserveSequenceReset(observation SequenceResetObservation) {
	o.previous = observation.PreviousSequence
	o.sequence = observation.Sequence
}

func waitForState(t *testing.T, service *service, backendID string, accepted func(InstanceState) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, exists := service.State().Instances()[backend.ID(backendID)]
		if exists && accepted(state) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state condition not met for %s: %+v", backendID, service.State().Instances()[backend.ID(backendID)])
}

func TestReplayCapacityInvalidatesAndClears(t *testing.T) {
	config := testConfig()
	config.MaxReplayEvents = 1
	instance := testInstance("backend-a", "uid-a", "pod-a")
	source := &replaySource{}
	source.setEvents(testEvent(instance, 0), testEvent(instance, 1))
	store := &fakeStore{}
	stream := newEventStream(context.Background(), config, instance, source, store, time.Now)

	stream.replayOnce(context.Background())
	if state := stream.Snapshot(); state.Reason != ReasonReplayCapacityExceeded {
		t.Fatalf("replay capacity was not enforced: %+v", state)
	}
	if len(store.cleared) != 1 {
		t.Fatalf("capacity failure did not clear partial index: %v", store.cleared)
	}
}
