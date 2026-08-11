package requestpath

import (
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

func TestConfigValidateCentralizesFallbackAndFreshnessChecks(t *testing.T) {
	service := backend.Backend{ID: "service", URL: "http://service:8000"}
	if err := (Config{Service: service, RequireFreshDiscovery: true, DiscoveryMaxAge: time.Minute}).Validate(); err != nil {
		t.Fatalf("valid requestpath config rejected: %v", err)
	}
	for _, config := range []Config{
		{Service: backend.Backend{ID: "service", URL: "/relative"}},
		{Service: service, RequireFreshDiscovery: true},
	} {
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid requestpath config was accepted: %+v", config)
		}
	}
}
