# FishMesh llm-d 最小配置

这里保存 R5C 的 **配置契约**，还不是可直接部署的完整 Gateway/EPP 栈。

`fishmesh-epp` 复用 llm-d v0.9.0 的 EPP runner、ext_proc 协议、Endpoint
发现、in-flight 生命周期和 max-score picker，只注册一个 FishMesh
`bounded-affinity` Filter/Scorer。llm-d 会根据插件声明的 required data key 自动创建
默认 `inflight-load-producer`，所以配置中不再重复声明该生产者。

当前 profile 只有一个 scorer，FishMesh 返回 `1`（选中）或 `0`（未选中）。这保证
`inflightDelta` 和 `queueDepthDelta` 仍是硬边界，不会被其他加权 scorer 稀释。

根据 ADR-002，Standard mode 继续保留，但完整部署顺序调整到 R6E。R6B 会先扩展薄 adapter，
让 FishMesh scorer 消费 llm-d precise prefix producer 提供的 `PrefixCacheMatchInfo`；adapter 不会
启动 Lite mode 的 KV index 或 requestpath。

R6E 将补全并验证以下资源：

1. Gateway API Inference Extension v1.5.0 CRD；
2. Envoy Gateway/Gateway、InferencePool 与 EPP Deployment/Service/RBAC；
3. ConfigMap 挂载和 `--config-file` 参数；
4. llm-d token producer、precise prefix producer 与 FishMesh scorer 配置；
5. 空候选、坏指标、endpoint churn、retry served endpoint 和 EPP failover 的真实集群 smoke。

当前配置仍然只代表 R5C bounded-affinity 契约，不能描述为 exact KV cache-aware。

渲染检查：

```bash
kubectl kustomize deploy/integrated/llmd-config
```
