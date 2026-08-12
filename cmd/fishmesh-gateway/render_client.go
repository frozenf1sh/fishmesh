package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/tokenization"
)

const (
	// Render 不需要跟随数据面的连接上限；小连接池可以避免 KV-aware 冷启动
	// 突发同时制造大量 DNS 查询和 TCP 建连。
	renderMaxConnsPerHost = 16
	renderDNSCacheTTL     = 30 * time.Second
	renderIdleConnTimeout = 90 * time.Second
)

// newRenderHTTPClient 创建专用的 vLLM Render 客户端。数据面 transport 按 backend
// 管理，而 Render 面向单个 Service，因此单独拥有有界连接池和 DNS 缓存。
func newRenderHTTPClient(config tokenization.Config) (*http.Client, error) {
	endpoint, err := url.Parse(config.BaseURL)
	if err != nil || endpoint.Hostname() == "" {
		return nil, fmt.Errorf("parse Render endpoint %q", config.BaseURL)
	}
	resolver := &renderAddressResolver{
		host:     endpoint.Hostname(),
		ttl:      renderDNSCacheTTL,
		clock:    time.Now,
		lookupIP: lookupRenderIPs,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          renderMaxConnsPerHost,
		MaxIdleConnsPerHost:   renderMaxConnsPerHost,
		MaxConnsPerHost:       renderMaxConnsPerHost,
		IdleConnTimeout:       renderIdleConnTimeout,
		ResponseHeaderTimeout: config.Timeout,
		DialContext:           resolver.DialContext,
	}
	return &http.Client{Transport: transport, Timeout: config.Timeout}, nil
}

type renderAddressResolver struct {
	host     string
	ttl      time.Duration
	clock    func() time.Time
	lookupIP func(context.Context, string) ([]net.IP, error)
	dialer   net.Dialer

	mu          sync.Mutex
	address     string
	expiresAt   time.Time
	resolving   bool
	resolveDone chan struct{}
}

func (r *renderAddressResolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || r.host == "" || host != r.host || net.ParseIP(host) != nil {
		return r.dialer.DialContext(ctx, network, address)
	}
	resolved, err := r.cachedAddress(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve Render host %q: %w", host, err)
	}
	connection, err := r.dialer.DialContext(ctx, network, net.JoinHostPort(resolved, port))
	if err != nil {
		r.invalidate(resolved)
	}
	return connection, err
}

func (r *renderAddressResolver) cachedAddress(ctx context.Context) (string, error) {
	for {
		now := r.clock()
		r.mu.Lock()
		if r.address != "" && now.Before(r.expiresAt) {
			address := r.address
			r.mu.Unlock()
			return address, nil
		}
		if r.resolving {
			done := r.resolveDone
			r.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		r.resolving = true
		r.resolveDone = make(chan struct{})
		done := r.resolveDone
		r.mu.Unlock()

		ips, err := r.lookupIP(ctx, r.host)
		resolved := firstRenderIP(ips)
		r.mu.Lock()
		if err == nil && resolved != "" {
			r.address = resolved
			r.expiresAt = r.clock().Add(r.ttl)
		}
		r.resolving = false
		close(done)
		r.mu.Unlock()
		if err != nil {
			return "", err
		}
		if resolved == "" {
			return "", fmt.Errorf("Render host has no address")
		}
		return resolved, nil
	}
}

func (r *renderAddressResolver) invalidate(address string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.address == address {
		r.address = ""
		r.expiresAt = time.Time{}
	}
}

func lookupRenderIPs(ctx context.Context, host string) ([]net.IP, error) {
	// Kubernetes 通常注入 ndots:5。Render Service 已经是完整集群域名，必须强制
	// 使用绝对查询，避免先为每个 search suffix 重试，再向 CoreDNS 查询真实地址。
	host = strings.TrimSuffix(host, ".") + "."
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func firstRenderIP(ips []net.IP) string {
	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String()
		}
	}
	if len(ips) > 0 {
		return ips[0].String()
	}
	return ""
}
