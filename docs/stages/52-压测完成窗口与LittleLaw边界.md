# 阶段 52：压测完成窗口与 Little’s Law 边界

## 1. 目标

阶段 51 已能按 `arrival_rate_qps` 投递 offered rate，但只保留了计划值。若没有实际完成窗口，报告无法区分
“配置的到达速率”和“客户端真正完成了多少请求”。本阶段补齐客户端侧时间证据，并继续把 Gateway accepted
rate 留给 Gateway admission counter。

## 2. 实现

- 每条 benchmark attempt 记录 `started_at` 与 `completed_at`，不记录 prompt、Token IDs、routing key 或
  upstream 原始 headers；
- batch、scenario 和全局 report 都记录窗口起止时间、`elapsed_ms` 与 `completion_rate_qps`；
- `completion_rate_qps = completed / client window`，其中 completed 包含成功和失败的已结束请求；
- 没有完整或正向时间窗口时，elapsed/rate 保持零，不伪造吞吐；
- Markdown 报告同时展示 offered arrival QPS 和客户端 completed QPS，避免把两者混写。

## 3. Little’s Law 边界

客户端 completed QPS 只能作为 workload 侧证据，不能替代 Gateway 的：

```text
lambda_accepted = rate(admitted_requests_total)
L_gateway       = average(inflight_requests)
W_gateway       = L_gateway / lambda_accepted
```

下一阶段若做真实容量结论，必须用同一稳定时间窗 join Gateway admitted/completed counter、in-flight
采样和 vLLM queue/running；客户端 worker 排队以及 batch pause 要单独标记，不能用 offered rate 冒充 accepted
rate。

## 4. 验证

- `go test ./internal/workload/client`：完成窗口、速率计算、反向/缺失窗口保护和现有 benchmark 契约；
- 完整 Go、manifest 和 diff 门禁在提交前执行；
- 本阶段没有 rollout、GPU 压测或容量结论。
