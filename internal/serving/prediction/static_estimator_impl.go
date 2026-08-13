package prediction

import (
	"math"
	"sort"
	"time"
)

// StaticEstimator 是校准 profile 的纯值估算器；构造后不持有可变样本。
type StaticEstimator struct {
	profile StaticProfile
}

// NewStaticEstimator 在进程启动期校验完整 profile。
func NewStaticEstimator(profile StaticProfile) (*StaticEstimator, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if profile.MaxPromptTokens == 0 {
		profile.MaxPromptTokens = profile.Identity.MaxModelTokens
	}
	return &StaticEstimator{profile: profile}, nil
}

// Identity 返回构造时固定的 profile identity，供 requestpath 构造同一版本的不可变输入。
func (e *StaticEstimator) Identity() ProfileIdentity {
	return e.profile.Identity
}

// Estimate 计算 prefill、queue/running 或 local fallback 与 safety margin 的总和。
func (e *StaticEstimator) Estimate(input StaticInput) StaticEstimate {
	result := StaticEstimate{Version: e.profile.Version}
	if input.Identity != e.profile.Identity {
		result.Reason = StaticReasonProfileMismatch
		return result
	}
	if input.Prompt.PromptTokens <= 0 || input.Prompt.CachedPrefixTokens < 0 || input.Prompt.CachedPrefixTokens > input.Prompt.PromptTokens ||
		input.Load.QueueDepth < 0 || input.Load.Running < 0 || input.Load.LocalDelta < 0 || input.Load.LocalInflight < 0 {
		result.Reason = StaticReasonInvalidInput
		return result
	}
	if input.Prompt.PromptTokens < e.profile.Identity.MinPromptTokens || input.Prompt.PromptTokens > e.profile.MaxPromptTokens {
		result.Reason = StaticReasonOutOfRange
		return result
	}
	if e.outsideLoadBounds(input.Load) {
		result.Reason = StaticReasonOutOfRange
		return result
	}

	cacheRatio := input.Prompt.CachedPrefixTokens * cacheRatioBasisPoints / input.Prompt.PromptTokens
	prefill := interpolateGrid(
		e.profile.PromptTokenBreakpoints,
		e.profile.CachedRatioBreakpoints,
		e.profile.Prefill,
		input.Prompt.PromptTokens,
		cacheRatio,
	)
	total, ok := addDuration(prefill, e.profile.SafetyMargin)
	if !ok {
		result.Reason = StaticReasonOverflow
		return result
	}

	result.Reason = StaticReasonCalibrated
	result.Confidence = StaticConfidenceCalibrated
	if !e.profile.Calibrated {
		result.Reason = StaticReasonUncalibrated
		result.Confidence = StaticConfidenceUncalibrated
	}
	if input.Load.Valid {
		queue, queueOK := multiplyDuration(e.profile.QueueWaitPerRequest, input.Load.QueueDepth)
		running, runningOK := multiplyDuration(e.profile.RunningDelayPerRequest, input.Load.Running)
		localDelta, localDeltaOK := multiplyDuration(e.profile.LocalDeltaPerRequest, input.Load.LocalDelta)
		if !queueOK || !runningOK || !localDeltaOK {
			result.Reason = StaticReasonOverflow
			return result
		}
		total, ok = addDuration(total, queue)
		if ok {
			total, ok = addDuration(total, running)
		}
		if ok {
			total, ok = addDuration(total, localDelta)
		}
	} else {
		local, localOK := multiplyDuration(e.profile.LocalFallbackPerRequest, input.Load.LocalInflight)
		if !localOK {
			result.Reason = StaticReasonOverflow
			return result
		}
		total, ok = addDuration(total, local)
		if e.profile.Calibrated {
			result.Reason = StaticReasonLoadFallback
			result.Confidence = StaticConfidenceDegraded
		}
	}
	if !ok || total <= 0 {
		result.Reason = StaticReasonOverflow
		result.Confidence = ""
		return result
	}
	result.TTFT = total
	result.Valid = true
	return result
}

func (e *StaticEstimator) outsideLoadBounds(load LoadWork) bool {
	if e.profile.LoadBounds == nil {
		return false
	}
	if load.Valid {
		return load.QueueDepth > e.profile.LoadBounds.MaxQueueDepth || load.Running > e.profile.LoadBounds.MaxRunning || load.LocalDelta > e.profile.LoadBounds.MaxLocalDelta
	}
	return load.LocalInflight > e.profile.LoadBounds.MaxLocalFallback
}

func interpolateGrid(xBreakpoints, yBreakpoints []int64, grid [][]time.Duration, x, y int64) time.Duration {
	xLow, xHigh := bracket(xBreakpoints, x)
	yLow, yHigh := bracket(yBreakpoints, y)
	left := interpolateDuration(grid[xLow][yLow], grid[xHigh][yLow], xBreakpoints[xLow], xBreakpoints[xHigh], x)
	right := interpolateDuration(grid[xLow][yHigh], grid[xHigh][yHigh], xBreakpoints[xLow], xBreakpoints[xHigh], x)
	return interpolateDuration(left, right, yBreakpoints[yLow], yBreakpoints[yHigh], y)
}

func bracket(values []int64, target int64) (int, int) {
	if target <= values[0] {
		return 0, 0
	}
	last := len(values) - 1
	if target >= values[last] {
		return last, last
	}
	high := sort.Search(len(values), func(index int) bool { return values[index] >= target })
	return high - 1, high
}

func interpolateDuration(low, high time.Duration, lowAt, highAt, target int64) time.Duration {
	if lowAt == highAt || low == high {
		return low
	}
	ratio := float64(target-lowAt) / float64(highAt-lowAt)
	return time.Duration(math.Round(float64(low) + ratio*float64(high-low)))
}

func multiplyDuration(value time.Duration, count int64) (time.Duration, bool) {
	if value == 0 || count == 0 {
		return 0, true
	}
	if count > int64(math.MaxInt64)/int64(value) {
		return 0, false
	}
	return value * time.Duration(count), true
}

func addDuration(left, right time.Duration) (time.Duration, bool) {
	if left > time.Duration(math.MaxInt64)-right {
		return 0, false
	}
	return left + right, true
}
