package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

var _ Strategy = loadAwareStrategy{}

type loadAwareStrategy struct{}

// NewLoadAware returns local in-flight-aware selection.
func NewLoadAware() Strategy {
	return loadAwareStrategy{}
}

func (loadAwareStrategy) Name() Mode {
	return ModeLoadAware
}

func (loadAwareStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	backends := EligibleBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("load-aware routing requires at least one backend")
	}
	hash := sha256.Sum256([]byte(routingKey))
	start := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(backends)))
	best := backends[start]
	bestInflight := snapshot.Inflight[best.ID]
	for offset := 1; offset < len(backends); offset++ {
		candidate := backends[(start+offset)%len(backends)]
		candidateInflight := snapshot.Inflight[candidate.ID]
		if candidateInflight < bestInflight {
			best = candidate
			bestInflight = candidateInflight
		}
	}
	return Decision{
		Backend:            best,
		PreferredBackendID: best.ID,
		Reason:             ReasonLeastInflight,
		Policy:             PolicyLeastInflightV1,
	}, nil
}
