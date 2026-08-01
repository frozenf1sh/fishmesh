# FishMesh 代码架构

> 方向与验收边界见 [`project-charter.md`](project-charter.md)。本文件只描述代码依赖和运行时
> 演进方式。

## 1. 架构决策

当前项目不采用完整 DDD。它还没有复杂聚合生命周期、事务边界、持久化仓储或跨服务领域
事件；此时引入 Repository、Factory、Event Bus 会增加抽象而不是提高可维护性。

采用的模式是：

> **限界上下文（Bounded Context）+ 轻量整洁架构（Clean Architecture）+ 组合根（Composition Root）**

每个可运行服务的 `cmd/*/main.go` 是组合根，负责注入配置、时钟、HTTP client、策略和
适配器。业务包不读取环境变量、不创建全局 HTTP client、不直接调用 Kubernetes 或 LLM API。

## 2. 当前上下文

```text
Serving Context
  gateway -> serving/routing -> serving/endpoint
                    \\-> serving/transport

Diagnostics Context
  delivery -> application -> domain
                 ^             ^
                 |             |
              adapters --------

Workload Context
  workload/loadgen（独立实验客户端，不依赖 gateway 内部实现）
```

### Serving Context

- `internal/serving/gateway`：请求路径应用服务和 HTTP/SSE 交付；负责生命周期、fallback、metrics。
- `internal/serving/routing`：纯函数式策略；只认识 `Backend` 和 `Snapshot`。
- `internal/serving/endpoint`：后端发现端口；提供静态 resolver 和 namespace-scoped
  EndpointSlice watcher，二者由 Gateway 配置选择。
- `internal/serving/transport`：HTTP client/keep-alive 生命周期，不参与路由决策。

### Diagnostics Context

- `domain`：Incident、Signal、Diagnosis、Recommendation 和规则策略；不依赖基础设施。
- `application`：Tool、Registry、Engine；编排一次诊断用例。
- `adapters`：demo fixture、Gateway/vLLM/GPU Prometheus parser、Kubernetes Events/Pod collector；网络 collector 只有在实验确认需求后才增加。
- `delivery`：HTTP API；只负责输入校验、超时和 JSON 输出。
- `config`：环境变量映射与启动时校验。

Diagnostics Context 是冻结的次要组件。request-path reliability、EPP/llm-d 集成和自动 E2E
完成前，不增加新的 collector、Agent 或执行权限。

## 3. 依赖规则

允许的方向：

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

当前 standalone Gateway 同时拥有 HTTP/SSE 生命周期和 endpoint selection，便于开发、故障
注入和策略测试。它不是长期生产入口，不扩展 TLS、认证、tenant、通用 rate limit 或
Gateway API 控制面。

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

### N5：无 GPU conformance harness

controlled backend simulator 提供 delay、queue、stream、error、disconnect 和 endpoint churn。
它用于 CI 中验证 scheduler/transport/fallback 状态机；真实 vLLM 集群只承担集成验证。

### N6：EPP/llm-d 集成

先用 ADR 记录当前上游协议、插件接口、失败模式和版本约束，再选择 lightweight EPP、llm-d
scheduler plugin 或协议兼容 adapter。集成层只翻译请求和 endpoint snapshot，不重新实现
scheduler core。

### N7：可操作性

增加请求到路由决策再到 upstream outcome 的 trace/log correlation、dashboard、alerts 和
runbook；之后才执行有限的开源 scheduler 性能对照。Replay、自动 actuator、eBPF 和 CRD
继续延期。
