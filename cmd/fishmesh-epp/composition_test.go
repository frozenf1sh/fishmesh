package main

import (
	"context"
	"errors"
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/llmd"

	lldmplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
)

type fakeRunner struct {
	run func(context.Context) error
}

func (r fakeRunner) Run(ctx context.Context) error {
	return r.run(ctx)
}

func TestRunRegistersFishMeshPluginBeforeStartingRunner(t *testing.T) {
	preservePluginRegistration(t)
	delete(lldmplugin.Registry, llmd.PluginType)

	started := false
	err := run(context.Background(), func() eppRunner {
		if _, ok := lldmplugin.Registry[llmd.PluginType]; !ok {
			t.Fatal("FishMesh plugin was not registered before runner construction")
		}
		return fakeRunner{run: func(context.Context) error {
			started = true
			return nil
		}}
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !started {
		t.Fatal("runner was not started")
	}
}

func TestRunReturnsUpstreamRunnerError(t *testing.T) {
	preservePluginRegistration(t)
	want := errors.New("runner failed")
	err := run(context.Background(), func() eppRunner {
		return fakeRunner{run: func(context.Context) error { return want }}
	})
	if !errors.Is(err, want) {
		t.Fatalf("run() error = %v, want %v", err, want)
	}
}

func preservePluginRegistration(t *testing.T) {
	t.Helper()
	previous, existed := lldmplugin.Registry[llmd.PluginType]
	t.Cleanup(func() {
		if existed {
			lldmplugin.Registry[llmd.PluginType] = previous
			return
		}
		delete(lldmplugin.Registry, llmd.PluginType)
	})
}
