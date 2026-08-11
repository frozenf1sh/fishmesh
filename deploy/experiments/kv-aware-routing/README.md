# R6B-6 KV-aware routing 验收 overlay

此 overlay 显式开启 vLLM KVEvents/replay，并将 Gateway 切到 `kv-aware`。它只授予 gateway
读取 namespace 内 EndpointSlice 的权限；Pod UID 已由 targetRef 直接发布，不需要 Pod list/watch。
完成验证后必须恢复 `deploy/inference` 与 `deploy/baseline/base`。
