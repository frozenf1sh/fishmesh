// Package kubernetes contains small, dependency-free Kubernetes transport
// primitives shared by runtime adapters. It deliberately does not expose
// Kubernetes resources or client-go types to the domain packages.
package kubernetes

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

// NewHTTPClient clones the caller's HTTP client and configures the projected
// ServiceAccount CA. TLS verification remains enabled; this helper never
// falls back to InsecureSkipVerify.
func NewHTTPClient(base *http.Client, caFile string) (*http.Client, error) {
	if base == nil {
		base = http.DefaultClient
	}
	caBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes CA: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("append Kubernetes CA: no PEM certificate found")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if configured, ok := base.Transport.(*http.Transport); ok {
		transport = configured.Clone()
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool} //nolint:gosec // CA verification remains enabled.
	return &http.Client{Transport: transport, Timeout: base.Timeout}, nil
}
