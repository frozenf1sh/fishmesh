package kvcache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const topicPrefix = "kv@"

// eventStream 管理一个 Pod UID 对应的实时订阅、replay heartbeat 和连续 sequence。
// processMu 让 live/replay 事件同步落入 index；mu 只保护可发布状态，不能持有它执行外部 I/O。
type eventStream struct {
	parent   context.Context
	config   Config
	instance Instance
	source   EventSource
	store    cacheStore
	clock    Clock
	observer EventObserver

	processMu        sync.Mutex
	mu               sync.RWMutex
	started          bool
	closed           bool
	reason           Reason
	lastError        string
	hasSeq           bool
	lastSeq          uint64
	gapUntil         uint64
	lastReplayAt     time.Time
	lastEventAt      time.Time
	lastEventLag     time.Duration
	appliedBatches   uint64
	replayBatches    uint64
	duplicateBatches uint64
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

func newEventStream(
	parent context.Context,
	config Config,
	instance Instance,
	source EventSource,
	store cacheStore,
	clock Clock,
	observers ...EventObserver,
) *eventStream {
	var observer EventObserver
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &eventStream{
		parent:   parent,
		config:   config,
		instance: instance,
		source:   source,
		store:    store,
		clock:    clock,
		reason:   ReasonReplayNotConfirmed, observer: observer,
	}
}

// Start 启动一条 live 流和一条周期 replay 流；Close 会等待二者退出。
func (s *eventStream) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.parent)
	s.started = true
	s.cancel = cancel
	s.wg.Add(2)
	s.mu.Unlock()

	go s.runLive(ctx)
	go s.runReplay(ctx)
}

// Snapshot 返回不暴露 mutex 或内部可变状态的 freshness 快照。
func (s *eventStream) Snapshot() InstanceState {
	now := s.clock()
	s.mu.RLock()
	defer s.mu.RUnlock()

	reason := s.reason
	freshness := time.Duration(0)
	if !s.lastReplayAt.IsZero() {
		freshness = now.Sub(s.lastReplayAt)
		if freshness < 0 {
			freshness = 0
		}
	}
	valid := !s.closed && reason == ReasonNone && !s.lastReplayAt.IsZero() && freshness <= s.config.FreshnessTTL
	if !valid && reason == ReasonNone {
		reason = ReasonReplayHeartbeatStale
	}
	return InstanceState{
		Instance:         s.instance,
		Valid:            valid,
		Reason:           reason,
		HasSequence:      s.hasSeq,
		LastSequence:     s.lastSeq,
		LastReplayAt:     s.lastReplayAt,
		LastEventAt:      s.lastEventAt,
		LastEventLag:     s.lastEventLag,
		Freshness:        freshness,
		AppliedBatches:   s.appliedBatches,
		ReplayBatches:    s.replayBatches,
		DuplicateBatches: s.duplicateBatches,
		LastError:        s.lastError,
	}
}

// Instance 返回该流绑定的不可变实例描述。
func (s *eventStream) Instance() Instance {
	return s.instance
}

// Same 判断现有 subscriber 是否仍对应完全相同的 Pod 实例和事件端点。
func (s *eventStream) Same(instance Instance) bool {
	return s.instance == instance
}

// Closed 返回该流是否已经完成关闭流程。
func (s *eventStream) Closed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// Invalidate 立即撤销 exact 可用性，但不自行选择 fallback。
func (s *eventStream) Invalidate(reason Reason, detail string) {
	s.mu.Lock()
	s.reason = reason
	s.lastError = detail
	s.mu.Unlock()
}

