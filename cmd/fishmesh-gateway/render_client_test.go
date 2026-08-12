package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRenderAddressResolverSharesConcurrentLookup(t *testing.T) {
	var mu sync.Mutex
	lookups := 0
	resolver := &renderAddressResolver{
		host:  "renderer.kubellm.svc.cluster.local",
		ttl:   time.Minute,
		clock: time.Now,
		lookupIP: func(context.Context, string) ([]net.IP, error) {
			mu.Lock()
			lookups++
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			return []net.IP{net.ParseIP("10.43.163.254")}, nil
		},
	}

	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			address, err := resolver.cachedAddress(context.Background())
			if err != nil || address != "10.43.163.254" {
				t.Errorf("cached address = %q, error = %v", address, err)
			}
		}()
	}
	wait.Wait()

	mu.Lock()
	defer mu.Unlock()
	if lookups != 1 {
		t.Fatalf("DNS lookups = %d, want one shared lookup", lookups)
	}
}

func TestFirstRenderIPPrefersIPv4(t *testing.T) {
	ips := []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("10.43.163.254")}
	if got := firstRenderIP(ips); got != "10.43.163.254" {
		t.Fatalf("firstRenderIP() = %q", got)
	}
}
