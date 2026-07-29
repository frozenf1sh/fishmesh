package config

import "testing"

func TestConfigValidateGatewayRequiresMetricsURL(t *testing.T) {
	config := Config{ListenAddress: ":8090", Mode: "gateway", RequestTimeout: 1, ShutdownTimeout: 1}
	if err := config.Validate(); err == nil {
		t.Fatal("Validate() should reject gateway mode without metrics URL")
	}
}

func TestConfigValidateObservabilityMode(t *testing.T) {
	config := Config{ListenAddress: ":8090", Mode: "observability", RequestTimeout: 1, ShutdownTimeout: 1}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestCSVValuesTrimsAndDropsEmptyItems(t *testing.T) {
	values := csvValues(" first, ,second,, ")
	if len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("values = %#v", values)
	}
}