// Close 幂等取消并等待 live/replay goroutine，确保 lifecycle Clear 后不会有旧事件重新写入。
func (s *eventStream) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.reason == ReasonNone || s.reason == ReasonReplayNotConfirmed {
		s.reason = ReasonClosed
	}
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *eventStream) runLive(ctx context.Context) {
	defer s.wg.Done()
	for ctx.Err() == nil {
		err := s.source.Follow(ctx, s.instance, func(event Event) error {
			return s.accept(ctx, event, false)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.recordError(fmt.Errorf("follow live events: %w", err))
		}
		if s.hasFatalReason() {
			return
		}
		if !waitContext(ctx, s.config.ReconnectDelay) {
			return
		}
	}
}

func (s *eventStream) accept(ctx context.Context, event Event, replayed bool) error {
	s.processMu.Lock()
	defer s.processMu.Unlock()

	if len(event.Payload) > s.config.MaxEventBytes {
		err := fmt.Errorf("event payload is %d bytes", len(event.Payload))
		s.invalidateAndClear(ctx, ReasonEventTooLarge, err)
		return err
	}
	if event.Topic != s.topic() {
		err := fmt.Errorf("event topic %q does not match %q", event.Topic, s.topic())
		s.invalidateAndClear(ctx, ReasonEventDecodeFailed, err)
		return err
	}

	expected, duplicate, fatal := s.classifySequence(event.Sequence, replayed)
	if duplicate {
		return nil
	}
	if fatal != nil {
		if replayed {
			s.invalidateAndClear(ctx, ReasonUnrecoverableSequenceGap, fatal)
		}
		return fmt.Errorf("sequence %d, expected %d: %w", event.Sequence, expected, fatal)
	}

	result, err := s.store.Apply(ctx, s.instance, event)
	if err != nil {
		reason := ReasonEventApplyFailed
		var fault *eventFault
		if errors.As(err, &fault) {
			reason = fault.reason
		}
		slog.Error("KV event apply failed", "backend", s.instance.Backend, "pod_identifier", s.instance.PodIdentifier, "sequence", event.Sequence, "replayed", replayed, "reason", reason, "error", err)
		s.invalidateAndClear(ctx, reason, err)
		return err
	}

	// sequence 只在同步 Apply 成功后推进；这是与上游无回执 worker queue 的关键差异。
	appliedAt := s.clock()
	var observation EventObservation
	hasObservation := !result.publishedAt.IsZero() && !result.publishedAt.After(appliedAt)
	if hasObservation {
		observation = EventObservation{Backend: s.instance.Backend, Replayed: replayed, PublishToApply: appliedAt.Sub(result.publishedAt)}
	}

	s.mu.Lock()
	s.hasSeq = true
	s.lastSeq = event.Sequence
	s.lastEventAt = appliedAt
	if hasObservation {
		s.lastEventLag = observation.PublishToApply
	}
	s.appliedBatches++
	if replayed {
		s.replayBatches++
	}
	s.mu.Unlock()
	if hasObservation && s.observer != nil {
		s.observer.ObserveKVEvent(observation)
	}
	return nil
}

func (s *eventStream) classifySequence(sequence uint64, replayed bool) (uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expected := uint64(0)
	if s.hasSeq {
		expected = s.lastSeq + 1
	}
	if sequence < expected {
		s.duplicateBatches++
		return expected, true, nil
	}
	if sequence == expected {
		return expected, false, nil
	}
	if replayed {
		return expected, false, fmt.Errorf("replay buffer starts at %d", sequence)
	}
	s.reason = ReasonSequenceGap
	if sequence > s.gapUntil {
		s.gapUntil = sequence
	}
	return expected, false, fmt.Errorf("live event skipped to %d", sequence)
}

func (s *eventStream) invalidateAndClear(ctx context.Context, reason Reason, cause error) {
	s.Invalidate(reason, cause.Error())
	if err := s.store.Clear(ctx, s.instance.PodIdentifier); err != nil {
		s.recordError(fmt.Errorf("%v; clear partial index: %w", cause, err))
	}
}

func (s *eventStream) nextSequence() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasSeq {
		return 0
	}
	return s.lastSeq + 1
}

func (s *eventStream) hasFatalReason() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch s.reason {
	case ReasonNone, ReasonReplayNotConfirmed, ReasonSequenceGap:
		return false
	default:
		return true
	}
}

func (s *eventStream) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}

func (s *eventStream) topic() string {
	return topicPrefix + s.instance.PodIdentifier + "@" + s.instance.Model
}
