package admission

import "sync"

var _ Controller = (*controller)(nil)
var _ TargetController = (*controller)(nil)
var _ Permit = (*permit)(nil)

type controller struct {
	permits chan struct{} // channel 容量是硬上限；只有 permit.Release 取出元素。
	mu      sync.Mutex
	target  int
}

type permit struct {
	controller *controller
	once       sync.Once
}

// New 创建一个进程内准入控制器。
func New(config Config) (TargetController, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	target := config.InitialTarget
	if target == 0 {
		target = config.MaxInflight
	}
	return &controller{permits: make(chan struct{}, config.MaxInflight), target: target}, nil
}

func (c *controller) TryAcquire() (Permit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.permits) >= c.target {
		return nil, ErrCapacity
	}
	select {
	case c.permits <- struct{}{}:
		return &permit{controller: c}, nil
	default:
		return nil, ErrCapacity
	}
}

func (c *controller) Inflight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.permits)
}

func (c *controller) Target() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

func (c *controller) MaxInflight() int { return cap(c.permits) }

func (c *controller) SetTarget(target int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if target <= 0 || target > cap(c.permits) {
		return ErrTarget
	}
	c.target = target
	return nil
}

func (p *permit) Release() {
	p.once.Do(func() {
		p.controller.mu.Lock()
		defer p.controller.mu.Unlock()
		<-p.controller.permits
	})
}
