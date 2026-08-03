// Package admission 负责进程级有界请求许可，并提供不排队的过载拒绝能力。
package admission

import "errors"

var ErrCapacity = errors.New("admission capacity reached")

// Config 设置进程允许同时进入推理请求路径的最大请求数。
type Config struct {
	MaxInflight int
}

// Permit 表示一个已占用的准入名额；Release 必须幂等。
type Permit interface {
	Release()
}

// Controller 非阻塞地获取准入名额，不在 Gateway 内建立隐式等待队列。
type Controller interface {
	TryAcquire() (Permit, error)
	Inflight() int
}
