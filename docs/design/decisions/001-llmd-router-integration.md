# ADR-001｜以 llm-d Router 编译期插件接入标准 EPP

- 状态：Standard mode 决策继续有效；产品主次与精确 KV 范围已由
  [`ADR-002`](002-lite-kv-aware-routing.md) 修订
- 决策日期：2026-08-10
- 上游快照：Gateway API Inference Extension v1.5.0、Endpoint Picker Protocol v1.0.0、
  llm-d Router v0.9.0

> 2026-08-11 修订说明：本 ADR 关于“不自研 ext_proc、固定 llm-d release、复用 InferencePool
> 和 response lifecycle”的决定继续有效。文中“standalone 只用于开发/演示”以及“精确 KV
> block cache 不进入项目”的范围已被 ADR-002 替代；R5D 标准部署顺序调整到 Lite KV-aware
> MVP 之后。以下正文保留当时的决策背景，避免改写历史。

## 1. 要决定什么

FishMesh 已有 standalone Go Gateway、纯 routing 策略和完整的 HTTP/SSE 请求生命周期。下一步
不是再写一个入口，而是回答：怎样把 FishMesh 的有界亲和策略接入主流 Kubernetes 推理网关，
同时不复制已有开源能力？

本 ADR 只决定 integrated runtime 的边界。它不证明性能更好，也不在本阶段部署新组件。

## 2. 已核对的上游事实

### 2.1 EPP 是一个完整的流式协议，不是一次普通 RPC

[Endpoint Picker Protocol v1.0.0](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/v1.5.0/docs/proposals/004-endpoint-picker-protocol/README.md)
要求 EPP 实现 Envoy `ext_proc` 双向流，并支持流式请求与响应。数据面可以通过
`x-gateway-destination-endpoint-subset` 限定候选集；EPP 必须只从该集合选择。

选择结果需要同时写入 `x-gateway-destination-endpoint` header 和 `envoy.lb` dynamic metadata，
两处值不得不同。没有 ready endpoint 时返回 503，主动丢弃过载请求时返回 429。数据面还会在
响应阶段回报最终实际服务请求的 endpoint，供 EPP 处理 retry 后的真实结果。

因此，“只实现一个 Select gRPC 方法”并不构成协议兼容 EPP。自己实现协议会同时引入 body
stream、metadata、健康检查、retry endpoint 列表和响应生命周期状态机。

### 2.2 GIE 保留标准 API 与 conformance，完整调度器已迁往 llm-d

