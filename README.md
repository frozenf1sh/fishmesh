# KubeLLM-Edge

KubeLLM-Edge 是一个研究 **Kubernetes LLM Serving 中 prefix locality-aware routing**
的实验项目。

它不替代 vLLM，也不重新实现 vLLM 的 Automatic Prefix Caching。vLLM 会在单个推理
副本内部复用 KV cache；本项目研究的是：如果把具有相同共享前缀的请求持续发送到
同一个副本，是否能改善跨 Kubernetes 部署的缓存局部性和首 Token 延迟（TTFT）。

当前已经验证的目标环境是一个通过 Tailscale 连接的双节点 K3s 集群：

- Mac 上 OrbStack 托管的 ARM64 Ubuntu VM 运行 K3s control plane；
- 一台带 RTX 4060 的 x86_64 Ubuntu 笔记本运行 GPU 工作负载；
- vLLM 0.11 使用 NVIDIA GPU time-slicing 运行两个本地 Qwen2.5-0.5B 副本；
- 两个副本和一个 OpenAI-compatible Chat Completion 请求均已验证成功。

架构、约束和验收门槛见[可行性与执行方案](docs/feasibility-and-execution-plan.md)。

第一个代码里程碑是可重复的 random-Service baseline，设计和运行手册见[基线开发
方案](docs/baseline-development-plan.md)。
