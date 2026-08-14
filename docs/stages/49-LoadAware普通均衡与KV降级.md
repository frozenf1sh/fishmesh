# 阶段 49：Load-aware 普通均衡与 KV 降级

## 1. 范围

本阶段把 `load-balanced` 从“只看单个 Gateway 的 local in-flight”升级为可复用的 load-aware
普通策略。它同时服务普通模式和 `kv-aware-load-fallback-v1`，避免 KV signal 失效后丢弃仍然
可用的 vLLM 负载事实。

## 2. 选择契约

- 所有候选的 queue/running 都有效时，按 queue、running、local delta、local in-flight 比较；
- queue/running 任一候选缺失或过期时，整体退回 local in-flight，不能把未知解释为零；
- 已发布的 hard-overload backend 优先排除；所有候选都过载时保留可用性优先，选压力相对较小者；
- 完全相同时继续使用 routing key 和 backend ID 做确定性平局消解；
- 不把 queue、running 和 local in-flight 简单相加，避免重复计算同一批请求。

## 3. KV-aware 降级

Render、KV event/replay 或本地 KV index 暂时不可用时，requestpath 仍返回
`kv-aware-signal-unavailable` / `kv-aware-load-fallback-v1`，但选择逻辑复用新的 load-aware
普通策略。Discovery、circuit 或策略候选完全不可用时，才继续使用 Service fallback。

实际选定 backend 的 transport/stream failure 仍不在当前请求内自动换 backend；它通过 lease outcome
更新 circuit，影响后续请求。

## 4. 验证

新增 routing contract tests 覆盖：完整 vLLM load、partial observation、hard-overload 排除和
local fallback。下一阶段继续补充 Little’s Law 所需的 accepted/rejected、Gateway in-flight、
backend queue/running 和请求完成率观测；本阶段尚未声称已经得到容量或 QPS 结论。
