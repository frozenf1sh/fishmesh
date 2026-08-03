// Package simulator 提供可确定控制的 OpenAI-compatible backend，用于无 GPU 的故障与生命周期验证。
package simulator

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultEvents = 1
)

// Behavior 描述一个 backend 如何响应后续请求；更新不会改变已经开始的请求。
type Behavior struct {
	StatusCode       int
	FirstEventDelay  time.Duration
	EventInterval    time.Duration
	Events           int
	AbortAfterEvents int
	Hold             bool // 写出首个 SSE 事件后保持流打开，直到 client cancellation。
	QueueDepth       float64
	RunningRequests  float64
}

// State 是控制面读取的不可变运行快照。
type State struct {
	Behavior      Behavior
	Requests      int64
	Active        int64
	Cancellations int64
	ForcedErrors  int64
	StreamAborts  int64
}

// Backend 持有可变行为和有界计数器；它不创建监听端口，也不拥有进程生命周期。
type Backend struct {
	mu       sync.RWMutex // 只保护 behavior；请求计数使用 atomic，避免控制请求阻塞数据面。
	behavior Behavior

	requests      atomic.Int64
	active        atomic.Int64
	cancellations atomic.Int64
	forcedErrors  atomic.Int64
	streamAborts  atomic.Int64
}

// New 创建一个可嵌入 httptest、独立进程或容器的受控 backend。
func New(behavior Behavior) (*Backend, error) {
	behavior = behavior.withDefaults()
	if err := behavior.Validate(); err != nil {
		return nil, err
	}
	return &Backend{behavior: behavior}, nil
}

// Validate 拒绝不确定或无法解释的故障配置。
func (b Behavior) Validate() error {
	if b.StatusCode != http.StatusOK && (b.StatusCode < http.StatusBadRequest || b.StatusCode > 599) {
		return fmt.Errorf("simulator status code must be 200 or between 400 and 599: %d", b.StatusCode)
	}
	if b.FirstEventDelay < 0 || b.EventInterval < 0 {
		return fmt.Errorf("simulator delays must not be negative")
	}
	if b.Events <= 0 {
		return fmt.Errorf("simulator events must be positive: %d", b.Events)
	}
	if b.AbortAfterEvents < 0 || b.AbortAfterEvents > b.Events {
		return fmt.Errorf("abort-after-events must be between 0 and events")
	}
	if b.QueueDepth < 0 || b.RunningRequests < 0 {
		return fmt.Errorf("simulator observation values must not be negative")
	}
	return nil
}

// SetBehavior 原子替换后续请求使用的行为。
func (b *Backend) SetBehavior(behavior Behavior) error {
	behavior = behavior.withDefaults()
	if err := behavior.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	b.behavior = behavior
	b.mu.Unlock()
	return nil
}

// Snapshot 返回行为和计数器在当前时刻的副本。
func (b *Backend) Snapshot() State {
	b.mu.RLock()
	behavior := b.behavior
	b.mu.RUnlock()
	return State{
		Behavior: behavior, Requests: b.requests.Load(), Active: b.active.Load(),
		Cancellations: b.cancellations.Load(), ForcedErrors: b.forcedErrors.Load(), StreamAborts: b.streamAborts.Load(),
	}
}

func (b Behavior) withDefaults() Behavior {
	if b.StatusCode == 0 {
		b.StatusCode = http.StatusOK
	}
	if b.Events == 0 {
		b.Events = defaultEvents
	}
	return b
}
