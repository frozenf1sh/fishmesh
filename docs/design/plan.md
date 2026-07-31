# FishMesh 当前设计与实施路线

> 状态：P0 方向收敛版，2026-08-09。

## 1. 项目定位

FishMesh 是 Kubernetes LLM Serving 的**可解释请求调度实验平台**。它通过真实 vLLM、
动态 endpoint、流式请求和可归档 benchmark，研究连接复用、请求亲和、服务端负载和故障
状态之间的取舍。

它不与 llm-d、NVIDIA Dynamo、vLLM Production Stack 或 Gateway API Inference Extension
竞争完整生产能力。当前 Go Gateway 是 scheduler core 的实验载体；成熟方向是把同一策略
核心接到 Gateway API Inference Extension Endpoint Picker（EPP）边界，并与开源 EPP 做同
环境对照。

面试叙事应是：

> 我没有先假设 cache-aware routing 一定更快，而是建立 Service/keep-alive 基线，发现纯
> affinity 在热点下受益、在倾斜负载下放大尾延迟。因此我把策略收敛为 eligibility filter、
> bounded affinity 和 load spillover，并用动态发现、freshness、故障回退和可复现实验验证
> 每个设计选择；生产集成遵循 Kubernetes InferencePool/EPP 生态。

## 2. 已确认和未确认的事实

### 已确认

- HTTP keep-alive 是默认传输基线；
- 固定 Pod IP 无法承受 Pod 重建，EndpointSlice Ready discovery 是必要工程基础；
- 纯 routing-key affinity 在热点合成负载中可能降低 TTFT；
- 纯 affinity 在混合倾斜负载中可能显著恶化 P99；
- Gateway 本地 in-flight 不能观察绕过 Gateway 的外部负载；
- 缺失、过期和健康的观测必须可区分，不能把缺失数据解释为零负载；
- 两个 vLLM replica 共享一张 time-sliced RTX 4060，不是两个独立 GPU 故障域。

### 尚未确认

- 现有 affinity 是否提高了 vLLM 的真实 token-block Prefix Cache 命中；
- 不同大模型、长 prompt 和高并发下的收益能否复现；
- bounded affinity 相对 least-loaded、llm-d 或 vLLM Router 的收益；
- 当前集群能否提供可归因到单个 Pod 的 GPU telemetry；
- 网络是否是当前同节点数据路径中的主要瓶颈。

未经确认的结论不得进入 README 的能力声明或简历性能数字。

## 3. 快路径架构

```text
request
  -> RequestContext extractor
       model / session-or-routing-key / estimated prompt cost
  -> EligibilityFilter
       Ready && discovery fresh && circuit closed
  -> BoundedAffinityPolicy
       preferred = consistent_hash(key)
       if preferred load <= pool minimum + threshold: preferred
       else: least-loaded spillover
  -> selected backend transport
  -> streaming response + local outcome
```

第一版不实现多指标线性加权。它使用“硬过滤 + 单一可解释决策 + 明确 fallback”：

1. discovery 不新鲜或没有 Ready endpoint 时回退 Kubernetes Service；
2. 没有 routing key 时使用 least-loaded/P2C，而不是把空 key 固定到同一 Pod；
3. preferred backend 没有超过负载阈值时保持亲和；
4. 超过阈值后溢出到更空闲 backend；
5. 近期 transport/backend error 打开短 TTL circuit；
6. 每次决策记录 policy version、reason、preferred/selected backend 和 spillover 原因。

### 快路径允许使用的信号

| 信号 | 用途 | 原因 |
| --- | --- | --- |
| Endpoint Ready/terminating | eligibility | endpoint 生命周期事实 |
| discovery freshness | eligibility/fallback | 防止使用无限陈旧地址 |
| Gateway local in-flight | 快速 load tie-break | 与请求生命周期同步 |
| vLLM waiting/running | bounded spillover | 后端级即时状态，但必须有短 TTL |
| recent local error EWMA | circuit breaker | Gateway 能准确归因到选中 backend |
| session/routing key | affinity | 语义明确、计算便宜 |

### 不进入第一版快路径 score 的信号

- 进程生命周期累计 TTFT histogram；
- 累计 Prefix Cache hits/queries；
- 节点级 GPU utilization、温度和显存；
- eBPF RTT、重传或 socket stall；
- 固定的“AI 推荐权重”。

这些信号用于时间窗口分析、异常门控或实验评估。若未来消费 vLLM KV events 并建立
token-block index，才能把 request-specific cache overlap 作为精确快路径信号。

## 4. 状态模型

`BackendObservation.Status` 是当前过渡契约。正式 scheduler 输入需要把每个字段改成独立
sample：

```text
Sample[T] {
  value
  valid
  observed_at
  source
  error
}
```

