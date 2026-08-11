// Package transport owns upstream HTTP client lifecycle. Routing selects a
// backend; transport decides how that backend's connections are reused.
package transport

import (
	"fmt"
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
	IdleConnTimeout time.Duration
}

// Validate 检查连接池创建后不可变的请求边界。Transport.New 保持兼容的无 error API，
// 由组合根在创建连接池前调用本方法完成初始化校验。
func (c Config) Validate() error {
	if c.RequestTimeout <= 0 || c.MaxConnsPerHost <= 0 || c.IdleConnTimeout <= 0 {
		return fmt.Errorf("transport request timeout, max connections and idle connection timeout must be positive")
	}
	return nil
}
