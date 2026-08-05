package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	zmq4 "github.com/go-zeromq/zmq4"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	topicPrefix    = "kv@"
	reconnectDelay = time.Second
	replayTimeout  = 3 * time.Second
)

// streamSnapshot 是订阅可靠性的只读快照。
// Valid 只在 replay 心跳新鲜、没有不可恢复 gap 且流未被人工暂停时成立。
type streamSnapshot struct {
	Backend         string            `json:"backend"`
	Enabled         bool              `json:"enabled"`
	Valid           bool              `json:"valid"`
	InvalidReason   string            `json:"invalid_reason,omitempty"`
	LastSequence    uint64            `json:"last_sequence"`
	HasSequence     bool              `json:"has_sequence"`
	LastReplayAt    time.Time         `json:"last_replay_at,omitempty"`
	LastEventAt     time.Time         `json:"last_event_at,omitempty"`
	LastEventLagMS  float64           `json:"last_event_lag_ms"`
	ReplayBatches   uint64            `json:"replay_batches"`
	DuplicateEvents uint64            `json:"duplicate_batches"`
	EventCounts     map[string]uint64 `json:"event_counts"`
}

// eventStream 管理一个 vLLM Pod 的实时订阅和 replay 心跳。
//
// vLLM 在空闲时不会发送 heartbeat，所以不能用“最后一条业务事件时间”判断 freshness。
// 这里周期性请求 replay_endpoint；即使没有遗漏，END 响应也能证明 publisher 仍可达。
type eventStream struct {
	ctx          context.Context
	backend      backendConfig
	pool         *kvevents.Pool
	index        kvblock.Index
	adapter      kvevents.EngineAdapter
	freshnessTTL time.Duration
	replayPeriod time.Duration

	mu              sync.RWMutex
	enabled         bool
	invalidReason   string
	hasSequence     bool
	lastSequence    uint64
	lastReplayAt    time.Time
	lastEventAt     time.Time
	lastEventLagMS  float64
	replayBatches   uint64
	duplicateEvents uint64
	eventCounts     map[string]uint64
	runCancel       context.CancelFunc
}

func newEventStream(
	ctx context.Context,
	backend backendConfig,
	pool *kvevents.Pool,
	index kvblock.Index,
	adapter kvevents.EngineAdapter,
	freshnessTTL time.Duration,
	replayPeriod time.Duration,
) *eventStream {
	stream := &eventStream{
		ctx:           ctx,
		backend:       backend,
		pool:          pool,
		index:         index,
		adapter:       adapter,
		freshnessTTL:  freshnessTTL,
		replayPeriod:  replayPeriod,
		enabled:       true,
		invalidReason: "replay-not-confirmed",
		eventCounts:   make(map[string]uint64),
	}
	stream.startLocked()
	return stream
}

func (s *eventStream) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled == enabled {
		return
	}

	s.enabled = enabled
	if !enabled {
		if s.runCancel != nil {
			s.runCancel()
			s.runCancel = nil
		}
		s.invalidReason = "subscriber-disabled"
		return
	}

	// 恢复时保留 lastSequence，先从下一序列 replay，再继续实时消费。
	s.invalidReason = "replay-not-confirmed"
	s.startLocked()
}

func (s *eventStream) Invalidate(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidReason = reason
}

func (s *eventStream) Snapshot() streamSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := make(map[string]uint64, len(s.eventCounts))
	for eventType, count := range s.eventCounts {
		counts[eventType] = count
	}
	valid := s.enabled && s.invalidReason == "" && time.Since(s.lastReplayAt) <= s.freshnessTTL
	reason := s.invalidReason
	if reason == "" && !valid {
		reason = "replay-heartbeat-stale"
	}
	return streamSnapshot{
		Backend:         s.backend.ID,
		Enabled:         s.enabled,
		Valid:           valid,
		InvalidReason:   reason,
		LastSequence:    s.lastSequence,
		HasSequence:     s.hasSequence,
		LastReplayAt:    s.lastReplayAt,
		LastEventAt:     s.lastEventAt,
		LastEventLagMS:  s.lastEventLagMS,
		ReplayBatches:   s.replayBatches,
		DuplicateEvents: s.duplicateEvents,
		EventCounts:     counts,
	}
}

func (s *eventStream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCancel != nil {
		s.runCancel()
		s.runCancel = nil
	}
}