[Gateway API Inference Extension](https://github.com/kubernetes-sigs/gateway-api-inference-extension/tree/v1.5.0)
已经 GA。该仓库继续维护 `InferencePool`、轻量级 EPP（LWEPP）和 conformance；完整 EPP、
request control 与调度框架已迁移到
[llm-d Router](https://github.com/llm-d/llm-d-router/tree/v0.9.0)。LWEPP 的定位是参考和
conformance，不是 FishMesh 应该 fork 的生产调度器。

`InferencePool` v1 还定义了两个重要边界：

- selector 只选择同 namespace Pod，每个 `podIP:targetPort` 是一个独立 endpoint；
- EPP 无响应时通过 `endpointPickerRef.failureMode` 选择 `FailOpen` 或 `FailClose`，默认
  `FailClose`。

### 2.3 llm-d 已拥有 FishMesh 不应复制的运行时能力

[llm-d Router 调度框架](https://github.com/llm-d/llm-d-router/blob/v0.9.0/docs/architecture.md)
已经提供：

- OpenAI 等请求解析和 `ext_proc` 生命周期；
- InferencePool endpoint discovery 与 subset 过滤；
- endpoint metrics data layer；
- admission、flow control 和请求结束清理；
- `Filter -> Scorer -> Picker` 调度链；
- response hook 中的实际 served endpoint。

FishMesh 的策略需要的信号也已有明确入口：`Scorer` 可以读取请求 header、endpoint 的
`WaitingQueueSize`，并通过 `ConsumerPlugin` 消费 `inflight-load-producer` 提供的实时在途请求数。

### 2.4 插件是编译期扩展，不是稳定的动态 ABI

v0.9.0 暴露了 `plugin.Register(type, factory)`、`runner.NewRunner().Run(ctx)` 和公开的调度接口。
因此可以构建一个 FishMesh EPP 命令：先注册 out-of-tree scorer，再启动上游 Runner，无需 fork
llm-d 仓库。

但这不是 Go `.so` 或网络插件。插件与 Runner 编译成同一个二进制，并直接依赖上游 Go 类型。
截至 2026-08-10，上游 main 已把注册签名改为带 stability 参数的形式，说明 minor release 间
仍可能发生源码不兼容。FishMesh 必须 pin release，不能跟踪 main。

## 3. 方案比较

| 方案 | 优点 | 主要问题 | 结论 |
| --- | --- | --- | --- |
| 继续扩展 standalone Gateway | 已有代码最直接 | 复制 Gateway API、TLS、通用流控和生态集成 | 只保留开发/演示 |
| 自研 `ext_proc` EPP | 完全控制协议 | 重写双向流、metadata、retry、健康和生命周期 | 拒绝 |
| fork LWEPP | 起步代码少 | 参考实现定位，仍需自行维护生产调度框架 | 拒绝 |
| 仅配置 llm-d 内置 scorer | 无自定义二进制 | 可作为开源对照，不能表达 FishMesh 的硬边界 spillover 语义 | 保留为 baseline |
| llm-d out-of-tree scorer + 自定义入口 | 复用完整运行时，只实现差异化策略 | 与上游 Go API 编译期耦合，需要版本矩阵 | 采用 |

## 4. 决策

FishMesh 选择：

> 基于固定版本的 llm-d Router 构建自定义 EPP 二进制；FishMesh 只提供一个编译期注册的
> session-key scorer/adapter，不实现 `ext_proc`、discovery、proxy 或 flow control。

目标请求路径为：

```text
client
  -> Envoy-compatible Gateway
  -> llm-d Router ext_proc runtime
       -> InferencePool candidates / subset
       -> llm-d data producers and metrics
       -> FishMesh session-key scorer
       -> llm-d picker and request lifecycle
  -> vLLM endpoint
```

初始版本固定：

| 组件 | R5C 基线 | 约束 |
| --- | --- | --- |
| Gateway API Inference Extension | v1.5.0 API/conformance | 不复制 CRD 类型 |
| Endpoint Picker Protocol | v1.0.0 | 由 llm-d 实现 |
| llm-d Router | v0.9.0 | 精确 pin，不使用 main |
| Go | FishMesh 1.26 | v0.9.0 的最低版本是 1.25.11，可向上构建 |
| vLLM | 0.23.0 | 先做兼容验证，不因本阶段再次升级 |

## 5. 能力所有权

### FishMesh 继续拥有

- cooperative routing key 到 preferred endpoint 的确定性映射；
- preferred 与 least-loaded 之间的 queue/in-flight 硬边界；
- 决策 policy、reason 和 spillover reason；
- 纯策略测试及 standalone/integrated 的选择 conformance fixture。

### llm-d / Gateway 负责

- `ext_proc` wire protocol、request body parser 和 response streaming；
- InferencePool、endpoint subset、endpoint metrics 与请求在途生命周期；
- admission、429/503、retry endpoint 和实际 served endpoint；
- gRPC health、Gateway 到 EPP 的连接和 data-plane failover。

### 明确不共享的运行时状态

standalone 的 `requestpath` 不能被 EPP adapter 直接调用。它拥有 EndpointSlice resolver、
Prometheus reader、local circuit、Service fallback 和 lease；llm-d 已有对应的数据与生命周期。
在 EPP 中再启动一次会产生双重事实源，并可能选择 subset 之外的 endpoint。

因此两种模式只共享 `internal/serving/routing` 的纯选择语义。adapter 把 llm-d request/endpoint
翻译为 routing snapshot，再把选择投影为 scorer 结果。协议和运行时故障分别由各自 delivery
层测试。

## 6. 故障语义

| 场景 | integrated 初始行为 | 原因 |
| --- | --- | --- |
| subset 为空或没有 eligible endpoint | 503 | EPP 协议要求，禁止 Service fallback 绕过 subset |
| llm-d flow control 决定丢弃 | 429 | 由上游 admission/flow control 负责 |
| EPP 无响应 | `FailClose` | 初期优先保证策略没有被静默绕过 |
| queue 数据缺失或过期 | 不使用 queue，只使用可用的 in-flight 信号 | 不把缺失误判成零负载 |
| client 取消或 stream 结束 | 由 llm-d lifecycle/TTL 释放在途状态 | 不再创建第二个 FishMesh lease |
| data-plane retry 到另一个 endpoint | 以 served endpoint hook 为真实结果 | 不能把首选 endpoint 当成实际服务者 |

`FailOpen` 以后可以作为显式可用性选项评估，但必须有指标和告警说明请求绕过了 FishMesh
策略；它不是初始默认值。

## 7. 多副本与 affinity 状态

当前 session-key-v1 的 registry 是进程内状态。Rendezvous Hash 本身在候选集一致时能让
多个 EPP 副本得到相同 preferred endpoint，但 registry 在 endpoint 扩容后的 TTL stickiness
不能跨副本一致。

R5C 不引入 Redis、数据库或 EPP 粘性会话。实现时必须把这个限制写入指标和文档，并用测试
区分：

- 稳态候选集：多实例应得到相同 preferred；
- endpoint 增删：允许最小重映射，但不能选择已删除 endpoint；
- spillover：只影响当前请求，不改写 preferred。

若后续真实部署证明跨副本 registry 差异会造成问题，优先评估移除 registry、使用完全无状态的
Rendezvous preference；不默认增加共享存储。

## 8. R5C 实现边界与验收（已完成）

R5C 已完成以下最小垂直切片：

1. 新建 `internal/serving/llmd` adapter 能力包，按 FishMesh 文件规范组织；
2. 实现并单测参数解析、request/header 翻译、endpoint/metrics 翻译和 score 投影；
3. 新建 `fishmesh-epp` 组合根，注册插件后启动 pinned llm-d Runner；
4. 提供最小 `EndpointPickerConfig`，只启用 FishMesh scorer 和上游默认 picker/lifecycle；
5. 在无 GPU 环境编译并运行 plugin contract/race tests；
6. 用 fixture 验证相同候选与相同 queue/in-flight 输入下，纯 routing 与 integrated 的
   selection/reason 一致；
7. 验证空/非法候选不能越过 llm-d profile，并锁定上游 `ServiceUnavailable -> 503`
   ImmediateResponse 映射；完整 Envoy wire-level 503 留给 R5D 部署验证。

以下内容不进入 R5C：Gateway Controller 安装矩阵、P/D disaggregation、精确 KV block cache、
共享 affinity 数据库、动态 Go plugin、修改 llm-d 源码。

计划文件布局为：

```text
internal/serving/llmd/
├── llmd.go                 # 包说明、插件名、参数和 Register 入口
├── scorer_impl.go          # llm-d Scorer/ConsumerPlugin 实现
├── translation_impl.go     # request、endpoint、queue/in-flight 到 routing snapshot
├── llmd_test.go            # 参数与公开契约
└── scorer_impl_test.go     # 选择、缺失信号、并发和 endpoint churn

cmd/fishmesh-epp/
├── main.go                 # signal 与进程退出
├── composition.go          # 注册 FishMesh plugin 后启动上游 Runner
└── composition_test.go
```

`llmd` 是第三方运行时 adapter，因此可以 import llm-d；`routing` 不能反向 import `llmd`。该包
只有一个具体 scorer，不为目录对称创建 FishMesh 自有接口；它直接实现上游要求的接口并提供
编译期检查。

## 9. 升级规则

- `go.mod` 只依赖 release tag，不依赖 commit main；
- 每次 llm-d minor 升级单独提交，先跑 adapter compile/contract tests；
- 升级记录 Register/Factory、InferenceRequest、Endpoint、DataKey 和 response hook 的差异；
- 上游 API 破坏时优先修改薄 adapter，不把 llm-d 类型泄漏进 routing；
- 连续两个版本都需要 fork Runner 才能注册插件时，重新打开本 ADR，而不是静默维护 fork。

## 10. 结果与代价

这个决策让“为什么不用开源技术”有清晰答案：FishMesh 复用成熟 EPP、Gateway 和请求控制，
只实现可解释的有界选择策略及其工程验证。

代价是构建依赖明显变大，并且 adapter 需要跟踪 llm-d 的 Go API。通过精确版本 pin、薄翻译层、
独立升级提交和 conformance tests，可以把这项代价限制在一个明确边界内。
