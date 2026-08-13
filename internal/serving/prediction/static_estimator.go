package prediction

import (
	"fmt"
	"time"
)

const (
	cacheRatioBasisPoints = int64(10_000)

	StaticConfidenceCalibrated   StaticConfidence = "calibrated"
	StaticConfidenceDegraded     StaticConfidence = "degraded"
	StaticConfidenceUncalibrated StaticConfidence = "uncalibrated"

	StaticReasonCalibrated      StaticReason = "calibrated"
	StaticReasonUncalibrated    StaticReason = "uncalibrated"
	StaticReasonLoadFallback    StaticReason = "load-fallback"
	StaticReasonInvalidInput    StaticReason = "invalid-input"
	StaticReasonProfileMismatch StaticReason = "profile-mismatch"
	StaticReasonOutOfRange      StaticReason = "out-of-range"
	StaticReasonOverflow        StaticReason = "overflow"
)

// StaticConfidence 表示 calibrated-static 估算是否可用于正式选择。
type StaticConfidence string

// StaticReason 解释一次估算的证据质量或失败边界。
type StaticReason string

// ProfileIdentity 防止模型、硬件或 vLLM 版本变化后静默复用旧 profile。
type ProfileIdentity struct {
	Model           string
	HardwareProfile string
	VLLMVersion     string
	MinPromptTokens int64
	MaxModelTokens  int64
}

// PromptWork 是不含 prompt 内容和 token IDs 的请求形状。
type PromptWork struct {
	PromptTokens       int64
	CachedPrefixTokens int64
}

// LoadWork 区分完整的外部 queue/running 与单 Gateway local fallback。
type LoadWork struct {
	QueueDepth    int64
	Running       int64
	LocalDelta    int64
	LocalInflight int64
	Valid         bool
}

// StaticLoadBounds prevents a calibrated profile from silently extrapolating to unmeasured pressure.
type StaticLoadBounds struct {
	MaxQueueDepth    int64
	MaxRunning       int64
	MaxLocalDelta    int64
	MaxLocalFallback int64
}

// StaticInput 是一次纯估算所需的完整不可变输入。
type StaticInput struct {
	Identity ProfileIdentity
	Prompt   PromptWork
	Load     LoadWork
}

// StaticEstimate 是 requestpath 后续可投影给 routing 的稳定值对象。
type StaticEstimate struct {
	TTFT       time.Duration
	Valid      bool
	Confidence StaticConfidence
	Version    string
	Reason     StaticReason
}

// StaticProfile 使用 prompt token × cached-prefix ratio 的二维单调网格描述 prefill。
// CachedRatioBreakpoints 的单位是 basis point，0/10000 分别表示无缓存/完整缓存。
type StaticProfile struct {
	Identity                ProfileIdentity
	MaxPromptTokens         int64
	Version                 string
	Calibrated              bool
	PromptTokenBreakpoints  []int64
	CachedRatioBreakpoints  []int64
	Prefill                 [][]time.Duration
	QueueWaitPerRequest     time.Duration
	RunningDelayPerRequest  time.Duration
	LocalDeltaPerRequest    time.Duration
	LocalFallbackPerRequest time.Duration
	SafetyMargin            time.Duration
	LoadBounds              *StaticLoadBounds
}

// Validate 拒绝不可解释、越界或破坏单调性的 profile。
func (p StaticProfile) Validate() error {
	if err := p.Identity.Validate(); err != nil {
		return err
	}
	if p.Version == "" {
		return fmt.Errorf("static profile version must not be empty")
	}
	if len(p.PromptTokenBreakpoints) == 0 || len(p.CachedRatioBreakpoints) < 2 {
		return fmt.Errorf("static profile requires prompt and cache-ratio breakpoints")
	}
	if p.CachedRatioBreakpoints[0] != 0 || p.CachedRatioBreakpoints[len(p.CachedRatioBreakpoints)-1] != cacheRatioBasisPoints {
		return fmt.Errorf("static profile cache-ratio breakpoints must cover 0 through 10000")
	}
	maxPromptTokens := p.MaxPromptTokens
	if maxPromptTokens == 0 {
		maxPromptTokens = p.Identity.MaxModelTokens
	}
	if maxPromptTokens < p.Identity.MinPromptTokens || maxPromptTokens > p.Identity.MaxModelTokens {
		return fmt.Errorf("static profile prompt range must fit within max model tokens")
	}
	if err := validateIncreasing(p.PromptTokenBreakpoints, 1, maxPromptTokens); err != nil {
		return fmt.Errorf("prompt breakpoints: %w", err)
	}
	if p.PromptTokenBreakpoints[0] != p.Identity.MinPromptTokens || p.PromptTokenBreakpoints[len(p.PromptTokenBreakpoints)-1] != maxPromptTokens {
		return fmt.Errorf("static profile prompt breakpoints must cover the declared prompt range")
	}
	if err := validateIncreasing(p.CachedRatioBreakpoints, 0, cacheRatioBasisPoints); err != nil {
		return fmt.Errorf("cache-ratio breakpoints: %w", err)
	}
	if p.QueueWaitPerRequest < 0 || p.RunningDelayPerRequest < 0 || p.LocalDeltaPerRequest < 0 || p.LocalFallbackPerRequest < 0 || p.SafetyMargin < 0 {
		return fmt.Errorf("static profile latency coefficients must not be negative")
	}
	if p.LoadBounds != nil && (p.LoadBounds.MaxQueueDepth < 0 || p.LoadBounds.MaxRunning < 0 || p.LoadBounds.MaxLocalDelta < 0 || p.LoadBounds.MaxLocalFallback < 0) {
		return fmt.Errorf("static profile load bounds must not be negative")
	}
	if len(p.Prefill) != len(p.PromptTokenBreakpoints) {
		return fmt.Errorf("static profile prefill rows must match prompt breakpoints")
	}
	for row := range p.Prefill {
		if len(p.Prefill[row]) != len(p.CachedRatioBreakpoints) {
			return fmt.Errorf("static profile prefill columns must match cache-ratio breakpoints")
		}
		for column, value := range p.Prefill[row] {
			if value < 0 {
				return fmt.Errorf("static profile prefill values must not be negative")
			}
			if column > 0 && value > p.Prefill[row][column-1] {
				return fmt.Errorf("static profile prefill must not increase as cache ratio grows")
			}
			if row > 0 && value < p.Prefill[row-1][column] {
				return fmt.Errorf("static profile prefill must not decrease as prompt grows")
			}
		}
	}
	return nil
}

// Validate 检查 profile 的适用身份。
func (i ProfileIdentity) Validate() error {
	if i.Model == "" || i.HardwareProfile == "" || i.VLLMVersion == "" || i.MinPromptTokens <= 0 || i.MaxModelTokens < i.MinPromptTokens || i.MaxModelTokens > int64(^uint64(0)>>1)/cacheRatioBasisPoints {
		return fmt.Errorf("static profile identity and prompt token range must be configured")
	}
	return nil
}

func validateIncreasing(values []int64, minimum, maximum int64) error {
	for index, value := range values {
		if value < minimum || value > maximum {
			return fmt.Errorf("value %d is outside [%d,%d]", value, minimum, maximum)
		}
		if index > 0 && value <= values[index-1] {
			return fmt.Errorf("values must be strictly increasing")
		}
	}
	return nil
}
