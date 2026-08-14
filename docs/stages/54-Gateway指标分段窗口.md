# 阶段 54：Gateway 指标分段窗口

## 1. 背景

阶段 53 的第一版 metrics window 覆盖整个 benchmark invocation，warmup、batch pause 和 scenario 之间的
等待可能进入 counter delta 与平均 in-flight。这样的结果可以观测，但不适合作为正式容量窗口。

## 2. 实现

- 每个 scenario 在 warmup 完成后、正式 batch 开始前启动 Gateway metrics sampler；最后一个 batch 结束后停止；
- 每个 scenario 形成独立的 admitted/completed counter delta、active elapsed 和时间加权平均 in-flight；
- 全局报告按所有有效 scenario 段聚合：counter delta 求和，active duration 求和，in-flight 按段时长加权；
- warmup 不进入 metrics window，报告保留计划中的 `warmup_requests` 并标记 `warmup_excluded=true`；
- scenario 之间的 batch pause 不进入 active elapsed；采样失败、counter reset 和任何无效段仍使全局 metrics
  保守地标记 invalid，不影响 workload 主流程。

## 3. Little’s Law 语义

```text
lambda_accepted = sum(admitted_delta) / sum(active_elapsed)
L_gateway       = sum(segment_average_inflight * segment_elapsed) / sum(active_elapsed)
W_gateway       = L_gateway / lambda_accepted
```

这仍然是 Gateway accepted 请求的窗口结论，不是客户端 offered rate，也不代表容器/GPU 负载已经进入路由。

## 4. 验证

- 覆盖多段窗口的 duration 加权、scenario gap 排除、counter reset 和 warmup 后启动 sampler；
- 完整 Go、manifest 和 diff 门禁在提交前执行；
- 本阶段没有 rollout、GPU 压测或容量结论。
