package main

import (
	"context"

	"github.com/frozenf1sh/fishmesh/internal/serving/llmd"

	upstreamrunner "github.com/llm-d/llm-d-router/cmd/epp/runner"
)

const executableName = "FishMesh EPP"

type eppRunner interface {
	Run(context.Context) error
}

type runnerFactory func() eppRunner

// run 是 FishMesh 与上游 llm-d 的唯一组合边界。
func run(ctx context.Context, factory runnerFactory) error {
	// 1. 先注册 FishMesh 插件，保证 llm-d 解析配置时能找到它。
	llmd.Register()

	// 2. 生命周期、ext_proc 协议和内置插件继续由上游 runner 管理。
	return factory().Run(ctx)
}

func newUpstreamRunner() eppRunner {
	return upstreamrunner.NewRunner().WithExecutableName(executableName)
}
