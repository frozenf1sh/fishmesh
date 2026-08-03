package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

var _ Strategy = prefixAffinityStrategy{}

type prefixAffinityStrategy struct{}

// NewPrefixAffinity returns stable prefix-to-backend selection.
func NewPrefixAffinity() Strategy {
	return prefixAffinityStrategy{}
}

func (prefixAffinityStrategy) Name() Mode {
	return ModePrefixAffinity
}

func (prefixAffinityStrategy) Select(routingKey string, snapshot Snapshot) (Decision, error) {
	backends := EligibleBackends(snapshot)
	if len(backends) == 0 {
		return Decision{}, fmt.Errorf("prefix affinity requires at least one backend")
	}
	hash := sha256.Sum256([]byte(routingKey))
	index := int(binary.BigEndian.Uint32(hash[:4]) % uint32(len(backends)))
	selected := backends[index]
	return Decision{
		Backend:            selected,
		PreferredBackendID: selected.ID,
		Reason:             ReasonPrefixAffinity,
		Policy:             PolicyPureAffinityV1,
	}, nil
}
