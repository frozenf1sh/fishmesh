package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

const BoundedAffinityPolicyV1 = "bounded-affinity-v1"

// BoundedAffinityConfig contains only thresholds whose units are comparable.
// Queue depth and local in-flight are evaluated independently; they are never
// blended into an arbitrary weighted score.
type BoundedAffinityConfig struct {
	TTL             time.Duration
	MaxEntries      int
	InflightDelta   int64
	QueueDepthDelta float64
	Clock           func() time.Time
}

func DefaultBoundedAffinityConfig() BoundedAffinityConfig {
	return BoundedAffinityConfig{
		TTL:             5 * time.Minute,
		MaxEntries:      10_000,
		InflightDelta:   2,
		QueueDepthDelta: 1,
		Clock:           time.Now,
	}
}

type affinityEntry struct {
	backendID string
	expiresAt time.Time
}

type boundedAffinityStrategy struct {
	config  BoundedAffinityConfig
	mu      sync.Mutex
	entries map[[32]byte]affinityEntry
}

func NewBoundedAffinity(config BoundedAffinityConfig) (Strategy, error) {
	if config.TTL <= 0 {
		return nil, fmt.Errorf("bounded affinity TTL must be positive")
	}
	if config.MaxEntries <= 0 {
		return nil, fmt.Errorf("bounded affinity max entries must be positive")
	}
	if config.InflightDelta < 0 || config.QueueDepthDelta < 0 {
		return nil, fmt.Errorf("bounded affinity deltas must not be negative")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &boundedAffinityStrategy{config: config, entries: make(map[[32]byte]affinityEntry)}, nil
}

func (*boundedAffinityStrategy) Name() string { return ModeBoundedAffinity }

func (s *boundedAffinityStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	eligible := EligibleBackends(snapshot)
	if len(eligible) == 0 {
		return Decision{}, fmt.Errorf("bounded affinity requires at least one backend")
	}

	least := leastLoaded(snapshot, "")
	if routingKey == "" {
		return Decision{
			Backend: least, PreferredBackendID: least.ID,
			Reason: "missing-key-least-loaded", Policy: BoundedAffinityPolicyV1,
		}, nil
	}

	keyHash := sha256.Sum256([]byte(routingKey))
	preferred, hit := s.preferred(keyHash, snapshot.Backends)
	if !hit {
		preferred = rendezvousBackend(keyHash, snapshot.Backends)
		s.remember(keyHash, preferred.ID)
	}

	selected, spilloverReason := selectWithinBounds(preferred, snapshot, s.config)
	reason := "affinity-hit"
	if !hit {
		reason = "affinity-miss"
	}
	if spilloverReason != "" {
		reason = "affinity-spillover"
	}
	// Active keys use sliding expiration. Spillover does not rewrite the
	// preferred backend, so locality resumes when pressure subsides.
	s.remember(keyHash, preferred.ID)
	return Decision{
		Backend: selected, PreferredBackendID: preferred.ID, Reason: reason,
		SpilloverReason: spilloverReason, Policy: BoundedAffinityPolicyV1,
	}, nil
}

func (s *boundedAffinityStrategy) ReconcileBackends(backends []Backend) {
	active := make(map[string]struct{}, len(backends))
	for _, backend := range backends {
		active[backend.ID] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if _, ok := active[entry.backendID]; !ok {
			delete(s.entries, key)
		}
	}
}

func (s *boundedAffinityStrategy) preferred(key [32]byte, backends []Backend) (Backend, bool) {
	now := s.config.Clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		delete(s.entries, key)
		return Backend{}, false
	}
	for _, backend := range backends {
		if backend.ID == entry.backendID {
			return backend, true
		}
	}
	delete(s.entries, key)
	return Backend{}, false
}

func (s *boundedAffinityStrategy) remember(key [32]byte, backendID string) {
	now := s.config.Clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.config.MaxEntries {
		s.collectOrEvictOldest(now)
	}
	s.entries[key] = affinityEntry{backendID: backendID, expiresAt: now.Add(s.config.TTL)}
}

func (s *boundedAffinityStrategy) collectOrEvictOldest(now time.Time) {
	var oldestKey [32]byte
	var oldestExpiry time.Time
	foundOldest := false
	for key, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, key)
			continue
		}
		if !foundOldest || entry.expiresAt.Before(oldestExpiry) {
			oldestKey, oldestExpiry, foundOldest = key, entry.expiresAt, true
		}
	}
	if len(s.entries) >= s.config.MaxEntries && foundOldest {
		delete(s.entries, oldestKey)
	}
}

func selectWithinBounds(preferred Backend, snapshot Snapshot, config BoundedAffinityConfig) (Backend, string) {
	least := leastLoaded(snapshot, preferred.ID)
	if reason, blocked := snapshot.Ineligible[preferred.ID]; blocked {
		if reason == "" {
			reason = "ineligible"
		}
		return least, reason
	}
	if least.ID == preferred.ID {
		return preferred, ""
	}
	if queueAvailableForAll(snapshot) {
		preferredQueue := snapshot.Observations[preferred.ID].QueueLength.Value
		leastQueue := snapshot.Observations[least.ID].QueueLength.Value
		if preferredQueue > leastQueue+config.QueueDepthDelta {
			return least, "queue-depth"
		}
	}
	if snapshot.Inflight[preferred.ID] > snapshot.Inflight[least.ID]+config.InflightDelta {
		return least, "local-inflight"
	}
	return preferred, ""
}

func leastLoaded(snapshot Snapshot, tieBreakerID string) Backend {
	backends := EligibleBackends(snapshot)
	best := backends[0]
	useQueue := queueAvailableForAll(snapshot)
	for _, candidate := range backends[1:] {
		if lessLoaded(candidate, best, snapshot, tieBreakerID, useQueue) {
			best = candidate
		}
	}
	return best
}

func lessLoaded(candidate, current Backend, snapshot Snapshot, tieBreakerID string, useQueue bool) bool {
	if useQueue {
		candidateQueue := snapshot.Observations[candidate.ID].QueueLength.Value
		currentQueue := snapshot.Observations[current.ID].QueueLength.Value
		if candidateQueue != currentQueue {
			return candidateQueue < currentQueue
		}
	}
	candidateInflight := snapshot.Inflight[candidate.ID]
	currentInflight := snapshot.Inflight[current.ID]
	if candidateInflight != currentInflight {
		return candidateInflight < currentInflight
	}
	if candidate.ID == tieBreakerID {
		return true
	}
	if current.ID == tieBreakerID {
		return false
	}
	return candidate.ID < current.ID
}

func queueAvailableForAll(snapshot Snapshot) bool {
	if len(snapshot.Observations) == 0 {
		return false
	}
	for _, backend := range EligibleBackends(snapshot) {
		observation, ok := snapshot.Observations[backend.ID]
		if !ok || !observation.QueueLength.Valid {
			return false
		}
	}
	return true
}

// rendezvousBackend minimizes remapping when EndpointSlice membership changes.
func rendezvousBackend(key [32]byte, backends []Backend) Backend {
	best := backends[0]
	bestScore := rendezvousScore(key, best.ID)
	for _, candidate := range backends[1:] {
		score := rendezvousScore(key, candidate.ID)
		if score > bestScore || (score == bestScore && candidate.ID < best.ID) {
			best, bestScore = candidate, score
		}
	}
	return best
}

func rendezvousScore(key [32]byte, backendID string) uint64 {
	hash := sha256.New()
	_, _ = hash.Write(key[:])
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(backendID))
	return binary.BigEndian.Uint64(hash.Sum(nil)[:8])
}
