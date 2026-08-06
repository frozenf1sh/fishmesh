package kvcache

import (
	"context"
	"fmt"
	"time"
)

func (s *eventStream) runReplay(ctx context.Context) {
	defer s.wg.Done()
	s.replayOnce(ctx)
	if s.hasFatalReason() {
		return
	}
	ticker := time.NewTicker(s.config.ReplayPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.replayOnce(ctx)
			if s.hasFatalReason() {
				return
			}
		}
	}
}

func (s *eventStream) replayOnce(parent context.Context) {
	if s.hasFatalReason() {
		return
	}
	ctx, cancel := context.WithTimeout(parent, s.config.ReplayTimeout)
	defer cancel()

	requested := s.nextSequence()
	count := 0
	err := s.source.Replay(ctx, s.instance, requested, func(event Event) error {
		if count >= s.config.MaxReplayEvents {
			err := fmt.Errorf("replay exceeds %d event batches", s.config.MaxReplayEvents)
			s.processMu.Lock()
			s.invalidateAndClear(ctx, ReasonReplayCapacityExceeded, err)
			s.processMu.Unlock()
			return err
		}
		count++
		return s.accept(ctx, event, true)
	})
	if err != nil {
		if parent.Err() == nil {
			s.recordError(fmt.Errorf("replay from sequence %d: %w", requested, err))
		}
		return
	}

	// 只有收到 replay END 后才能刷新 freshness。live gap 还未补到 gapUntil 时继续保持 invalid。
	s.mu.Lock()
	s.lastReplayAt = s.clock()
	s.lastError = ""
	switch s.reason {
	case ReasonReplayNotConfirmed:
		s.reason = ReasonNone
	case ReasonSequenceGap:
		if s.hasSeq && s.lastSeq >= s.gapUntil {
			s.reason = ReasonNone
			s.gapUntil = 0
		}
	}
	s.mu.Unlock()
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
