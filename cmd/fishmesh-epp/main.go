package main

import (
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
)

// main 只建立信号上下文并返回进程状态；协议服务生命周期由 llm-d runner 负责。
func main() {
	if err := run(ctrl.SetupSignalHandler(), newUpstreamRunner); err != nil {
		os.Exit(1)
	}
}
