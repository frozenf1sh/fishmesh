// Package admission 负责进程级有界请求许可，并提供不排队的过载拒绝能力。
package admission

import (
	"errors"
	"fmt"
)

var ErrCapacity = errors.New("admission capacity reached")

// Config 设置进程允许同时进入推理请求路径的最大请求数。
type Config struct {
	MaxInflight int
}

// Validate 检查准入控制器的固定容量。容量一旦用于创建 channel 就不会改变，
// 因此该约束只应在初始化阶段执行。
func (c Config) Validate() error {
	if c.MaxInflight <= 0 {
		return fmt.Errorf("admission max inflight must be positive: %d", c.MaxInflight)
	}
	return nil
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
