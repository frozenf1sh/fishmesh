package kvcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

type fakeStore struct {
	mu          sync.Mutex
	matched     map[backend.ID]int
	totalBlocks int
	applyErr    error
	applyResult applyResult
	applied     []Event
	cleared     []string
}

func (s *fakeStore) Lookup(_ context.Context, _ Query, _ map[backend.ID]Instance) (map[backend.ID]int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[backend.ID]int, len(s.matched))
	for backendID, blocks := range s.matched {
		result[backendID] = blocks
	}
	return result, s.totalBlocks, nil
}

func (s *fakeStore) Apply(_ context.Context, _ Instance, event Event) (applyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applyErr != nil {
		return applyResult{}, s.applyErr
	}
	s.applied = append(s.applied, event)
	return s.applyResult, nil
}

func (s *fakeStore) Clear(_ context.Context, podIdentifier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared = append(s.cleared, podIdentifier)
	return nil
}

type blockingSource struct{}

func (blockingSource) Follow(ctx context.Context, _ Instance, _ func(Event) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingSource) Replay(context.Context, Instance, uint64, func(Event) error) error {
	return nil
}

func TestConfigAndInstanceValidation(t *testing.T) {
	config := DefaultConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	config.MaxBackendsPerKey = config.MaxInstances - 1
	if err := config.Validate(); err == nil {
		t.Fatal("index that cannot retain every instance was accepted")
	}

	if err := testInstance("backend-a", "uid-a", "10.0.0.1:8000").Validate(); err != nil {
		t.Fatalf("valid instance rejected: %v", err)
	}
	invalid := testInstance("backend-a", "", "10.0.0.1:8000")
	if err := invalid.Validate(); err == nil {
		t.Fatal("instance without Pod UID was accepted")
	}
}

func TestLookupDistinguishesUnknownFromRealMiss(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	config := DefaultConfig()
	store := &fakeStore{matched: map[backend.ID]int{"backend-a": 0}, totalBlocks: 2}
	service := newService(context.Background(), config, blockingSource{}, func() time.Time { return now }, store)
	stream := newEventStream(service.ctx, config, testInstance("backend-a", "uid-a", "10.0.0.1:8000"), blockingSource{}, store, service.clock)
	stream.reason = ReasonNone
	stream.lastReplayAt = now
	service.streams["backend-a"] = stream
	t.Cleanup(func() { _ = service.Close() })

	snapshot, err := service.Lookup(context.Background(), Query{
		Model:       "qwen",
		TokenGroups: [][]uint32{make([]uint32, 32)},
		Backends:    []backend.ID{"backend-a", "backend-unknown"},
	})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	matches := snapshot.Matches()
	if !matches["backend-a"].Valid || matches["backend-a"].MatchedTokens != 0 {
		t.Fatalf("real miss was not published as valid zero: %+v", matches["backend-a"])
	}
	if matches["backend-unknown"].Valid || matches["backend-unknown"].Reason != ReasonBackendUnknown {
		t.Fatalf("unknown backend was confused with miss: %+v", matches["backend-unknown"])
	}

	matches["backend-a"] = Match{MatchedTokens: 99}
	if serviceSnapshot := snapshot.Matches()["backend-a"]; serviceSnapshot.MatchedTokens != 0 {
		t.Fatal("snapshot exposed its internal match map")
	}
}

func TestLookupProtectsQueryLimitsAndClosedState(t *testing.T) {
	config := DefaultConfig()
	config.MaxQueryTokens = 1
	service := newService(context.Background(), config, blockingSource{}, time.Now, &fakeStore{})
	_, err := service.Lookup(context.Background(), Query{
		Model:       "qwen",
		TokenGroups: [][]uint32{{1, 2}},
		Backends:    []backend.ID{"backend-a"},
	})
	requireCode(t, err, CodeCapacity)

	if err := service.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = service.Lookup(context.Background(), Query{
		Model:       "qwen",
		TokenGroups: [][]uint32{{1}},
		Backends:    []backend.ID{"backend-a"},
	})
	requireCode(t, err, CodeClosed)
	if !service.State().Closed() {
		t.Fatal("closed state was not published")
	}
}

func TestLookupAndPodReconcileAreConcurrentSafe(t *testing.T) {
	config := DefaultConfig()
	config.ReplayPeriod = time.Hour
	store := &fakeStore{totalBlocks: 1}
	service := newService(context.Background(), config, blockingSource{}, time.Now, store)
	t.Cleanup(func() { _ = service.Close() })
	if err := service.Reconcile(context.Background(), []Instance{testInstance("backend-a", "uid-0", "pod-0")}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range 50 {
			_, err := service.Lookup(context.Background(), Query{
				Model:       "qwen",
				TokenGroups: [][]uint32{make([]uint32, 16)},
				Backends:    []backend.ID{"backend-a"},
			})
			if err != nil {
				errorsFound <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for index := range 20 {
			instance := testInstance("backend-a", WorkloadUID(fmt.Sprintf("uid-%d", index+1)), fmt.Sprintf("pod-%d", index+1))
			if err := service.Reconcile(context.Background(), []Instance{instance}); err != nil {
				errorsFound <- err
				return
			}
		}
	}()
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent lookup/reconcile: %v", err)
	}
}

func requireCode(t *testing.T, err error, expected ErrorCode) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("expected typed error, got %T: %v", err, err)
	}
	if typed.Code != expected {
		t.Fatalf("expected code %q, got %q: %v", expected, typed.Code, err)
	}
}

func testInstance(backendID backend.ID, uid WorkloadUID, podIdentifier string) Instance {
	return Instance{
		Backend:        backendID,
		PodUID:         uid,
		PodIdentifier:  podIdentifier,
		Model:          "qwen",
		EventsEndpoint: "tcp://127.0.0.1:5557",
		ReplayEndpoint: "tcp://127.0.0.1:5558",
	}
}
