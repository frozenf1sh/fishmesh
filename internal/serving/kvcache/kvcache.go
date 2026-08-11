// Package kvcache 维护逐 vLLM 实例的真实 KV block locality，并提供逐 backend 前缀命中快照。
//
// 这个包拥有事件 sequence、replay freshness、Pod UID 生命周期和索引容量。它不负责分词、
// backend 选择或 fallback；无效状态必须由调用方显式降级，不能解释成普通 cache miss。
package kvcache

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	ReasonNone                     Reason = ""
	ReasonBackendUnknown           Reason = "backend-unknown"
	ReasonModelMismatch            Reason = "model-mismatch"
	ReasonReplayNotConfirmed       Reason = "replay-not-confirmed"
	ReasonReplayHeartbeatStale     Reason = "replay-heartbeat-stale"
	ReasonSequenceGap              Reason = "sequence-gap-awaiting-replay"
	ReasonUnrecoverableSequenceGap Reason = "unrecoverable-sequence-gap"
	ReasonEventTooLarge            Reason = "event-too-large"
	ReasonEventDecodeFailed        Reason = "event-decode-failed"
	ReasonEventApplyFailed         Reason = "event-apply-failed"
	ReasonUnsupportedEvent         Reason = "unsupported-event"
	ReasonReplayCapacityExceeded   Reason = "replay-capacity-exceeded"
	ReasonLifecycleChanging        Reason = "lifecycle-changing"
	ReasonClosed                   Reason = "closed"

	CodeInvalidQuery ErrorCode = "invalid-query"
	CodeCapacity     ErrorCode = "capacity-exceeded"
	CodeIndex        ErrorCode = "index-failed"
	CodeLifecycle    ErrorCode = "lifecycle-failed"
	CodeClosed       ErrorCode = "closed"
)

// WorkloadUID 是 Kubernetes Pod UID 的协议无关表示。
// Pod IP 和 backend ID 都可能复用，只有 UID 变化能确定这是一个新缓存实例。
type WorkloadUID string

// Reason 说明某个 backend 的 KV 信号为什么不可用。
type Reason string

// ErrorCode 是整个查询或生命周期操作失败时可稳定判断的错误类别。
type ErrorCode string

// Error 保留稳定错误类别和原始错误链。
type Error struct {
	Code ErrorCode
	Err  error
}

