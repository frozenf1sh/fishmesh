package main

import (
	"os"
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/llmd"

	"github.com/go-logr/logr"
	configloader "github.com/llm-d/llm-d-router/pkg/epp/config/loader"
	lldmplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
)

const endpointPickerConfigPath = "../../deploy/integrated/llmd-config/epp-config.yaml"

func TestEndpointPickerConfigMatchesPinnedSchemaAndPluginFactory(t *testing.T) {
	preservePluginRegistration(t)
	content, err := os.ReadFile(endpointPickerConfigPath)
	if err != nil {
		t.Fatalf("read EndpointPickerConfig: %v", err)
	}
	rawConfig, _, err := configloader.LoadRawConfig(content, logr.Discard())
	if err != nil {
		t.Fatalf("LoadRawConfig() error = %v", err)
	}

	llmd.Register()
	for _, specification := range rawConfig.Plugins {
		if specification.Type != llmd.PluginType {
			continue
		}
		factory, ok := lldmplugin.Registry[specification.Type]
		if !ok {
			t.Fatalf("plugin type %q is not registered", specification.Type)
		}
		if _, err := factory(specification.Name, lldmplugin.StrictDecoder(specification.Parameters), nil); err != nil {
			t.Fatalf("create configured FishMesh plugin: %v", err)
		}
		return
	}
	t.Fatalf("EndpointPickerConfig does not contain plugin type %q", llmd.PluginType)
}
