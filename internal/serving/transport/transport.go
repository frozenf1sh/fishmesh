// Package transport owns upstream HTTP client lifecycle. Routing selects a
// backend; transport decides how that backend's connections are reused.
package transport

import (
	"net/http"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// Pool owns endpoint-scoped HTTP clients and their idle connections.
type Pool interface {
	ClientFor(backend.Backend) *http.Client
	Remove(backend.ID) bool
	Len() int
	Close()
}

// Config controls connection reuse and request bounds.
type Config struct {
	KeepAlive       bool
	RequestTimeout  time.Duration
	MaxConnsPerHost int
}
