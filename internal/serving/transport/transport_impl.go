package transport

import (
	"net/http"
	"sync"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

var _ Pool = (*pool)(nil)

type pool struct {
	config  Config
	mu      sync.Mutex
	clients map[backend.ID]*http.Client
}

// New constructs an endpoint-scoped HTTP client pool.
func New(config Config) Pool {
	return &pool{config: config, clients: make(map[backend.ID]*http.Client)}
}

func (p *pool) ClientFor(candidate backend.Backend) *http.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[candidate.ID]; ok {
		return client
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DisableKeepAlives:     !p.config.KeepAlive,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          p.config.MaxConnsPerHost,
		MaxIdleConnsPerHost:   p.config.MaxConnsPerHost,
		MaxConnsPerHost:       p.config.MaxConnsPerHost,
		IdleConnTimeout:       p.config.IdleConnTimeout,
		ResponseHeaderTimeout: p.config.RequestTimeout,
	}, Timeout: p.config.RequestTimeout}
	p.clients[candidate.ID] = client
	return client
}

func (p *pool) Remove(backendID backend.ID) bool {
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

func (p *pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
}

func (p *pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, client := range p.clients {
		client.CloseIdleConnections()
	}
}
