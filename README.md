# KubeLLM-Edge

KubeLLM-Edge 是一个面向 Kubernetes LLM Serving 的 **LLM-aware 请求调度、可观测与集群
诊断平台**。

它不替代 vLLM，也不重新实现 vLLM 的 Automatic Prefix Caching。项目首先用 Service
keep-alive 建立真实基线，再研究 Prefix Affinity、负载感知调度和多层观测如何共同影响
TTFT、尾延迟、故障恢复与集群诊断。

当前已经验证的目标环境是一个通过 Tailscale 连接的双节点 K3s 集群：

- Mac 上 OrbStack 托管的 ARM64 Ubuntu VM 运行 K3s control plane；
- 一台带 RTX 4060 的 x86_64 Ubuntu 笔记本运行 GPU 工作负载；
- vLLM 0.11 使用 NVIDIA GPU time-slicing 运行两个本地 Qwen2.5-0.5B 副本；
- 两个副本和一个 OpenAI-compatible Chat Completion 请求均已验证成功。

架构、约束和验收门槛见[设计方案](docs/design/plan.md)。

当前默认基线和下一阶段路线见[设计方案](docs/design/plan.md)；实验结果见[实验报告]
(docs/experiments/2026-08-08-llm-scheduling.md)。

当前只读 Cluster Analyst 控制面骨架见[Agent 实施说明](docs/notes/analyst.md)。默认
使用可重复 demo fixture，后续可切换到 Gateway `/metrics`，不会把 Agent 放进请求路径。