// Error 返回适合日志记录的错误信息。
func (e *Error) Error() string {
	if e == nil {
		return "kvcache error"
	}
	if e.Err == nil {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Err.Error()
}

// Unwrap 返回原始错误，供 errors.Is/errors.As 使用。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Instance 描述一个 backend 当前对应的真实 vLLM Pod 实例和事件端点。
// PodIdentifier 必须与 `kv@<pod-identifier>@<model>` topic 中的值一致。
type Instance struct {
	Backend        backend.ID
	PodUID         WorkloadUID
	PodIdentifier  string
	Model          string
	EventsEndpoint string
	ReplayEndpoint string
}

// Validate 检查实例是否具备稳定身份和完整的实时/replay 边界。
func (i Instance) Validate() error {
	if i.Backend == "" || i.PodUID == "" || strings.TrimSpace(i.PodIdentifier) == "" || strings.TrimSpace(i.Model) == "" {
		return errors.New("backend, Pod UID, Pod identifier and model must not be empty")
	}
	if err := validateZMQEndpoint(i.EventsEndpoint); err != nil {
		return fmt.Errorf("events endpoint: %w", err)
	}
	if err := validateZMQEndpoint(i.ReplayEndpoint); err != nil {
		return fmt.Errorf("replay endpoint: %w", err)
	}
	return nil
}

// Event 是 transport 交给 kvcache owner 的一条原始、带 sequence 的 KV event batch。
// Payload 只在回调期间有效，owner 必须同步处理或自行复制。
type Event struct {
	Topic    string
	Sequence uint64
	Payload  []byte
}

// EventObservation 是一个已经同步写入本地 index 的 KV event batch 的低基数观测值。
// PublishToApply 受 vLLM publisher 与 Gateway 时钟偏差影响，不表示纯 ZMQ 网络 RTT；没有有效
// publisher timestamp 的 batch 不产生该值，调用方不得将缺失解释为零延迟。
type EventObservation struct {
	Backend        backend.ID
	Replayed       bool
	PublishToApply time.Duration
}

// EventObserver 是 kvcache 在成功 apply 后通知 delivery/metrics 边界的最小替换接口。
// 回调在事件 owner 的同步路径之外执行；实现不得阻塞、重入 index 或改变 routing 决策。
type EventObserver interface {
	ObserveKVEvent(EventObservation)
}

// EventSource 隔离 ZMQ transport，并以同步回调提供自然背压。
// Follow 持续消费直到 context 取消或连接失败；Replay 必须在收到 END 后才返回 nil。
type EventSource interface {
	Follow(context.Context, Instance, func(Event) error) error
	Replay(context.Context, Instance, uint64, func(Event) error) error
}

// Query 是一次 KV locality 查询。每个 TokenGroups 元素代表一个相互独立的 prompt。
type Query struct {
	Model       string
	CacheSalt   string
	TokenGroups [][]uint32
	Backends    []backend.ID
}

// Validate 检查查询是否包含模型、prompt 和唯一候选 backend。
func (q Query) Validate() error {
	if strings.TrimSpace(q.Model) == "" {
		return errors.New("query model must not be empty")
	}
	if len(q.TokenGroups) == 0 {
		return errors.New("query must contain at least one prompt")
	}
	if len(q.Backends) == 0 {
		return errors.New("query must contain at least one backend")
	}
	seen := make(map[backend.ID]struct{}, len(q.Backends))
	for _, candidate := range q.Backends {
		if candidate == "" {
			return errors.New("query backend must not be empty")
		}
		if _, exists := seen[candidate]; exists {
			return fmt.Errorf("query backend %q is duplicated", candidate)
		}
		seen[candidate] = struct{}{}
	}
	return nil
}

// Match 描述一个 backend 的最长完整 block 前缀。
// Valid=false 表示 KV-aware 信号未知；只有 Valid=true 且 MatchedTokens=0 才是真实 cache miss。
type Match struct {
	Backend       backend.ID
	Valid         bool
	Reason        Reason
	MatchedBlocks int
	MatchedTokens int
	TotalBlocks   int
	TotalTokens   int
	ObservedAt    time.Time
	Freshness     time.Duration
}

// Snapshot 是一次查询的不可变逐 backend 结果。
type Snapshot struct {
	lookupAt    time.Time
	totalTokens int
	totalBlocks int
	matches     map[backend.ID]Match
}

// LookupAt 返回本次查询完成时间。
func (s Snapshot) LookupAt() time.Time {
	return s.lookupAt
}

// TotalTokens 返回所有 prompt 的 token 总数。
func (s Snapshot) TotalTokens() int {
	return s.totalTokens
}

// TotalBlocks 返回所有 prompt 可用于 KV-aware lookup 的完整 block 数。
func (s Snapshot) TotalBlocks() int {
	return s.totalBlocks
}

// Matches 返回逐 backend 结果副本。
func (s Snapshot) Matches() map[backend.ID]Match {
	return maps.Clone(s.matches)
}

// InstanceState 是一个 subscriber/lifecycle owner 的只读状态。
type InstanceState struct {
	Instance         Instance
	Valid            bool
	Reason           Reason
	HasSequence      bool
	LastSequence     uint64
	LastReplayAt     time.Time
	LastEventAt      time.Time
	LastEventLag     time.Duration
	Freshness        time.Duration
	AppliedBatches   uint64
	ReplayBatches    uint64
	DuplicateBatches uint64
	LastError        string
}

// StateSnapshot 是所有已登记实例的不可变状态。
type StateSnapshot struct {
	observedAt time.Time
	closed     bool
	instances  map[backend.ID]InstanceState
}

// ObservedAt 返回状态快照时间。
func (s StateSnapshot) ObservedAt() time.Time {
	return s.observedAt
}

// Closed 返回 owner 是否已经停止并回收全部 subscriber。
func (s StateSnapshot) Closed() bool {
	return s.closed
}

// Instances 返回实例状态副本。
func (s StateSnapshot) Instances() map[backend.ID]InstanceState {
	return maps.Clone(s.instances)
}

// Index 提供 KV-aware lookup、Pod 生命周期对齐和资源关闭能力。
type Index interface {
	Lookup(context.Context, Query) (Snapshot, error)
	Reconcile(context.Context, []Instance) error
	State() StateSnapshot
	Close() error
}
