# 阶段 40：R6I-0 可校准 TTFT 路由设计与实施计划

## 1. 范围

本阶段将 R6H 的固定等价 token 成本升级路线固化为 ADR 和多阶段计划，不改变请求路径、部署或集群状态。
正式交付优先 calibrated-static estimator；现有 learned model 继续 shadow，只有误差门禁通过后才讨论 active。

## 2. 决策

- 优化目标固定为 Estimated TTFT，不混合 E2E latency；
- HardOverload 是独立安全门，不由学习模型产生；
- queue/running 与 local in-flight 必须去重；
- static profile 使用毫秒、硬件/模型/vLLM/version identity；
- prediction 不在本阶段改变实际选择；
- 实验必须使用实际 token 档位，并隔离 cold/controlled-warm/steady-warm cache generation；
- 不引入 Redis、Operator、远程 KV、在线探索或新的常驻服务。

详细契约见 [`ADR-003`](../design/decisions/003-calibrated-ttft-routing.md)，实施顺序见
[`ttft-routing-development-plan.md`](../design/ttft-routing-development-plan.md)。

## 3. 当前证据边界

R6H-3 的 4/12 KiB warm-cache A/B 证明当前 profile 的长文本尾延迟收益，但不能用作 TTFT 模型校准：

- 12 KiB 约为 2K token，不是 12K token；
- treatment 之间没有独立 cache salt/generation；
- 正式 overlay 没有启用 vLLM queue/running observation；
- 每 treatment 只有两轮，没有置信区间。

这些结果保留为历史 warm-cache profile，不删除、不重命名为无效实验，也不外推为动态负载收益。

## 4. 下一阶段

R6I-1 先在本地实现快速 observation 配置、HardOverload 运行时接线和负载成本去重。GPU 集群只在代码与
manifest 门禁通过后做最小 smoke；GPU Node/双 vLLM 不稳定时立即暂停等待协助。
