# 阶段 06：Bounded Affinity 调度核心

状态：代码、本地验证和真实 K3s workload smoke 均已完成。

第一次 cluster smoke 发现 ConfigMap `envFrom` 更新不会自动重启 Gateway，导致 artifact 中
阈值与进程实际阈值不一致。该 attempt 已保留并标记 `valid=false`；Deployment 现使用
`fishmesh.io/runtime-config-version` 触发配置 rollout，Gateway 启动日志也会输出全部有效
调度参数。修正后的 attempt 2 已执行并归档为完整 live capture。

## 项目边界

本阶段只实现 cooperative routing-key affinity 的安全快路径，不宣称读取 vLLM 的真实 KV
block ownership，也不引入 GPU score、eBPF、CRD、LLM Agent 或自动阈值调优。

输入限定为：

- EndpointSlice 已过滤的 Ready backend；
- HTTP header 提供的 routing key，策略内部只保存 SHA-256；
- Gateway 生命周期同步的 local in-flight；
- 所有候选 backend 都存在且 `status=ok` 时的 vLLM queue depth。

累计 TTFT、Prefix Cache hit rate、node GPU telemetry 和 degraded/partial queue snapshot
不进入快路径决策。

## 已实现不变量

1. 空 routing key 走 least-loaded，不会固定到同一 backend；
2. 首次 key 使用 Rendezvous Hash，Endpoint 成员变化时减少无关 key 重映射；
3. registry 使用滑动 TTL、最大 entry 数和过期/最老 entry 回收；
4. preferred queue 未超过 pool minimum + queue delta，且 local in-flight 未超过 pool
   minimum + inflight delta 时保持亲和；
5. queue 与 in-flight 独立比较，不合成不可解释的 weighted score；
6. queue snapshot 只有在所有候选 backend 都为 `ok` 时才参与决策；
7. spillover 不改写 preferred backend，压力恢复后重新回到亲和 backend；
8. EndpointSlice unavailable、没有 Ready backend 或 freshness 超过 max age 时回退
   Kubernetes Service；
9. response header 和 Loadgen artifact 同时记录 policy、reason、preferred、selected 和
   spillover reason；
10. Prometheus 暴露固定枚举的 routing reason 和 spillover counter，不把请求 key 用作 label。

## 配置

| 环境变量 | 默认值 | 含义 |
| --- | --- | --- |
| `FISHMESH_AFFINITY_TTL` | `5m` | 活跃 routing key 的滑动过期时间 |
| `FISHMESH_AFFINITY_MAX_ENTRIES` | `10000` | registry 硬上限 |
| `FISHMESH_AFFINITY_INFLIGHT_DELTA` | `2` | local in-flight 相对差值 |
| `FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA` | `1` | vLLM queue depth 相对差值 |

所有显式配置解析失败都会阻止 Gateway 启动，不静默使用默认值。

## 已完成验证

- affinity hit 稳定性；
- local in-flight 与 queue-depth spillover；
- degraded/partial queue snapshot 不参与路由；
- spillover 后恢复 preferred backend；
- missing key、TTL、容量回收；
- 16 goroutine 并发选择的 race test；
- stale/unavailable/no-ready EndpointSlice 的 direct-routing eligibility；
- Gateway response header、Prometheus spillover counter 和 Loadgen provenance。

### 真实 K3s smoke：attempt 2

归档：`artifacts/published/2026-08-09-bounded-affinity-runtime-smoke-attempt-2/`。

- Git SHA：`9f41c5af46c782bb5806d8f074c484bca73d624a`；
- FishMesh image digest：
  `sha256:2b80b19f5c25bc33ad1357c5018458211df831224d21954fa814e6c28ca4d60b`；
- vLLM：`0.23.0`；K3s：`v1.36.3+k3s1`；
- workload：24 requests、concurrency 8、固定 hot routing key、4096-byte prefix、
  `max_tokens=64`；
- 结果：24/24 成功，TTFT P50/P95/P99 分别为 54.231/133.193/133.251 ms；
- 决策：1 次 affinity miss、11 次 hit、12 次 local-inflight spillover；两个 backend
  各选择 12 次，Service fallback 为 0；
- Gateway 启动日志确认 smoke 进程实际使用 `inflight_delta=0`、`queue_depth_delta=1`，
  不再依赖 ConfigMap 静态内容推断运行时配置。

该运行验证的是 rollout、EndpointSlice eligibility、亲和命中、压力溢出、响应 provenance
和归档链路。单次小样本、单张 time-sliced GPU 不能用于宣称吞吐或尾延迟优于其他策略。
归档完成后 smoke Job 已删除；其 Job YAML、原始日志和逐请求 JSONL 均保留在 artifact 中。
集群已恢复默认 `inflight_delta=2`、`queue_depth_delta=1` 的 bounded-affinity overlay。

## 明确未完成

- recent transport error EWMA 与短 TTL circuit breaker；
- endpoint 删除时对 transport pool/in-flight/observation map 的主动批量 GC；
- admission limit 和 `MaxConnsPerHost`；
- 重复随机 benchmark、统计区间和开源 router 对照。

这些属于后续 P1/P2 条目，不能因为 bounded-affinity-v1 已能路由就宣称 MVP 性能结论成立。
