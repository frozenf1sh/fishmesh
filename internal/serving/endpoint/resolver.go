// Package endpoint defines backend discovery boundaries. Static discovery is
// sufficient for the MVP; EndpointSlice discovery can implement Resolver later
// without changing routing policies or the gateway handler.
package endpoint

import (
	"context"
	"fmt"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

// Resolver returns a point-in-time backend snapshot.
type Resolver interface {
	Snapshot(context.Context) ([]routing.Backend, error)
}

type staticResolver struct{ backends []routing.Backend }

// NewStatic validates and creates a resolver for an immutable endpoint list.
// It is intended for experiments and unit tests, not for long-lived Pod IPs.
func NewStatic(backends []routing.Backend) (Resolver, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("static resolver requires at least one backend")
	}
	copyBackends := append([]routing.Backend(nil), backends...)
	for _, backend := range copyBackends {
		if backend.ID == "" || backend.URL == "" {
			return nil, fmt.Errorf("backend ID and URL must not be empty")
		}
	}
	return staticResolver{backends: copyBackends}, nil
}

func (r staticResolver) Snapshot(context.Context) ([]routing.Backend, error) {
	return append([]routing.Backend(nil), r.backends...), nil
}
