# KubeLLM-Edge 最终项目方案

> 面向 Kubernetes LLM Serving 的智能请求调度、可观测与集群诊断平台
>
> 本文件基于 2026-08-08 实验结果形成，作为当前实施的最终决策版。

## 1. 最终定位

KubeLLM-Edge 不是“用 eBPF 直接理解 LLM 状态”，也不是“重新实现一个 Kubernetes Scheduler”。它是一个面向多副本 vLLM Serving 的轻量控制平面：

```text
请求进入
  -> 确定性的 LLM-aware Request Scheduler
  -> vLLM backend
  -> Gateway / vLLM / GPU / 网络指标
  -> 慢速 Cluster Analyst Agent
  -> 诊断、策略建议、受控变更
```

一句话面试叙事：

> 我先用真实基线证明 HTTP Keepalive 的收益，再验证 Prefix Affinity 只在热点前缀下有效；最终把两者收敛成可解释的混合调度器，并用 AI Agent 在慢速控制环分析集群状态、给出带证据的策略建议。

## 2. 研究结论驱动的架构

### 2.1 快速请求路径

Gateway 内的 scheduler 必须是低延迟、确定性和可回滚的，不能在每个请求上调用 LLM。策略顺序建议：

1. 检查 endpoint Ready/健康状态；
2. 对已知热点 `prefix_group` 提供有限 Prefix Affinity；
3. 根据 vLLM queue/running、最近 TTFT、GPU headroom 和错误率计算 backend score；
4. 对过热 prefix 做迁移或放宽 affinity；
5. 失败时回退 Service 或剩余 Ready endpoint。

一个可解释的分数模型：

```text
score(backend, request) =
    w_prefix * prefix_locality
  + w_queue  * queue_headroom
  + w_gpu    * gpu_headroom
  + w_ttft   * recent_ttft_health
  - w_error  * recent_error_rate
  - w_net    * network_penalty
```

第一版权重由配置固定，并在响应头/日志中记录选择原因；后续由 Agent 推荐权重变化，但必须经过门控。

### 2.2 慢速集群分析路径

Cluster Analyst Agent 每 30–60 秒或在异常触发时运行，输入：

- Gateway 请求、TTFT、fallback、连接复用和 backend 分布；
- 每个 vLLM Pod 的 Prefix Cache、queue、running requests、TTFT 和错误；
- GPU 显存、利用率、温度、OOM 和 device plugin 状态；
- K3s Pod/EndpointSlice/事件；
- 可选 eBPF RTT、重传、socket stall。

输出固定 schema，而不是自由聊天：

```json
{
  "diagnosis": "prefix_locality_degraded",
  "confidence": 0.82,
  "evidence": ["cache_hit_rate_down", "queue_normal", "network_normal"],
  "recommendation": "enable_bounded_prefix_affinity",
  "risk": "hot_backend_overload",
  "expires_at": "..."
}
```

默认只读。第二阶段在 Shadow/Simulation 中评估建议；第三阶段才允许通过受限 Controller 修改路由策略，所有变更有 TTL、审计和一键回滚。

## 3. 当前已实现的原型

- `Service + no keep-alive`、`Service + keep-alive`、`Prefix-hash + keep-alive` 连接矩阵。
- Loadgen 确定性 `--hot-prefix-ratio`，可生成热点和混合前缀分布。
- Gateway `load-aware` 原型：按当前 Gateway in-flight 数选择较空闲 endpoint。
- 完整 SSE 消费、TTFT、实际 upstream、错误分类和 JSONL artifact。
- K3s 双节点、vLLM 双副本、NVIDIA device plugin、GPU 驱动/DKMS/CDI 恢复验证。

注意：当前 `BackendEndpoints` 仍是实验用静态地址，`load-aware` 只观测 Gateway 自身 in-flight，不应直接宣称为正式 GPU-aware scheduler。

代码边界已经对应到以下限界上下文和包：

```text
internal/serving/routing/   # Strategy、Backend、Snapshot、Decision
internal/serving/endpoint/  # Resolver；当前 Static，后续 EndpointSlice
internal/serving/transport/ # 每个 backend 的 HTTP client/keepalive 生命周期
internal/serving/gateway/    # HTTP/SSE 代理、请求生命周期和 metrics
internal/workload/loadgen/   # 可重复工作负载与 JSONL artifact
internal/diagnostics/        # domain/application/adapters/delivery/config
```

Gateway 只负责组合这些边界，不再在 HTTP handler 中直接实现 hash、连接池和 endpoint
状态。这样后续接入 vLLM metrics 或 EndpointSlice 时，可以替换 resolver/snapshot，而不
改变请求协议和 Loadgen。

