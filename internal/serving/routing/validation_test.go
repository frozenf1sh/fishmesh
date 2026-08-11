package routing

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

// TestConfigValidateCentralizesStrategyConstraints 用表驱动的方式验证：
// 所有非法配置（未知模式、坏 service、坏 session-key、负 penalty）
// 都必须被集中的 Config.Validate 拒绝，而不是散落在各构造函数里漏检。
func TestConfigValidateCentralizesStrategyConstraints(t *testing.T) {
	service := backend.Backend{ID: "service", URL: "http://service:8000"}
	tests := []struct {
		name   string
		config Config
	}{
		{name: "unsupported mode", config: Config{Mode: "unknown", Service: service}},
		{name: "invalid service", config: Config{Mode: ModeLoadBalanced, Service: backend.Backend{ID: "service", URL: "/relative"}}},
		{name: "invalid session-key", config: Config{Mode: ModeSessionKey, Service: service, SessionKey: SessionKeyConfig{TTL: time.Second}}},
		{name: "invalid KV-aware penalty", config: Config{Mode: ModeKVAware, Service: service, KVAware: KVAwareConfig{QueueTokenPenalty: -1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err == nil {
				t.Fatal("invalid routing configuration was accepted")
			}
		})
	}
}

func TestModeValidateAcceptsTheThreeRoutingModes(t *testing.T) {
	for _, mode := range []Mode{ModeLoadBalanced, ModeSessionKey, ModeKVAware} {
		if err := mode.Validate(); err != nil {
			t.Fatalf("mode %q was rejected: %v", mode, err)
		}
	}
}

// TestDecisionValidateRejectsInvalidBackend 验证 Decision.Validate 能挡住
// 携带不完整 backend 的决策——requestpath 在结算前依赖这道防线。
func TestDecisionValidateRejectsInvalidBackend(t *testing.T) {
	err := (Decision{Backend: backend.Backend{ID: "backend", URL: "/relative"}}).Validate()
	if err == nil {
		t.Fatal("invalid decision backend was accepted")
	}
}