func (s *eventStream) startLocked() {
	runCtx, cancel := context.WithCancel(s.ctx)
	s.runCancel = cancel
	go s.runLive(runCtx)
	go s.runReplayHeartbeats(runCtx)
}

func (s *eventStream) runLive(ctx context.Context) {
	logger := log.FromContext(s.ctx).WithName("r6a-live-subscriber").WithValues("backend", s.backend.ID)
	for ctx.Err() == nil {
		if err := s.receiveLive(ctx); err != nil && ctx.Err() == nil {
			logger.Error(err, "实时 KVEvents 订阅中断")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

func (s *eventStream) receiveLive(ctx context.Context) error {
	subscriber := zmq4.NewSub(ctx)
	defer subscriber.Close()
	if err := subscriber.Dial(s.backend.EventsEndpoint); err != nil {
		return fmt.Errorf("连接实时 endpoint %s: %w", s.backend.EventsEndpoint, err)
	}
	if err := subscriber.SetOption(zmq4.OptionSubscribe, topicPrefix); err != nil {
		return fmt.Errorf("订阅 topic %s: %w", topicPrefix, err)
	}

	for {
		message, err := subscriber.Recv()
		if err != nil {
			return err
		}
		if len(message.Frames) != 3 || len(message.Frames[1]) < 8 {
			continue
		}
		s.accept(rawEvent{
			topic:    string(message.Frames[0]),
			sequence: binary.BigEndian.Uint64(message.Frames[1]),
			payload:  message.Frames[2],
		}, false)
	}
}

func (s *eventStream) runReplayHeartbeats(ctx context.Context) {
	// 首次立即 replay，避免等待一个周期后才知道已有 buffer 是否可完整恢复。
	s.replayOnce(ctx)
	ticker := time.NewTicker(s.replayPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.replayOnce(ctx)
		}
	}
}

func (s *eventStream) replayOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, replayTimeout)
	defer cancel()

	requested := s.nextSequence()
	events, err := requestReplay(ctx, s.backend.ReplayEndpoint, s.topic(), requested)
	if err != nil {
		return
	}
	if len(events) > 0 && events[0].sequence > requested {
		// replay buffer 已覆盖不了缺口。继续保留旧映射会把未知状态误报为 exact hit，因此清空该 Pod。
		_ = s.index.Clear(parent, s.backend.ID)
		s.Invalidate("unrecoverable-sequence-gap")
		return
	}
	for _, event := range events {
		s.accept(event, true)
	}

	s.mu.Lock()
	s.lastReplayAt = time.Now()
	if s.invalidReason == "replay-not-confirmed" {
		s.invalidReason = ""
	}
	s.mu.Unlock()
}

func (s *eventStream) accept(event rawEvent, replayed bool) {
	s.mu.Lock()
	if s.hasSequence && event.sequence <= s.lastSequence {
		s.duplicateEvents++
		s.mu.Unlock()
		return
	}
	if s.hasSequence && event.sequence > s.lastSequence+1 {
		s.invalidReason = "sequence-gap-awaiting-replay"
		s.mu.Unlock()
		return
	}

	raw := &kvevents.RawMessage{Topic: event.topic, Sequence: event.sequence, Payload: event.payload}
	_, _, batch, err := s.adapter.ParseMessage(raw)
	if err != nil {
		s.invalidReason = "event-decode-failed"
		s.mu.Unlock()
		return
	}
	for _, item := range batch.Events {
		s.eventCounts[string(item.Type())]++
	}
	if batch.Timestamp > 0 {
		publishedAt := time.Unix(0, int64(batch.Timestamp*float64(time.Second)))
		s.lastEventLagMS = milliseconds(time.Since(publishedAt))
	}
	s.hasSequence = true
	s.lastSequence = event.sequence
	s.lastEventAt = time.Now()
	if replayed {
		s.replayBatches++
	}
	if s.invalidReason == "sequence-gap-awaiting-replay" {
		s.invalidReason = ""
	}
	s.mu.Unlock()

	s.pool.AddTask(raw)
}

func (s *eventStream) nextSequence() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasSequence {
		return 0
	}
	return s.lastSequence + 1
}

func (s *eventStream) topic() string {
	return topicPrefix + s.backend.ID + "@" + defaultModelName
}
