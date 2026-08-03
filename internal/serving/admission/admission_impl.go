package admission

import (
	"fmt"
	"sync"
)

var _ Controller = (*controller)(nil)
var _ Permit = (*permit)(nil)

type controller struct {
	permits chan struct{} // channel 容量是硬上限；只有 permit.Release 取出元素。
}

type permit struct {
	controller *controller
	once       sync.Once
}

// New 创建一个进程内准入控制器。
func New(config Config) (Controller, error) {
	if config.MaxInflight <= 0 {
		return nil, fmt.Errorf("admission max inflight must be positive: %d", config.MaxInflight)
	}
	return &controller{permits: make(chan struct{}, config.MaxInflight)}, nil
}

func (c *controller) TryAcquire() (Permit, error) {
	select {
	case c.permits <- struct{}{}:
		return &permit{controller: c}, nil
	default:
		return nil, ErrCapacity
	}
}

func (c *controller) Inflight() int { return len(c.permits) }

func (p *permit) Release() {
	p.once.Do(func() { <-p.controller.permits })
}
