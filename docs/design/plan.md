# FishMesh 设计与实施路线

> 状态：工程优先路线，2026-08-10。R5C 已完成 llm-d Router 编译期插件最小切片。方向约束以
> [`project-charter.md`](project-charter.md) 为准。

## 1. 交付目标

FishMesh 要交付一个 Kubernetes-native LLM 请求流量调度组件，而不是一篇调度策略研究：

- 正确代理 OpenAI-compatible HTTP/SSE 请求；
- 从 Kubernetes 动态维护可用 backend；
- 在 affinity、负载和故障之间做有界、可解释的 endpoint selection；
- 在过载、endpoint 删除、观测过期和上游错误时提供明确的降级行为；
- 以 standalone Gateway 支持开发和测试，以 EPP/llm-d 集成形成生产形态；
- 通过自动化测试、故障演练和有限 benchmark 验证工程结果。

实验不单独形成产品路线。只有当实验决定实现方案、验证验收条件或防止回归时，才进入当前
里程碑。

## 2. 当前系统基线

### 已完成

- Go streaming proxy、SSE 完整消费、TTFT 和 request provenance；
- EndpointSlice Ready discovery、watch/relist、freshness/readiness 和 Service fallback；
- per-backend vLLM queue/running observation，区分 ok/degraded/unavailable；
- bounded-affinity-v1：Rendezvous Hash、SHA-256 routing key、TTL/容量上限、独立
  queue/local-inflight spillover；
- Prometheus routing/discovery/backend metrics；
- 严格启动配置、graceful shutdown、最小 RBAC、Kustomize、CI 和 race tests；
- 真实 K3s + vLLM 的行为 smoke 和可追溯运行元数据。

### 尚未闭环

- llm-d 插件、组合根和最小配置已实现并通过本地契约测试，但标准 Gateway/EPP 栈尚未部署；
- simulator 已覆盖第一组自动 fault E2E，长时间 churn/soak 尚未完成；
- dashboard、trace、release/supply-chain 工程尚未完成。

## 3. 目标运行时

```text
Development / conformance
  client -> FishMesh standalone Gateway -> scheduler core -> simulated/vLLM backends

Production-shaped integration
  client -> Envoy-compatible Gateway -> llm-d EPP runtime
                                      -> FishMesh routing plugin
                                      -> vLLM backends

Shared policy inputs
  eligible candidates + routing key + queue + in-flight
```

standalone 与 integrated mode 必须共享：

- `internal/serving/routing` 中的 bounded-affinity 选择语义；
- 相同候选、routing key、queue 和 in-flight 输入下的 endpoint/reason；
- deterministic/race tests 和选择 conformance fixture。

两种模式不共享 delivery 故障处理。standalone requestpath 继续拥有 EndpointSlice、Service
fallback、local circuit 和 transport lease；integrated 模式复用 llm-d 的 InferencePool、subset、
flow control 和 stream lifecycle。空 EPP 候选集必须按协议返回 503，不能为了表面一致而调用
standalone Service fallback。

R5B 已完成短期 spike，并在
[`ADR-001`](decisions/001-llmd-router-integration.md) 选择 pinned llm-d Router + 编译期
FishMesh scorer。LWEPP 只用于标准参考/conformance，不作为生产路径；不自研 ext_proc 服务。

## 4. 快路径设计

```text
request
  -> validate + RequestContext
  -> admission
  -> eligibility
       Ready && fresh && circuit closed
  -> bounded affinity
       preferred within queue/inflight bounds ? preferred : least-loaded
  -> bounded transport
  -> stream response + record local outcome
```

### 快路径允许的输入

| 信号 | 用途 | 约束 |
| --- | --- | --- |
| Endpoint Ready/terminating | eligibility | Kubernetes 生命周期事实 |
| discovery freshness | fallback | 超时后不继续使用无限陈旧地址 |
| local in-flight | spillover/admission | 与 Gateway 请求生命周期同步 |
| vLLM waiting/running | spillover | 只使用存在且足够新的字段 |
| recent local transport errors | circuit | 只归因到实际选中的 backend |
| session/routing key | affinity | 只保存摘要，不进入 metric label |

### 不进入快路径 score

- 进程生命周期累计 TTFT histogram；
- 累计 Prefix Cache hits/queries；
- 节点级 GPU utilization、温度和显存；
- eBPF RTT、重传或 socket stall；
- LLM 或固定权重生成的综合分数。

这些数据可以用于告警、时间窗口分析和方案验证，但不能把观测缺口伪装成精确调度信号。

## 5. 状态与资源不变量

正式 scheduler 输入逐步改为独立 sample：

```text
Sample[T] {
  value
  valid
  observed_at
  source
  error
}
```

必须始终成立：