## 4. MVP 最终边界

### MVP-1：Serving Baseline

- 默认 `Service + keep-alive`；
- Gateway/Loadgen/vLLM 可用 Kustomize 复现；
- JSONL 和 Prometheus 指标契约稳定；
- 记录 request、prefix group、backend、TTFT、错误和版本 metadata。

### MVP-2：可解释调度器

- `RouteStrategy`、`BackendResolver`、`TransportProvider` 三个接口；
- Service、Prefix Affinity、In-flight Load-aware 三种策略；
- EndpointSlice Ready watch，替代 Pod IP 快照；
- 健康检查、fallback、热点保护和连接池上限；
- 通过 vLLM `/metrics` 接入 queue/running、TTFT、Prefix Cache 和 GPU 数据。

### MVP-3：Cluster Analyst Agent

- 规则优先：局部性退化、GPU 饱和、推理排队、网络重传、endpoint 故障；
- 输出证据、置信度、影响范围和建议；
- 无 LLM 也能工作，LLM 只负责把结构化诊断翻译为可读报告；
- 默认只读，不自动执行 `kubectl delete`、扩缩容或修改路由。

## 5. 后续能力分层

### Tier 1：正式 Hybrid Scheduler

把当前三个策略统一为 score-based policy，使用 EWMA TTFT、queue headroom、GPU free memory、Prefix Cache hit 和错误率；对热点 prefix 设置 TTL 和最大并发，防止把所有请求压到一个 Pod。

### Tier 2：Kernel Telemetry

eBPF 仅采集 TCP RTT、重传、socket lifecycle 和 stall，用来解释网络层原因；不解析 prompt、token 或 KV cache，不决定后端。

### Tier 3：Guarded Agent Actuator

Agent 只提交 `Recommendation`，由策略门控和 Controller 决定是否应用。限制变更频率、影响范围和 TTL，保留旧策略快照和回滚按钮。

### Tier 4：Gateway Replay Shadow

对幂等、脱敏、采样请求做应用层 replay；不使用 eBPF 克隆流式请求，不让 shadow 影响主请求。

### Tier 5：CRD/Operator

只有出现多个 Gateway、策略版本、灰度发布或租户隔离需求时才引入，不为当前单 Gateway 实验预先增加控制器复杂度。

## 6. 关键工程约束

- 不把 Gateway 的 prefix hash 叫作 vLLM Cache Key；Prefix Cache 以 vLLM 自身指标为准。参考 [Automatic Prefix Caching](https://docs.vllm.ai/en/latest/design/prefix_caching/) 和 [vLLM Metrics](https://docs.vllm.ai/en/v0.11.0/design/metrics.html)。
- 不把 request routing 叫 Kubernetes scheduling。
- 不使用静态 Pod IP 作为正式发现机制。
- 不把 Agent 放进每请求路径。
- 不因一次更好的 P95 就忽略 P50、P99、成功率和故障恢复。
- 所有策略都必须有 Service fallback、超时、健康状态和资源上限。
- 所有 GPU 实验前先验证内核、DKMS、NVML、CDI、device plugin 和 vLLM readiness。

## 7. 最终实施顺序

1. 提交并固化当前 Loadgen 热点分布和 Gateway load-aware 原型。
2. 把静态 endpoint resolver 替换为 namespace-scoped EndpointSlice watcher。
3. 接入 vLLM metrics 和 GPU exporter，形成真正的 backend snapshot。
4. 实现 Hybrid Scheduler，并用本报告的 hot/mixed/saturation 负载重新验证。
5. 实现只读 Cluster Analyst Agent 和结构化 Recommendation。
6. 在 Shadow/Simulation 中验证 Agent 建议，最后再考虑 guarded actuation。

项目完成的标准不是“所有模块都存在”，而是能够用实验回答：何时 Keepalive 足够、何时 Prefix Affinity 值得付出复杂度、何时负载和故障信号必须覆盖语义局部性，以及 Agent 的建议是否有可审计证据。

当前 Agent 骨架已落地在 `cmd/fishmesh-analyst` 与 `internal/diagnostics`：它以结构化
`Incident -> Tool Signal -> RulePolicy -> Diagnosis` 验证控制面契约，默认 demo 模式
可重复，`gateway-metrics` overlay 可接入 Gateway `/metrics`。LLM narrator、vLLM/GPU/
Kubernetes/eBPF collectors 和 guarded actuator 仍按上面的分层顺序推进。
