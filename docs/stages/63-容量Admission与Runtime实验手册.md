# 阶段 63：容量、Admission 与 Runtime 实验手册

## 1. 交付

- 新增 `docs/experiments/2026-08-16-capacity-admission-runtime.md`，把 A0/A1/A2 admission、B0/B1/B2/B3 routing、
  capacity ladder、dynamic step、long SSE drain 和 runtime identity 校验串成可执行流程。
- 固化真实集群实验的前置检查、shadow→active 顺序、产物配对、Little’s Law 收益比较、停止条件和恢复基线。
- 明确 invalid metrics、stale runtime、counter reset、SSE 断流和 Pod 重启不能被改写为“低收益”或“零吞吐”。

## 2. 当前边界

本阶段只完成实验准备，没有 apply admission-active、没有启动新的真实 GPU 压测，也没有新增线上 QPS/TTFT
收益结论。`session-key` 继续是 frozen compatibility mode，不进入主收益矩阵。

## 3. 验证

- 已通过报告/客户端专项测试、`go test -race ./...`、`go vet ./...`、`go build ./...`、`make manifest`、
  两个 admission overlay 的 Kustomize 渲染和 `git diff --check`。
