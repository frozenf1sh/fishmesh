package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/prediction"
)

const maxStaticProfileBytes = 1 << 20

type staticProfileDocument struct {
	Model                     string                    `json:"model"`
	HardwareProfile           string                    `json:"hardware_profile"`
	VLLMVersion               string                    `json:"vllm_version"`
	MinPromptTokens           int64                     `json:"min_prompt_tokens"`
	MaxModelTokens            int64                     `json:"max_model_tokens"`
	MaxPromptTokens           int64                     `json:"max_prompt_tokens,omitempty"`
	Version                   string                    `json:"version"`
	Calibrated                bool                      `json:"calibrated"`
	PromptTokenBreakpoints    []int64                   `json:"prompt_token_breakpoints"`
	CachedRatioBreakpoints    []int64                   `json:"cached_ratio_breakpoints"`
	PrefillMilliseconds       [][]float64               `json:"prefill_ms"`
	QueueWaitMilliseconds     float64                   `json:"queue_wait_ms"`
	RunningDelayMilliseconds  float64                   `json:"running_delay_ms"`
	LocalDeltaMilliseconds    float64                   `json:"local_delta_ms"`
	LocalFallbackMilliseconds float64                   `json:"local_fallback_ms"`
	SafetyMarginMilliseconds  float64                   `json:"safety_margin_ms"`
	LoadBounds                *staticLoadBoundsDocument `json:"load_bounds,omitempty"`
}

type staticLoadBoundsDocument struct {
	MaxQueueDepth    int64 `json:"max_queue_depth"`
	MaxRunning       int64 `json:"max_running"`
	MaxLocalDelta    int64 `json:"max_local_delta"`
	MaxLocalFallback int64 `json:"max_local_fallback"`
}

func loadStaticProfile(path string) (prediction.StaticProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return prediction.StaticProfile{}, fmt.Errorf("read static profile: %w", err)
	}
	if len(data) == 0 || len(data) > maxStaticProfileBytes {
		return prediction.StaticProfile{}, fmt.Errorf("static profile must be between 1 and %d bytes", maxStaticProfileBytes)
	}
	var document staticProfileDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return prediction.StaticProfile{}, fmt.Errorf("decode static profile: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return prediction.StaticProfile{}, fmt.Errorf("decode static profile: trailing JSON content")
	}
	profile := prediction.StaticProfile{
		Identity: prediction.ProfileIdentity{
			Model: document.Model, HardwareProfile: document.HardwareProfile, VLLMVersion: document.VLLMVersion,
			MinPromptTokens: document.MinPromptTokens, MaxModelTokens: document.MaxModelTokens,
		},
		MaxPromptTokens: document.MaxPromptTokens,
		Version:         document.Version, Calibrated: document.Calibrated,
		PromptTokenBreakpoints: document.PromptTokenBreakpoints, CachedRatioBreakpoints: document.CachedRatioBreakpoints,
	}
	if document.LoadBounds != nil {
		profile.LoadBounds = &prediction.StaticLoadBounds{
			MaxQueueDepth: document.LoadBounds.MaxQueueDepth, MaxRunning: document.LoadBounds.MaxRunning,
			MaxLocalDelta: document.LoadBounds.MaxLocalDelta, MaxLocalFallback: document.LoadBounds.MaxLocalFallback,
		}
	}
	profile.Prefill = make([][]time.Duration, len(document.PrefillMilliseconds))
	for row := range document.PrefillMilliseconds {
		profile.Prefill[row] = make([]time.Duration, len(document.PrefillMilliseconds[row]))
		for column, value := range document.PrefillMilliseconds[row] {
			converted, err := milliseconds(value)
			if err != nil {
				return prediction.StaticProfile{}, fmt.Errorf("prefill_ms[%d][%d]: %w", row, column, err)
			}
			profile.Prefill[row][column] = converted
		}
	}
	for _, field := range []struct {
		value       float64
		destination *time.Duration
	}{
		{document.QueueWaitMilliseconds, &profile.QueueWaitPerRequest},
		{document.RunningDelayMilliseconds, &profile.RunningDelayPerRequest},
		{document.LocalDeltaMilliseconds, &profile.LocalDeltaPerRequest},
		{document.LocalFallbackMilliseconds, &profile.LocalFallbackPerRequest},
		{document.SafetyMarginMilliseconds, &profile.SafetyMargin},
	} {
		converted, err := milliseconds(field.value)
		if err != nil {
			return prediction.StaticProfile{}, err
		}
		*field.destination = converted
	}
	if err := profile.Validate(); err != nil {
		return prediction.StaticProfile{}, err
	}
	return profile, nil
}

func milliseconds(value float64) (time.Duration, error) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(math.MaxInt64)/float64(time.Millisecond) {
		return 0, fmt.Errorf("milliseconds must be finite, non-negative and bounded")
	}
	return time.Duration(math.Round(value * float64(time.Millisecond))), nil
}
