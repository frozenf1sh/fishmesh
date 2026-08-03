package discovery

import (
	"context"
	"fmt"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

var _ Resolver = staticResolver{}

type staticResolver struct {
	backends []backend.Backend
}

// NewStatic validates and creates a resolver for an immutable endpoint list.
// It is intended for experiments and tests, not for long-lived Pod IPs.
func NewStatic(backends []backend.Backend) (Resolver, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("static resolver requires at least one backend")
	}
	copyBackends := append([]backend.Backend(nil), backends...)
	for _, candidate := range copyBackends {
		if candidate.ID == "" || candidate.URL == "" {
			return nil, fmt.Errorf("backend ID and URL must not be empty")
		}
	}
	return staticResolver{backends: copyBackends}, nil
}

func (r staticResolver) Snapshot(context.Context) ([]backend.Backend, error) {
	return append([]backend.Backend(nil), r.backends...), nil
}

func (r staticResolver) Status() ResolverStatus {
	return ResolverStatus{Status: StatusOK, ReadyBackends: len(r.backends)}
}

func (staticResolver) Close() error { return nil }
