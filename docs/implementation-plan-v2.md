# KubeLLM-Edge V2 详细实施方案

> 配套文档：`docs/project-plan-v2.md`
>
> 原则：先保证测量正确，再增加路由能力；每一阶段都有可运行交付物和停止条件。

## 1. 执行规则

1. 不修改旧方案文档；新工作只引用本 V2 文档。
2. 不把当前未提交的实验改动与规划文档混合提交；每个逻辑变化独立提交。
3. 一次只改变一个主要变量。连接模型、路由策略、并发、模型和 prompt 不能在同一轮同时变化。
4. 每次实验保存配置、版本、节点、指标和原始 JSONL；没有 artifact 的运行不进入结论。
5. 优先使用可回滚的 Kustomize overlay 和 Job；不直接在集群中手工改生产对象。
6. 在加入 EndpointSlice、eBPF 或 AIOps 前，先满足前一阶段的验收条件。

## 2. 现状基线与目标

当前仓库已经具备：K3s 控制平面、Ubuntu RTX 4060 算力节点、NVIDIA device plugin、vLLM 多副本、Go Gateway、Loadgen、Kustomize 和随机服务基线。当前连接矩阵实验文件和代码改动仍可能处于未提交状态，本实施方案不覆盖它们；先将其整理成独立实验提交，再开始新增接口。

目标拓扑：

```text
OrbStack Mac (fishmesh-control-plane)
  └─ K3s server / kubectl / Prometheus-side tools

Ubuntu RTX 4060 (fishmesh-gpu)
  ├─ K3s agent
  ├─ NVIDIA device plugin
  ├─ vLLM replica A/B
  └─ optional eBPF DaemonSet
```

Tailscale 用于管理连接，Clash 只用于镜像/依赖访问。网络代理是否开启必须作为运行环境记录，不能混入路由策略实验。

## 3. 阶段总览

| 阶段 | 内容 | 产物 | 通过后才能进入 |
| --- | --- | --- | --- |
| P0 | 仓库和集群基线整理 | 状态清单、可复现部署 | P1 |
| P1 | 测量正确性和指标契约 | 测试、JSONL schema、metrics | P2 |
| P2 | A/B/C 连接与路由矩阵 | 三组可重复 benchmark | P3 |
| P3 | 条件式 EndpointSlice 路由 | 动态发现和故障 fallback | P4 |
| P4 | 统计分析与工程结论 | 报告、图表、结论 | MVP 完成 |
| P5 | 平台化重构 | routing/transport/endpoint 接口 | 附加项 |
| A1-A4 | eBPF、诊断、replay、Operator | 分层扩展 | 按触发条件选择 |

工作量级可粗略理解为：P0-P2 是最小可交付主体；P3-P5 是增强和工程化；每个附加项都应单独排期和验收。

## 4. P0：仓库与集群基线整理

### 目标

让任何人能从仓库状态准确知道“运行了什么、在哪个节点、用的哪个镜像和模型”，并把已有实验改动与新规划隔离。

### 操作

1. 检查 `git status`，确认现有 Gateway、Loadgen、配置和 `deploy/experiments` 改动的归属；只为连接矩阵建立独立提交。
2. 为 baseline 和 experiments 统一增加 labels：`app.kubernetes.io/name`、`app.kubernetes.io/component`、`fishmesh.dev/condition`、`fishmesh.dev/run-id`。
3. 固化 Namespace、Service、Deployment、Job 的 Kustomize 入口；所有实验从 overlay 生成，不依赖手工 `kubectl edit`。
4. 记录节点架构、K3s 版本、GPU 型号/显存、模型路径、镜像 digest、代理状态和当前 Pod 分布。
5. 明确 vLLM 的默认模型和显存预算：RTX 4060 8 GiB 先以 Qwen2.5-0.5B 双副本为默认；1.5B 必须单独验证，不作为隐含前提。

### 交付物与停止条件

- `docs/project-status.md` 或运行记录能复述拓扑和版本。
- `kubectl apply -k deploy/baseline/...` 可完成部署，删除后可重建。
- vLLM readiness、Gateway readiness、Loadgen smoke test 均通过。
- 如同一节点的 GPU 显存不足，先调整副本/模型，不进入路由优化。

