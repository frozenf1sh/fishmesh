# R6I-30 测试 Agent Prompt

在 `/Users/frozenf1sh/prj/fishmesh` 执行 R6I-30 三臂路由实验。不要修改产品代码、不要提交、不要删除历史
artifacts；开始时先读取 `AGENTS.md`、实验计划与 `configs/r6i30-routing-triad.json`。

目标是比较 `round-robin`、`load-aware`、`kv-aware`。只有集群实际 Ready、KV replay ready、JSONL/metrics/
Pod provenance 完整，才算一个有效 arm。

1. 只读预检：记录 kubectl context、namespace、git SHA/status、Gateway image digest、vLLM image/version、
   ConfigMap、Deployment、Pods、EndpointSlice、baseline 和可用磁盘；确认没有别的 benchmark 或 rollout。
   条件不安全则停止并报告，勿覆盖现有运行。
2. 用当前 git SHA 构建并加载一个唯一 Gateway image tag；三臂必须同一 image digest。三份 overlay 经
   `kubectl kustomize | sed` 注入 tag/digest 和每 arm 唯一 rollout annotation 后再 apply，不直接 edit
   live ConfigMap；先做 server dry-run。
3. 每 arm 都以 `maxSurge=0/maxUnavailable=1` 滚动 vLLM，再滚动 Gateway；等待 vLLM 2/2、Gateway 1/1、
   EndpointSlice 两地址和 `/readyz`。KV arm 还必须确认两个 backend replay valid。rollout/prewarm traffic
   不得进入正式 JSONL。
4. 执行两轮：`r1 RR → LA → KV`（seed 20260821），`r2 KV → LA → RR`（seed 20260822）。每 arm 使用唯一
   run nonce/vLLM generation，保存 JSONL、report、Gateway metrics window、cluster snapshots、image digest、
   Pod UID/generation 与 vLLM `/metrics` prefix-cache 证据；不得记录 prompt、API key 或原始 SSE。
5. 运行 RR/LA、LA/KV、RR/KV 三个 pairwise compare（20,000 bootstrap samples）。核对 RR policy 为
   `round-robin-v1`；完整 load observation 的 LA 为 `load-aware-v1`；KV signal 不可用为
   `kv-aware-*-fallback-*`，不能伪装成 `kv-aware-v1`。`not-requested` 不是 vLLM cache miss。
6. 若任一 arm 成功率 <99.5%、replay 未 valid、Pod/image/config/generation 不一致，标记整个 replicate 无效，
   不得 pool；留存故障证据并停止后续正式 run。结束时无论成败都声明式恢复
   `deploy/experiments/r6i22-final/load-aware`，确认 admission off、Gateway 1/1、vLLM 2/2 Ready。
7. 最终只报告证据：三对 pooled + 场景级 P50/P95/P99/CI、QPS/Little's Law、backend 分布、KV 状态和
   vLLM prefix-cache 指标；明确未通过的结论。不要把单场景或 CI 跨 0 写成整体收益。
