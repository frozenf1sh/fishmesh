package transport

import (
	"net/http"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestPoolBoundsConnectionsAndRemovesEndpoint(t *testing.T) {
	pool := New(Config{KeepAlive: true, RequestTimeout: time.Second, MaxConnsPerHost: 7})
	client := pool.ClientFor(backend.Backend{ID: "a"})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.MaxConnsPerHost != 7 || transport.MaxIdleConnsPerHost != 7 {
		t.Fatalf("connection bounds = max:%d idle:%d", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
	if !pool.Remove("a") || pool.Len() != 0 || pool.Remove("a") {
		t.Fatalf("endpoint client was not reclaimed: len=%d", pool.Len())
	}
}
