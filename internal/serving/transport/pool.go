// Package transport owns upstream HTTP client lifecycle. Routing decides which
// backend to use; this package decides how that backend's connection is reused.
package transport

import (
	"net/http"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

type Config struct {
	KeepAlive       bool
	RequestTimeout  time.Duration
	MaxConnsPerHost int
}

type Pool struct {
	config  Config
	mu      sync.Mutex
	clients map[string]*http.Client
}

func NewPool(config Config) *Pool {
	return &Pool{config: config, clients: make(map[string]*http.Client)}
}

func (p *Pool) ClientFor(backend routing.Backend) *http.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[backend.ID]; ok {
		return client
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DisableKeepAlives:     !p.config.KeepAlive,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          p.config.MaxConnsPerHost,
		MaxIdleConnsPerHost:   p.config.MaxConnsPerHost,
		MaxConnsPerHost:       p.config.MaxConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: p.config.RequestTimeout,
	}, Timeout: p.config.RequestTimeout}
	p.clients[backend.ID] = client
	return client
}

// Remove closes idle connections and forgets one endpoint-scoped client.
// The gateway calls it only after the endpoint is absent and its in-flight
// counter reaches zero.
func (p *Pool) Remove(backendID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	client, ok := p.clients[backendID]
	if !ok {
		return false
	}
	client.CloseIdleConnections()
	delete(p.clients, backendID)
	return true
}

func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
}

// Close releases idle sockets owned by every backend client. The gateway calls
// this during graceful shutdown so a rolling update does not retain transport
// resources beyond the process lifetime.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, client := range p.clients {
		client.CloseIdleConnections()
	}
}