## 5. P1：测量正确性与契约

### 5.1 Loadgen

实现并测试以下行为：

- 用固定的 `prefix_group` 生成共享前缀和变化后缀；记录生成参数，不上传原始敏感 prompt。
- 完整消费 SSE/stream body 后才结束请求；首 token 时间、最后 token 时间分别计时。
- 发送唯一 request ID，并从 Gateway 响应头读取 `routing_mode`、`selected_upstream` 等调试信息。
- 明确 `--keep-alive`、并发、请求总数、warm-up、最大输出 token、随机种子和超时。
- 将错误分类为 connect、timeout、HTTP、SSE protocol、upstream inference 和 client cancellation。

### 5.2 Gateway

- 统一记录 request start、upstream start、first byte/first token、body end 和状态码。
- 连接新建/复用只作为计数和实验字段，不把 request ID、prompt 或完整 routing key 放进 Prometheus label。
- 对每次请求返回可选的 `X-FishMesh-Routing-Mode` 和 `X-FishMesh-Upstream`，生产默认可以关闭。
- 当路由策略失败时显式 fallback 到 Service，并增加 fallback counter；禁止静默使用过期 endpoint。

### 5.3 vLLM 与节点观测

从每个 Pod 独立抓取 `/metrics`，保留 Pod/instance 维度；至少校验 TTFT、队列/运行请求、KV 使用和 Prefix Cache 指标是否存在。若当前 vLLM 版本没有某指标，artifact 中记录“unavailable”，不能用网关哈希推断。

### 5.4 测试和交付

- Gateway 配置解析、路由选择、fallback、响应头和 metrics 的单元测试。
- Loadgen 的计时、SSE 完整消费、错误分类和 JSONL schema 测试。
- 一次小规模 smoke run，产生 `artifacts/<run-id>.jsonl` 和 metadata。
- `git diff --check`、Go tests、镜像构建和 Kustomize 渲染通过。

## 6. P2：连接与路由矩阵

### 6.1 策略接口

把 Gateway 内部逻辑拆成可替换边界，建议接口语义如下：

```go
type RouteStrategy interface {
    Select(ctx context.Context, key string) (Backend, error)
}

type BackendResolver interface {
    Snapshot(ctx context.Context) ([]Backend, error)
}

type TransportProvider interface {
    ClientFor(BackendKey) *http.Client
}
```

MVP 可以由静态 Service resolver 实现，后续 EndpointSlice 只替换 resolver，不改 Loadgen 或实验 artifact 格式。接口命名要反映真实职责：`RouteStrategy`，而不是 `Scheduler`。

### 6.2 四组条件

#### A：Service + no keepalive

Gateway 使用 Service 地址，每次请求不复用上游连接。它提供最保守的 transport 基线。固定所有 prompt、并发和超时，先跑 warm-up 再记录正式样本。

#### B：Service + generic keepalive

Gateway 使用一个有界的通用 `http.Transport`。记录 idle connection reuse 和实际 upstream。此组只回答连接复用的收益，不能命名为 prefix affinity。

#### C：Prefix-scoped affinity

按 `prefix_group` 选择稳定 backend，并为每个 prefix group 使用有界连接池。第一版可把每桶连接数限制为 1，以清楚展示 affinity；随后增加连接数作为独立变量，避免把“每桶串行化”误当成路由收益。

必须定义：

- 未知 prefix 的分配算法和是否允许重新分配。
- backend 不健康、连接失败和超时时的 fallback。
- prefix 桶数量上限、空闲过期时间和内存上限。
- 同一条件下请求并发如何避免被单连接排队完全主导。

#### D：Direct Endpoint（可选）

只有 C 证明“Service LB 仍是主要混杂因素”时才实现。D 使用 Ready Endpoint 快照和稳定哈希，仍保留 Service fallback；不把当前静态 Pod IP 列表升级成正式架构。

### 6.3 运行协议

每个条件执行：

