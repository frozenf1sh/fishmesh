# EndpointSlice 动态发现实验

这个 overlay 将 Gateway 切换到 `session-key + EndpointSlice`，只读取
`kubellm` namespace 中与 `qwen-vllm` Service 对应的 Ready 地址。它是下一阶段的
隔离实验，不会修改默认 baseline。

实验需要 Gateway ServiceAccount 的最小 `get/list/watch endpointslices` 和 namespace-scoped
Pod `get/list` 权限，并将
ServiceAccount token 挂载到 Pod。当前 baseline 使用 Flannel，未声明会生效的
NetworkPolicy；若迁移到支持策略执行的 CNI，还需要显式放行 Gateway 到 Kubernetes API
Server 的 HTTPS egress。
