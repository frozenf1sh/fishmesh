// Package identity maps an inference backend to Kubernetes workload identity.
package identity

import (
	"context"
	"net/http"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	StatusOK          Status = "ok"
	StatusDegraded    Status = "degraded"
	StatusUnavailable Status = "unavailable"
)

// Status describes whether workload identity is currently usable.
type Status string

// Identity links a resolved backend to Kubernetes workload metadata.
// GPURequested is the declared resource request, not live utilization.
type Identity struct {
	PodName      string
	NodeName     string
	GPURequested float64
	Ready        bool
	Status       Status
	Error        string
}

// Enricher resolves workload identity for a backend snapshot.
type Enricher interface {
	Enrich(context.Context, []backend.Backend) (map[backend.ID]Identity, error)
}

// Config contains the Kubernetes adapter dependencies.
type Config struct {
	Namespace  string
	BaseURL    string
	TokenFile  string
	CAFile     string
	HTTPClient *http.Client
}