1. 部署对应 overlay，等待所有 readiness。
2. 记录 run metadata：git SHA、镜像 digest、模型、K3s 节点、GPU 状态、配置摘要。
3. warm-up 固定请求数，不纳入统计。
4. 正式运行固定请求数/时长和随机种子；首轮至少三次重复。
5. 保存 Loadgen JSONL、Gateway metrics、每个 vLLM Pod metrics 和 kubectl 事件快照。
6. 运行结束后删除 Job，不删除长期 baseline Deployment；下一条件前确认旧连接和旧 Pod 不影响结果。

### 6.4 初始建议参数

保留当前可运行的 8 个 prefix groups、固定前缀长度、并发 4 和最大输出 32 token 作为第一轮可比参数。参数不是永久标准：P2 完成后再增加并发、热点比例和输出长度，分别观察局部性收益是否被排队覆盖。

## 7. P3：EndpointSlice 与故障处理

### 进入条件

- A/B/C 已完成，且 D 的研究问题明确。
- 静态 endpoint 的局限已经由实验或代码审查确认。
- 团队愿意承担 client-go informer、缓存一致性和 RBAC 的维护成本。

### 实现范围

1. 使用 client-go watch/list EndpointSlice，只选择 `ready=true` 且地址类型正确的 endpoint。
2. 维护带版本/时间戳的内存快照；变更时原子替换，不在请求路径中等待 API Server。
3. 使用 rendezvous hash 或等价稳定算法；endpoint 集合变化时尽量减少无关 prefix 的迁移。
4. 连接失败、readiness 消失、超时和空快照都必须有明确 fallback 和 metrics。
5. 创建最小 ServiceAccount、Role/ClusterRole 和 RoleBinding；只允许读取目标 namespace 的 Service/EndpointSlice。
6. 增加故障演练：删除一个 vLLM Pod、阻断一个 endpoint、重启 Gateway、短暂失去 API Server 连接。

### 完成条件

- Pod 滚动更新后无需改配置即可被发现。
- 失效 endpoint 不再接收新请求，已有请求按超时/重试策略结束。
- resolver 不可用时，Gateway 仍能使用最后一个安全快照或 Service fallback，并在 metrics 中可见。

## 8. P4：分析与结论生成

### 分析流程

`analysis/` 先使用 Python 3.12 + uv 和标准库解析 JSONL；只有需要置信区间、绘图或大量数据时再引入 pandas/numpy/matplotlib。分析脚本必须是无状态的：输入 artifact 和 metadata，输出 Markdown/CSV/PNG，不读线上 API。

### 最低分析内容

- 每条件成功率、请求数、P50/P95/P99 TTFT、ITL、E2E 和输出 token。
- 上游分布、每 prefix group 的 backend 迁移率和分布熵。
- vLLM Prefix Cache hit/query（可用时）、队列/运行数、GPU 利用率和显存。
- 失败按类别分组；报告 timeout、fallback 和连接池耗尽。
- 三次重复的中位数和波动范围；不要只给单次最好结果。

### 结论模板

每个结论必须写成“条件 → 观测 → 解释 → 限制”：

```text
在相同并发和 prompt 分布下，C 相比 B 的 prefix→backend 迁移率下降……；
同时 vLLM cache hit……、TTFT P95……；
这支持/不支持 prefix affinity 的收益假设；
但当前结果受单 GPU、模型大小和连接池上限限制。
```

## 9. P5：平台化重构

P2/P3 得到结论后，再做以下稳定化，而不是提前重写：

1. 将 `internal/gateway` 中的路由、transport、endpoint、metrics 分成职责单一的包。
2. 增加配置校验：互斥模式、backend 列表为空、连接池上限、超时和 fallback 必须在启动时失败或给出明确警告。
3. 为策略增加 fake resolver、fake transport 和 deterministic hash 测试。
4. 统一 Kustomize labels、资源 requests/limits、readiness/liveness/startup probes 和最小 RBAC。
5. 加入 NetworkPolicy（若当前 CNI 支持且不破坏 Tailscale/metrics），只开放 Gateway→vLLM 和观测路径。
6. 在 CI 中加入 Go test、静态检查、镜像构建、Kustomize render、manifest 校验和小型 loadgen 单测；不把 GPU benchmark 放进每次 PR。