原因是“采到 queue 但没采到 TTFT”不能被一个 backend-level `ok` 隐藏；数值零也不能同时
表示“观测到零”和“没有观测”。Scheduler 使用 immutable snapshot/atomic swap，后台采集
和 Prometheus 指标更新不在请求决策临界区执行。

Endpoint 删除时必须同步回收：

- transport/client；
- local in-flight counter；
- circuit state；
- observation state；
- Prometheus backend label series。

## 5. 慢速证据路径

当前 `fishmesh-analyst` 应称为 evidence-based diagnoser，而不是 autonomous Agent：

```text
Incident or alert
  -> Prometheus time-window query + Kubernetes events
  -> typed Signals with per-source availability
  -> deterministic diagnosis rules
  -> read-only Recommendation
```

下一阶段应通过 Prometheus/PromQL 获取 counter rate、窗口 quantile 和趋势；不得通过一个
keep-alive ClusterIP `/metrics` 连接假装聚合多个 vLLM Pod。硬编码 `confidence` 在校准前只
能改称 evidence strength。

LLM narrator 只能把结构化结果转为报告，不获取 Kubernetes 写权限、不执行 shell、不进入
请求路径。自动 actuator 不属于 MVP。

## 6. 开源技术边界

| 层 | 当前实验实现 | 生产/对照方向 |
| --- | --- | --- |
| Inference engine | vLLM `0.23.0` | 保持上游 vLLM，不自研 engine |
| Proxy | Go streaming proxy | Envoy/Envoy Gateway |
| Endpoint selection | FishMesh strategy | Gateway API Inference Extension EPP adapter |
| Production scheduler comparator | Service/least-inflight | llm-d EPP、vLLM Router 或 Dynamo |
| Discovery | EndpointSlice REST watch | InferencePool/EPP data layer |
| Metrics | direct `/metrics` prototype | Prometheus time series + tracing |
| GPU operations | device plugin time-slicing | GPU Operator/DCGM；仅做 node-level health |

“为什么不用开源”的答案不是 FishMesh 更完整，而是 FishMesh 提供了可控的因果实验和自定义
策略核心；通用 ingress、认证、流控、精确 KV index 和大规模控制器优先复用开源。

## 7. MVP 验收边界

### MVP-A：可信 Serving Baseline

- Service + keep-alive；
- EndpointSlice 动态实验与 Service fallback；
- SSE 完整消费，TTFT/TPOT/E2E/错误契约；
- 每次实验首条 JSONL 为 `run_metadata`；
- artifact 包含 Git SHA、image digest、vLLM 参数和集群 profile。

### MVP-B：Bounded Affinity Scheduler

- request/session key 语义明确；
- eligibility filter；
- bounded affinity + overload spillover；
- per-field presence/freshness；
- error EWMA/circuit breaker；
- endpoint 删除后的所有状态回收；
- deterministic unit/race/fuzz tests。

### MVP-C：可复现实验与开源对照

- cold/hot/mixed/skew、多个 prompt 长度和并发档位；
- treatment 顺序随机，多轮重复，报告 effect size 和区间；
- Pod 删除、telemetry stale、API 断连和 overload fault；
- 同环境比较 Service、least-loaded、bounded affinity 和至少一个开源 router；
- 单 GPU time-slicing 结果只声明行为正确性，最终核心结论用独立 GPU 或明确模拟器复验。

## 8. 实施优先级

### P0：方向和证据修复（本阶段）

- 统一 FishMesh 名称和新定位；
- 更新 vLLM 及 NVIDIA device plugin 固定版本；
- 保存失败运行、rerun 和原始 artifact；
- 增加 run metadata 与历史 artifact provenance 标记；
- 将环境变量解析从静默 fallback 改为启动失败；
- 把 GPU-aware、weighted hybrid、Agent、eBPF 从下一步移出。

### P1：调度器核心

- RequestContext extractor；
- per-field sample；
- bounded affinity、spillover、circuit breaker；
- transport/metric state garbage collection；
- `MaxConnsPerHost` 和 admission limits。

### P2：实验系统

- 可声明 experiment matrix、随机顺序和重复次数；
- 自动环境快照、raw artifact 和统计分析；
- controlled backend simulator；
- 独立 GPU 复验。

### P3：工业化集成

- Gateway API Inference Extension EPP adapter；
- llm-d/vLLM Router 对照；
- multi-arch、registry digest、SBOM、签名和 E2E CI；
- Prometheus、Grafana、OpenTelemetry；
- PDB、逐副本滚动和故障演练。

## 9. 明确延期

以下方向在 MVP 数据证明需求前不实施：

- eBPF 请求路由或 socket rewrite；
- per-backend GPU utilization score；
- LLM tool-calling Agent 和自动 actuator；
- FishMesh CRD/Operator；
- prefill/decode disaggregation；
- 请求 replay shadow；
- 为展示技术栈而迁移 Service Mesh、Cilium 或 GitOps。
