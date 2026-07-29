# FishMesh 代码架构与下一阶段计划

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
- `internal/serving/endpoint`：后端发现端口；当前是静态 resolver，后续替换为 EndpointSlice。
- `internal/serving/transport`：HTTP client/keep-alive 生命周期，不参与路由决策。

### Diagnostics Context

- `domain`：Incident、Signal、Diagnosis、Recommendation 和规则策略；不依赖基础设施。
- `application`：Tool、Registry、Engine；编排一次诊断用例。
- `adapters`：demo fixture、Gateway/vLLM/GPU Prometheus parser、Kubernetes Events/Pod collector；未来添加 eBPF collector。
- `delivery`：HTTP API；只负责输入校验、超时和 JSON 输出。
- `config`：环境变量映射与启动时校验。

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

## 4. 为什么不是进一步拆成更多服务

当前 Gateway、Loadgen、Analyst 的运行时边界已经清晰，但 EndpointSlice watcher、指标
采集器和 Agent actuator 尚未成熟。现在只收紧 Go 包边界，不增加进程、数据库、消息队列
或 CRD。等出现独立扩缩容、独立权限或故障隔离需求时，再拆成服务或 Operator。

## 5. 下一阶段实施顺序

### N1：完成真实观测适配器

当前已在 `internal/diagnostics/adapters` 实现三个只读 collector：

1. vLLM `/metrics`：queue、running、TTFT、Prefix Cache；
2. GPU/DCGM Prometheus endpoint：利用率、显存、温度、OOM；
3. Kubernetes Events/Pod 状态：namespace-scoped、最小 RBAC。

每个 collector 都实现 `application.Tool`，失败返回 `SignalUnavailable`，不把缺失数据
当作正常值；缺失关键观测时诊断结果为 `insufficient_observability`。

### N2：EndpointSlice resolver

将 `internal/serving/endpoint` 的静态实现替换为 namespace-scoped EndpointSlice watcher，
保持 `Resolver` 接口和 Gateway 请求协议不变。先做只读缓存、Ready 过滤、原子快照和
Service fallback，再做故障演练。

### N3：Hybrid Scheduler

把当前 Service、Prefix Affinity、Load-aware 三个策略统一成可解释 score policy，输入
仅使用经过采样/聚合的 backend snapshot；Agent 只能提出 Recommendation，不直接改策略。

### N4：诊断闭环

规则策略先覆盖局部性退化、GPU 饱和、服务端排队、网络退化和 endpoint 故障；之后再增加
LLM narrator，把结构化 Diagnosis 翻译为报告。LLM 不拥有工具执行权限。

### N5：Shadow 与 guarded actuator（附加项）

只有 N1-N4 的证据链稳定后，才考虑 replay、策略 shadow 和带 TTL/审计/回滚的受控变更。
