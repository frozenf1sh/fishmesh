# 阶段 34：R6H-2 预测 TTFT 影子契约

> 状态：本地实现完成，集群验证后置。GPU 节点关闭期间未访问 Kubernetes、未构建镜像，也未运行 GPU workload。

## 1. 目标

R6H-1 的 `exact-cache-load-v2` 仍使用显式 token-equivalent penalty。R6H-2 不把另一组固定权重
放进实际路由，而是先以真实请求的首个 SSE 事件训练逐 backend 的预测模型。它只回答“若只按预计
TTFT，当前会选谁”，不改变本次已经完成的 exact 决策。

## 2. 契约与不变量

1. 新 `internal/serving/prediction` 是独立纯观测域，只依赖 `backend.ID` 和标准库；不 import
   `routing`、`kvcache`、`tokenization`、HTTP、Prometheus 或 Kubernetes。`routing` 完全不 import
   prediction，实际选择仍由既有 `routing.Strategy` 固定。
2. 每个 backend 保存至多 128 个、最多 15 分钟的样本。样本只包含
   `(uncached tokens, queue depth, running requests, local inflight, TTFT, timestamp)`；不保存 prompt、
   Token IDs、routing key、session、上游地址或 SSE 内容。EndpointSlice membership 移除 backend 时同步回收。
3. 估计使用每 backend 的非负 ridge 最小二乘：
   `T_hat = beta0 + betaU*U + betaQ*Q + betaR*R + betaL*L`，所有 beta 以 ms 为单位且投影到非负。
   这是从历史 TTFT 拟合出的测量模型，不是路由固定权重。每个候选至少 16 个未过期样本、并且其
   queue/running 均为已知时才可用；unknown/stale 不能当作零负载或零 TTFT。
4. Gateway 仅在第一个非终止 SSE 事件到达时调用 `Lease.ObserveFirstToken`。Ticket 幂等，因此重复事件
   不会重复写样本；没有 SSE 首事件、取消或上游失败也不会伪造 TTFT 样本。
5. 影子结果只有 `disabled`、`load-unavailable`、`insufficient-data`、`available` 四种状态。可用时才
   产生 `would-select`；它仅进入低基数指标，绝不写进 HTTP 头、不参与 circuit、不改变现有
   `exact-cache-load-v2`、unknown/stale 的 load-aware 降级或 hard-overload 保护。

## 3. 实施顺序与实现

1. 先在 prediction 同包 contract tests 锁定：影子可能与实际选择不同而不改变实际 selection；首事件
   只记录一次；unknown load 和过期样本明确失去置信度；无界/矛盾保留配置被拒绝。
2. 再实现有界样本、逐 backend 拟合和 `Tracker` 注入接口。`requestpath` 只在 exact input 全部有效且
   当前 decision 是 direct exact v2 时投影数值特征；这是 orchestration 翻译点，不把外部类型带入
   prediction 或 routing。
3. 最后由 Gateway 的 SSE delivery 将实际 TTFT 回交 Lease。新增
   `fishmesh_gateway_prediction_shadows_total{status,outcome}` 和
   `fishmesh_gateway_prediction_absolute_error_seconds`，均不含请求内容或 backend identity label。
   环境键 `FISHMESH_PREDICTION_MODE=off|shadow` 进入组合根；默认 `off`，因此部署前后既有行为不变。

## 4. 本地验证

- `prediction`、`requestpath`、`gateway`、`config` 与组合根定向测试通过：同包 tests 覆盖样本上限/时效、
  unknown 负载、首事件幂等、影子差异和指标隐私边界。
- 完整 Go 门禁和 manifest 渲染将在本提交前执行。没有进行 GPU/Kubernetes 验收，故没有性能、拟合精度或
  负载均衡结论。

## 5. GPU 恢复后的验证策略

1. 保持 `bounded-affinity` 基线，确认 vLLM 2/2 Ready、watchdog 无 WARN/CRITICAL；启用 Prometheus
   observation 后才把 `FISHMESH_PREDICTION_MODE=shadow` 写入一个可回滚的 exact overlay。
2. 先以默认并发 4 收集每 backend 至少 16 个完整 SSE 请求，检查 prediction status 从
   `insufficient-data` 到 `available`，并检查 absolute-error histogram；所有 unknown/stale KV 或 load
   都必须仍显示原有 exact degradation，而非预测选路。
3. 对 shared-prefix 与人为压力场景分别记录实际 backend、would-select、TTFT、queue/running、cached-prefix、
   KV event lag 和 GPU 温度。温度持续超过 80°C 立即停止，watchdog 告警即收尾。
4. 只有影子误差、tail TTFT 和降级正确性均有证据后，才另立 R6H-3 决策切片讨论是否让预测参与实际选择；
   该切片必须保留 hard-overload、置信度门和 `bounded-affinity` 回滚。当前阶段结束后不改集群基线。
