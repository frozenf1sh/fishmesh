// Package backend owns the stable identity and address of one inference backend.
package backend

import (
	"fmt"
	"net/url"
)

const (
	// MetadataPodName is the discovery-to-identity contract for a Kubernetes
	// Pod targetRef. Live Pod state is deliberately not stored in Metadata.
	MetadataPodName = "pod_name"
)

// ID is the stable identity used by routing state, metrics, and lifecycle
// reconciliation. It is intentionally separate from URL formatting.
type ID string

// Backend is an immutable routing candidate after publication in a snapshot.
// Metadata contains stable discovery facts; live load belongs to observation.
type Backend struct {
	ID       ID
	URL      string
	Metadata map[string]string
}

// Validate 检查一个 backend 是否具备可用于请求转发的稳定身份和 HTTP 地址。
func (b Backend) Validate() error {
	if b.ID == "" {
		return fmt.Errorf("backend ID must not be empty")
	}
	parsed, err := url.Parse(b.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("backend URL must be an absolute HTTP URL: %q", b.URL)
	}
	return nil
}
