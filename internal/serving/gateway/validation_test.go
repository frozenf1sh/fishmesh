package gateway

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

func TestConfigValidateRejectsInvalidStartupParameters(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "unsupported routing mode", config: Config{RoutingMode: "unknown", RequestTimeout: time.Second, MaxRequestBodyBytes: 1}},
		{name: "non-positive timeout", config: Config{RequestTimeout: 0, MaxRequestBodyBytes: 1}},
		{name: "non-positive body limit", config: Config{RequestTimeout: time.Second, MaxRequestBodyBytes: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err == nil {
				t.Fatal("invalid startup configuration was accepted")
			}
		})
	}
}

func TestConfigValidateAcceptsCompatibilityDefaultMode(t *testing.T) {
	if err := (Config{RoutingMode: routing.Mode(""), RequestTimeout: time.Second, MaxRequestBodyBytes: 1}).Validate(); err != nil {
		t.Fatalf("empty routing mode should use the load-balanced compatibility default: %v", err)
	}
}
