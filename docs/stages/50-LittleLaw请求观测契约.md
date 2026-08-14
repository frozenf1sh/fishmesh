# 阶段 50：Little’s Law 请求观测契约

## 1. 目标

为 QPS、并发和 Little’s Law 计算区分三类事实：

```text
admitted_requests_total       通过 Gateway admission 的请求累计数
inflight_requests              当前已进入请求路径但尚未结束的请求数
requests_total{status=...}    已结束请求累计数，按最终 HTTP status 分桶
```

`admitted_requests_total` 不包含 admission 容量拒绝；`requests_total` 仍保留最终状态，便于区分
成功、客户端错误、上游错误和超时。

## 2. Little’s Law 使用边界

在相同时间窗口内，对已接受请求使用：

```text
L_gateway ≈ average(inflight_requests)
λ_accepted = rate(admitted_requests_total)
W_gateway ≈ L_gateway / λ_accepted
```

vLLM 侧仍使用每个 backend 的新鲜 `queue_length`、`running_requests` 和完成吞吐；不能把 Gateway
in-flight 与 vLLM running 直接相加，因为它们可能描述同一请求。

## 3. 实现

- Gateway 在 permit 成功后记录 `admitted_requests_total`；容量拒绝继续记录
  `admission_rejections_total`；
- `inflight_requests` 的生命周期不变，覆盖 admission 成功到请求完成/失败；
- 不新增高基数 label，不记录 prompt、routing key、Token IDs 或 upstream URL；
- 当前仍是 closed-loop benchmark，固定 QPS 的 open-loop pacing 留到下一阶段实现。

## 4. 验证

Metrics contract test 验证 admitted 与 capacity rejection 可以独立观测。下一阶段增加 benchmark 的
arrival-rate 计划字段，并在报告中同时保留 offered rate、accepted rate、completion rate、平均 in-flight
和 Little’s Law residual。