1. 空 routing key 不形成全局热点；
2. partial/stale observation 不等于零负载；
3. spillover 不重写 affinity preference；
4. terminating、stale 或 open-circuit backend 不进入候选集；
5. 请求取消能传播到 upstream，请求完成后计数必定释放；
6. affinity、connections、waiting requests、observations、circuits 和 metric labels 都有
   上限或 GC；
7. fallback、reject 和 retry 均有固定 reason，不发生静默策略变化；
8. 流响应开始后不进行透明 retry。

## 6. 实施里程碑

### P0：可信 serving baseline（已完成）

- keep-alive baseline、动态 discovery 和 SSE 生命周期；
- 明确观测 availability/freshness；
- 配置失败关闭和实验 provenance；
- 移除 weighted GPU score、eBPF 和 Agent 主线。

### P1：request-path reliability（已完成）

- transport error EWMA/短 TTL circuit；
- endpoint-scoped transport/in-flight/observation/circuit/metric GC；
- admission limit、`MaxConnsPerHost` 和明确 429/503；
- cancellation、deadline 和 retry boundary tests；
- per-field sample 契约。

验收：在 simulator 中重复注入 slow/error/removed backend，内存状态保持有界，请求不继续发送
到被隔离 endpoint，恢复无需重启 Gateway。

当前 unit/race fault tests 已覆盖上述状态机，真实 K3s 已验证新镜像、配置、metrics 和 vLLM
请求兼容性；更长时间的 churn/soak 被纳入 P2 simulator E2E。

### P2：标准集成与自动 E2E（当前）

- `serving-domain-redesign.md` 的 R1–R4 已完成，可复用 requestpath 与显式组合根已经落地；
- controlled backend simulator 与第一组无 GPU fault E2E 已完成；
- EPP/llm-d integration spike、ADR、pinned scorer/adapter 和组合根已完成；
- 纯 routing/integrated selection/reason conformance 已完成；
- 部署标准 Gateway/EPP/InferencePool，并完成 wire-level failure contract；
- Pod 删除、discovery stale、overload、transport failure 自动化。

验收：同一 policy 在相同候选与信号下产生一致选择和 reason；两种运行时各自遵守 EPP 或
standalone failure contract；CI 不依赖 GPU 即可覆盖关键状态机。

代码重构不是新的产品路线，而是 P2 的入口条件：如果 simulator 和 EPP adapter 继续直接依赖
当前大 Gateway，它们会复制或绑死 standalone 运行时。R1–R4 必须保持行为不变，并按独立阶段
提交、验证和推送。

### P3：可操作与可交付

- Prometheus dashboard、结构化日志关联和 OpenTelemetry trace；
- runbook、告警与 rollout/rollback 演练；
- multi-arch、registry digest、SBOM、签名和版本化 release；
- 资源 requests/limits、PDB 和安全清单复核。

验收：从一次请求 ID 能定位 endpoint selection、upstream 结果和 fallback/circuit 原因；新环境
可按文档部署、验证并回滚。

### P4：有限对照验证

- Service、least-loaded、bounded affinity 和一个开源 scheduler；
- hot/skew/overload/failure 四组工程场景；
- 多轮重复、固定版本和环境边界；
- 输出决策结论、适用范围和已知代价。

验收：结果用于确认默认策略和阈值，不以扩展实验矩阵作为项目完成标准。

## 7. 慢速诊断路径

`fishmesh-analyst` 已验证只读 collector 和确定性规则结构，但目前不是主产品。P1-P3 期间冻结
功能扩展，只允许安全修复。若未来恢复，必须直接消费真实 Prometheus 时间窗口和 Kubernetes
事件，并服务于现有故障 runbook；不引入 LLM 自动执行或集群写权限。

## 8. 开源边界

| 层 | FishMesh 当前实现 | 长期边界 |
| --- | --- | --- |
| Model server | vLLM 0.23.0 | 复用上游 engine |
| Standalone proxy | Go HTTP/SSE | 开发、测试和演示 |
| Production gateway | 未实现 | 复用 Envoy-compatible Gateway |
| Endpoint selection | FishMesh scheduler core | llm-d 编译期 scorer 扩展点 |
| Discovery | EndpointSlice REST watch | integrated 模式复用 InferencePool/llm-d data layer |
| Observability | Prometheus metrics | Prometheus + OTel + Grafana |
| GPU operations | device plugin time-slicing | 不自研 GPU 管理平台 |

“为什么不用开源”的工程答案是：FishMesh 不替代成熟入口和推理引擎；它实现并验证一个边界
清晰的 selection/state/failure 模块，然后通过标准扩展点接入开源运行时。

## 9. 明确延期

- eBPF 请求路由或 socket rewrite；
- per-backend GPU utilization score；
- LLM tool-calling Agent 和自动 actuator；
- FishMesh CRD/Operator；
- prefill/decode disaggregation；
- 通用 AI Gateway、认证计费或多租户控制面；
- 仅为展示技术栈而迁移 Service Mesh、Cilium、数据库或消息队列。
