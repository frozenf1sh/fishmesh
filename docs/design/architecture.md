# FishMesh 代码架构

> 方向与验收边界见 [`project-charter.md`](project-charter.md)。本文件只描述代码依赖和运行时
> 演进方式。

## 1. 架构决策

当前项目不采用完整 DDD。它还没有复杂聚合生命周期、事务边界、持久化仓储或跨服务领域
事件；此时引入 Repository、Factory、Event Bus 会增加抽象而不是提高可维护性。

采用的模式是：

> **限界上下文（Bounded Context）+ 轻量整洁架构（Clean Architecture）+ 组合根（Composition Root）**

每个可运行服务的 `cmd/*` 是组合根，负责注入配置、时钟、HTTP client、策略和适配器。
`cmd/fishmesh-gateway/composition.go` 已显式创建全部 Serving 实现；Gateway 不再隐藏装配逻辑。
迁移记录见 [`serving-domain-redesign.md`](serving-domain-redesign.md)。

## 2. 当前上下文

```text
Serving Context（当前）
  cmd/composition -> config + gateway + all concrete implementations
  gateway --------> admission + requestpath + transport
  requestpath ----> discovery + observation + routing + circuit
  discovery ------> backend
  identity -------> backend
  observation ----> backend + identity
  routing --------> backend + observation
  circuit --------> backend
  transport ------> backend

Diagnostics Context
  delivery -> application -> domain
                 ^             ^
                 |             |
              adapters --------

Workload Context
  workload/loadgen（独立实验客户端，不依赖 gateway 内部实现）

Simulator Context
  cmd/fishmesh-simulator -> internal/simulator -> standard library only
```

### Serving Context

- `internal/serving/gateway`：standalone HTTP/SSE delivery 与 Prometheus 投影，不创建具体 domain。
- `internal/serving/config`：一次性解析环境变量，并按 owner 输出各 domain Config。
- `internal/serving/admission`：进程内非阻塞请求许可，不建立等待队列。
- `internal/serving/requestpath`：fallback、选择 lease、local in-flight、circuit outcome 和成员回收。
- `internal/serving/backend`：最原子的 backend ID、地址和稳定 metadata 值对象。
- `internal/serving/routing`：纯策略；只解释 backend/observation snapshot 并返回 typed decision。
- `internal/serving/circuit`：per-backend transport outcome EWMA 和临时 open state。
- `internal/serving/discovery`：后端发现端口；提供静态 resolver 和 namespace-scoped
  EndpointSlice watcher，二者由 Gateway 配置选择。
- `internal/serving/identity`：backend 到 Pod/Node/声明资源的映射。
- `internal/serving/observation`：per-field sample、freshness 和慢速 Prometheus 采集循环。
- `internal/serving/transport`：HTTP client/keep-alive 生命周期，不参与路由决策。

R1–R4 已完成：类型 owner、I/O contract、requestpath 编排、Gateway delivery 和组合根均已分离，
自动测试会约束上述 import。后续 simulator/EPP adapter 直接复用 requestpath，不复制 standalone
Gateway 的 HTTP 代码。
强制文件布局、字面量规则和完整依赖矩阵见
[`code-organization.md`](code-organization.md) 与
[`serving-domain-redesign.md`](serving-domain-redesign.md)。

### Diagnostics Context

- `domain`：Incident、Signal、Diagnosis、Recommendation 和规则策略；不依赖基础设施。
- `application`：Tool、Registry、Engine；编排一次诊断用例。
- `adapters`：demo fixture、Gateway/vLLM/GPU Prometheus parser、Kubernetes Events/Pod collector；网络 collector 只有在实验确认需求后才增加。
- `delivery`：HTTP API；只负责输入校验、超时和 JSON 输出。
- `config`：环境变量映射与启动时校验。

Diagnostics Context 是冻结的次要组件。request-path reliability、EPP/llm-d 集成和自动 E2E
完成前，不增加新的 collector、Agent 或执行权限。

### Simulator Context

`internal/simulator` 拥有可控 upstream 行为和请求计数，只实现 OpenAI HTTP/SSE、最小 vLLM
Prometheus 指标以及测试控制 API。它不 import Serving，因此不会为迁就 Gateway 内部类型而失真。
`cmd/fishmesh-simulator` 只负责监听、信号和优雅关闭。控制 API 仅用于本地/CI，不属于生产面。

## 3. 依赖规则

Serving 的原子 domain 依赖由 `internal/serving/architecture` 自动检查，当前为：

```text
backend     -> standard library only
admission   -> standard library only
circuit     -> backend
routing     -> backend + observation
identity    -> backend + platform/kubernetes
observation -> backend + identity
transport   -> backend
discovery   -> backend + platform/kubernetes
requestpath -> backend + discovery + observation + routing + circuit
gateway     -> admission + requestpath + transport（metrics 只投影 typed state）
config      -> 各 domain Config（不创建运行时资源）
simulator   -> standard library only
```

Diagnostics Context 允许的方向：

```text
cmd -> delivery/application/adapters/config
delivery -> application + domain
adapters -> application ports + domain
application -> domain
domain -> standard library only
```

禁止的方向：

- domain 依赖 HTTP、Prometheus、Kubernetes client 或 LLM SDK；
- Gateway handler 直接创建 routing/transport 实现；
- Analyst Tool 执行任意 shell/kubectl；
- Loadgen 依赖 Gateway 的内部包；
- 每请求调用 LLM。

## 4. 运行时边界

当前 standalone Gateway 只拥有 HTTP/SSE delivery；endpoint selection 由可复用 requestpath
完成。该运行时便于开发、故障注入和策略测试，但不是长期生产入口，不扩展 TLS、认证、tenant、
通用 rate limit 或 Gateway API 控制面。

