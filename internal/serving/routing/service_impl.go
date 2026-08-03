package routing

import (
	"fmt"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

var _ Strategy = serviceStrategy{}

type serviceStrategy struct {
	backend backend.Backend
}

// NewService returns the Kubernetes Service baseline strategy.
func NewService(service backend.Backend) Strategy {
	return serviceStrategy{backend: service}
}

func (serviceStrategy) Name() Mode {
	return ModeService
}

func (s serviceStrategy) Select(_ string, _ Snapshot) (Decision, error) {
	if s.backend.ID == "" || s.backend.URL == "" {
		return Decision{}, fmt.Errorf("service backend is incomplete")
	}
	return Decision{
		Backend:            s.backend,
		PreferredBackendID: s.backend.ID,
		Reason:             ReasonServiceDefault,
		Policy:             PolicyServiceV1,
	}, nil
}
