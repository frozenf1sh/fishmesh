// Package backend owns the stable identity and address of one inference backend.
package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"net/url"
)

const (
	// MetadataPodName is the discovery-to-identity contract for a Kubernetes
	// Pod targetRef. Live Pod state is deliberately not stored in Metadata.
	MetadataPodName = "pod_name"

	httpScheme       = "http"
	endpointIDPrefix = "endpoint-"
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

// NewHTTP 根据已经校验的 endpoint 地址构造跨 discovery adapter 一致的 backend 身份。
// metadata 会被复制，调用方后续修改原 map 不会改变已发布的 Backend。
func NewHTTP(address string, port int, metadata map[string]string) Backend {
	host := net.JoinHostPort(address, fmt.Sprintf("%d", port))
	sum := sha256.Sum256([]byte(host))
	return Backend{
		ID:       ID(endpointIDPrefix + hex.EncodeToString(sum[:6])),
		URL:      httpScheme + "://" + host,
		Metadata: maps.Clone(metadata),
	}
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