目标 integrated runtime 复用 Envoy-compatible Gateway 和 EPP/llm-d 扩展边界。两种运行时
共享 `internal/serving/routing` 及其状态契约，不复制策略实现：

```text
standalone: client -> gateway delivery -> scheduler core -> transport -> backend
integrated: client -> external gateway -> EPP adapter -> scheduler core -> backend
```

只有出现独立扩缩容、独立权限或故障隔离需求时才增加进程。当前不引入数据库、消息队列、
CRD 或 Operator。

## 5. 已完成的架构基础

### N1：真实观测适配器

当前已在 `internal/diagnostics/adapters` 实现三个只读 collector：

1. vLLM `/metrics`：queue、running、TTFT、Prefix Cache；
2. GPU/DCGM Prometheus endpoint：利用率、显存、温度、OOM；
3. Kubernetes Events/Pod 状态：namespace-scoped、最小 RBAC。

每个 collector 都实现 `application.Tool`，失败返回 `SignalUnavailable`，不把缺失数据
当作正常值；缺失关键观测时诊断结果为 `insufficient_observability`。

### N2：EndpointSlice resolver

已完成第一版 namespace-scoped EndpointSlice watcher：使用最小 `get/list/watch` RBAC，
只接受 Ready 的 IPv4/IPv6 地址，按地址和端口生成稳定 backend ID，使用锁保护快照，并
在 watch 断开后重新 list。Gateway 的 `Resolver` 接口和请求协议没有改变；动态 backend
URL 不再依赖静态 Pod IP 映射，非法或缺失地址会回退到 Service。Kustomize 实验入口为
`deploy/experiments/endpoint-slice`，默认 baseline 仍然是 Service + keep-alive。

2026-08-09 已在真实 K3s 验证：EndpointSlice 返回两个 Ready vLLM 地址，Gateway Pod 成功
通过 ServiceAccount 读取资源，`/v1/models` 返回 `X-FishMesh-Routing-Mode: prefix-affinity`
和稳定的 `X-FishMesh-Backend-ID: endpoint-*`。验证后已恢复 baseline 并删除实验 RBAC。

### N3：Bounded Affinity Scheduler

Backend Snapshot 第一版已完成：EndpointSlice backend ID 与每个 vLLM `/metrics` 的 queue、
running、Prefix Cache、TTFT 和 vLLM GPU cache usage 已对齐，并暴露 freshness/error。
bounded-affinity-v1 也已实现并通过真实 K3s smoke：只保存 cooperative routing key 的
SHA-256，通过 Rendezvous Hash 选择 preferred backend，以独立 queue/local-inflight delta
执行 spillover；缺失或不完整 queue snapshot 不参与决策，discovery 不可用、无 Ready backend
或过期时回退 Service。spillover 不改写 preferred，因此压力恢复后能恢复亲和。

下一步不做 GPU exporter label 对齐或任意多指标加权。当前 time-slicing 环境不能把 device
利用率可靠归因到单个 Pod。N3 剩余工作是 recent transport error circuit、endpoint 状态和
transport GC、admission/connection 上限，以及把 backend-level status 拆成 per-field sample。
累计 TTFT/Prefix Cache 和 node GPU metrics 继续留在慢速证据路径。

阶段 04 已完成身份与故障状态基础：backend 保留 Pod targetRef，Pod list 映射 Node 和声明
GPU request；EndpointSlice status/freshness 进入 Prometheus，缓存超过 max age 后 readiness
返回 503；watch 之外增加周期性 relist，RBAC 恢复后可自动回到 `ok/200`。实时 GPU 利用率
在当前集群只作为 node-level health signal，不进入 per-backend score。

## 6. 下一阶段架构演进

### N4：可靠性状态机（已完成）

已补齐 transport error circuit、endpoint state GC、admission/connection bounds 和用于
queue/running 的 per-field sample。endpoint lifecycle 统一回收 client、counter、affinity、
circuit、observation 和 Prometheus label；client cancellation 不计为 backend failure，响应头
发出后的 stream failure 不重试。

### N4.5：Serving Domain 边界整理（已完成）

在 simulator 和 EPP adapter 扩展请求路径之前，按 R1–R4 渐进迁移现有代码。R1 已拆出
backend/identity/observation 类型 owner、纯 routing contract、独立 circuit 和自动 import 门禁；
R2 已完成 discovery 与其他 I/O 能力整理，R3 已提取 requestpath lease、fallback、成员同步和
circuit outcome，R4 已拆分 Gateway/admission/config，并把实现创建移到显式组合根。
迁移只改变代码组织，不改变已验证的 route reason、fallback、timeout、connection 或 circuit
语义。

### N5：无 GPU conformance harness（基础已完成）

controlled backend simulator 已提供 delay、queue/running、HTTP error、stream abort、held stream
和运行时控制 API。进程级 E2E 已覆盖 slow SSE、admission、cancellation、circuit fallback、
EndpointSlice removal/stale，并验证 observation collector 能解析 simulator 指标。真实 vLLM 集群
只承担集成验证；N5 剩余工作是长时间 churn/soak，以及 integrated adapter 落地后的共享
selection/reason conformance suite。

### N6：EPP/llm-d 集成

先用 ADR 记录当前上游协议、插件接口、失败模式和版本约束，再选择 lightweight EPP、llm-d
scheduler plugin 或协议兼容 adapter。集成层只翻译请求和 endpoint snapshot，不重新实现
scheduler core。

### N7：可操作性

增加请求到路由决策再到 upstream outcome 的 trace/log correlation、dashboard、alerts 和
runbook；之后才执行有限的开源 scheduler 性能对照。Replay、自动 actuator、eBPF 和 CRD
继续延期。