## 10. 附加项实施方案

### A1：eBPF Kernel Telemetry

**目标**：回答“TTFT 变慢是否来自网络事实”。

**实现**：在 Ubuntu GPU 节点部署只读 DaemonSet，使用 `cilium/ebpf` 从 tracepoint/kprobe/socket 相关路径采集连接生命周期、RTT、重传和 stall；导出低基数 Prometheus 指标和按时间窗口的事件。优先使用现成内核字段，不做 prompt/SSE 解析。

**验收**：人为制造延迟/重传时指标可见；正常运行时 CPU 和内存开销有上限；卸载 DaemonSet 不影响 Gateway/vLLM。

### A2：只读 AIOps Diagnosis

**目标**：把多层指标整理为可审计的根因候选。

**实现顺序**：规则引擎 → 证据窗口 → Markdown/JSON 报告 → 可选 LLM 总结。每条建议包含时间范围、原始指标、置信度和反证；禁止默认写 Kubernetes API。

**验收**：至少覆盖局部性退化、服务端排队、GPU 饱和、网络重传四类场景，并能在无 LLM 时运行。

### A3：Gateway Replay Shadow

**目标**：在不改变主请求结果的情况下比较候选版本。

**实现**：Gateway 对明确允许的请求做采样、脱敏和异步 replay；shadow 响应不返回客户端，不进入主链路延迟。限制 body、QPS、并发、总时长和错误重试；默认关闭。

**验收**：主流量的错误率和 TTFT 不受影响；能区分 primary/shadow；敏感字段和非幂等请求不会被 replay。

### A4：CRD/Operator

**触发**：需要多 Gateway、策略版本、灰度发布、租户隔离或声明式路由生命周期时。

**实现**：先写 CRD schema 和状态条件，再实现 controller；加入 RBAC、finalizer、升级和回滚测试。不要把当前单 Gateway 配置包装成无实际收益的 Operator。

## 11. 回滚与故障手册

- **路由策略异常**：切换 `routing_mode=service`，保留 Gateway 和 vLLM，不删除数据 artifact。
- **EndpointSlice resolver 异常**：停止 resolver 使用，回退 Service；检查最后安全快照和 RBAC。
- **连接池耗尽**：降低每桶并发或恢复 Generic Keepalive，记录 queue/timeout，不直接扩大 GPU 副本。
- **vLLM 显存不足**：降低副本数/模型或关闭实验 overlay；不在运行中强行修改 CUDA 参数。
- **eBPF 影响节点**：删除其 DaemonSet/回滚镜像；确认业务 Pod、NVIDIA plugin 和 K3s agent 状态。
- **Ubuntu 暂停/关机**：GPU 节点上的 vLLM、Jobs 和可选 eBPF 会停止；控制平面仍可运行但没有可用推理后端，实验应标记为中断，不能把中断前后数据拼接。

## 12. 每阶段 Definition of Done

每阶段必须同时满足四项：

1. 代码或 manifests 可从干净工作区构建/渲染。
2. 有自动化测试或可重复的 smoke 命令。
3. 有 artifact、日志或截图证明行为发生。
4. 有一段中文记录说明已知限制、未完成项和下一阶段入口。

任何一项缺失，都只能标记为“进行中”，不能写成已完成能力。

## 13. 近期执行顺序

下一轮只做以下三件事：

1. 整理并单独提交当前连接矩阵代码和 manifests，确认 A/B/C 可以渲染和运行。
2. 补齐 P1 的 JSONL、路由响应头、错误分类和 vLLM 指标采集测试。
3. 运行 A/B/C 三组首轮实验，生成 artifact 和第一版分析；在看到结果前不实现 eBPF、AIOps、Shadow 或 Operator。

这三个交付物完成后，再根据数据决定是否进入 EndpointSlice 和附加项，而不是按概念清单自动扩展。
