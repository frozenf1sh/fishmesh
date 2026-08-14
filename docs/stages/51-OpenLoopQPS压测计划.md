# 阶段 51：Open-loop QPS 压测计划

## 1. 目标

benchmark scenario 新增可选 `arrival_rate_qps`。当它大于零时，batch generator 按目标间隔投递请求；
缺省值为零，继续使用原有的 closed-loop fixed-concurrency 行为。

## 2. 语义边界

- `arrival_rate_qps` 是 offered rate，不是 Gateway 已接受 QPS；
- `concurrency` 仍是客户端 worker 上限；worker 饱和时，客户端 jobs buffer 会积压，实际到达 Gateway
  的速率可能低于 offered rate；
- 报告保留配置的 arrival rate，不把它冒充实测吞吐；实测 accepted/completed rate 应从 Gateway
  `admitted_requests_total`、`requests_total` 和时间窗口计算；
- 不在压测客户端内实现重试，不把 429/502/504 改写成成功。

## 3. Little’s Law 取数

对同一稳定窗口记录：

```text
λ_accepted = increase(admitted_requests_total) / window
L_gateway  = average(inflight_requests)
W_estimate = L_gateway / λ_accepted
```

再与 per-backend vLLM queue/running、请求 duration 和 TTFT 对照。若客户端 worker 饱和，必须把
client-side queueing 单独标记，不能用 offered rate 计算 Gateway Little’s Law。

## 4. 验证

计划校验覆盖正数和负数 arrival rate；默认历史 JSON plans 不变，因为字段可选且零值保持
closed-loop 兼容行为。下一步可在真实集群上按 prompt/cache/output 类别逐级提高 offered rate，寻找
queue 持续增长、P95 失控或 admission rejection 增长的稳定性拐点。
