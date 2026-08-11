# 15｜R5B EPP 与 llm-d 集成决策

状态：上游协议调研、方案比较和架构决策已完成；本阶段只纠正了一处 Go 契约注释，没有修改
运行行为、部署清单或集群。

## 这一阶段为什么先写文档

R5A 已经能在没有 GPU 的情况下验证 FishMesh standalone Gateway。直接开始写 EPP adapter 看似
更快，但如果扩展点选错，会出现三类返工：

1. 重写 llm-d 已经实现的 `ext_proc`、请求解析和流控；
2. 把 standalone 的 EndpointSlice、Prometheus 和 circuit 再启动一份，形成双重事实源；
3. 把 Service fallback 搬进 EPP，违反 endpoint subset 协议。

所以 R5B 是一个 time-boxed integration spike：先用当前官方 release 和源码确认边界，再选择
唯一的实现路径。它是工程决策，不是论文调研。完整决策记录在
[`ADR-001`](../design/decisions/001-llmd-router-integration.md)。

## 先理解三个名词

### InferencePool

`InferencePool` 是 Gateway API Inference Extension 提供的 Kubernetes API。它用 selector 和
target port 描述一组模型服务器 endpoint，并引用一个 EPP。Gateway Controller 据此把请求、
候选 endpoint 和 EPP 接起来。

### EPP

Endpoint Picker（EPP）不是独立代理。它通过 Envoy `ext_proc` 双向 gRPC 流查看请求，选择一个
或多个 endpoint，再让 Gateway 直接把请求发到模型服务器。响应阶段 Gateway 还会把最终实际
服务请求的 endpoint 告诉 EPP。

### llm-d Router

llm-d Router 是当前完整的 EPP/request-control/scheduler 实现。它已经负责协议、endpoint、
metrics、flow control 和插件调度。FishMesh 只需要在它的 `Filter -> Scorer -> Picker` 链中
提供差异化策略。

## 调研使用了什么版本

本阶段按 2026-08-10 可获得的官方 release 固定事实：

| 项目 | 版本 | 本阶段查看内容 |
| --- | --- | --- |
| Gateway API Inference Extension | v1.5.0 | InferencePool v1、EPP Protocol v1.0.0、LWEPP 定位 |
| llm-d Router | v0.9.0 | 调度插件接口、Runner、data layer、in-flight producer |
| llm-d Router main | 2026-08-09 快照 | 只用于观察下一版本 API 变化，不作为依赖 |

官方资料：

- [GIE v1.5.0](https://github.com/kubernetes-sigs/gateway-api-inference-extension/tree/v1.5.0)
- [Endpoint Picker Protocol](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/v1.5.0/docs/proposals/004-endpoint-picker-protocol/README.md)
- [llm-d Router v0.9.0](https://github.com/llm-d/llm-d-router/tree/v0.9.0)
- [llm-d scheduling architecture](https://github.com/llm-d/llm-d-router/blob/v0.9.0/docs/architecture.md)
- [custom filter tutorial](https://github.com/llm-d/llm-d-router/blob/v0.9.0/docs/create_new_filter.md)

## 一次 integrated 请求实际经历什么

```text
1. Client 把 OpenAI-compatible 请求交给 Gateway。
2. Gateway 通过 ext_proc 把 headers/body 以流的形式交给 llm-d EPP。
3. llm-d 按 InferencePool 和 subset 得到本请求允许使用的 endpoint。
4. data producer 更新 queue、实时 in-flight 等数据。
5. FishMesh scorer 根据 routing key、preferred endpoint 和硬边界给 endpoint 打分。
6. llm-d picker 选择 endpoint，并按 EPP 协议把地址回写给 Gateway。
7. Gateway 直接连接 vLLM 并流式返回结果。
8. llm-d 在 response/end-of-stream hook 中释放 in-flight，并看到实际 served endpoint。
```

这里最重要的是：FishMesh 不再代理第 7 步，也不拥有第 2、3、6、8 步的状态机。

## 为什么不自己实现 EPP

EPP Protocol 要求的不只是“输入候选，输出一个地址”：

- request/response 都是流式 `ext_proc`；
- 必须尊重 data plane 下发的 endpoint subset；
- 结果同时写 header 和 dynamic metadata，并保持一致；
- 可以返回有序 retry endpoint；
- 没有 endpoint 返回 503，主动 shed 返回 429；
- 需要标准 gRPC liveness/readiness；
- response 阶段需要识别 retry 后真正服务请求的 endpoint。

FishMesh 如果实现这些代码，工程亮点会从“调度与故障语义”变成“维护一套不完整 Envoy 扩展”。
这正是应当复用开源的位置。

## 为什么不直接使用 llm-d 内置策略

llm-d v0.9.0 已有 session affinity、prefix cache、queue、active request、load aware 等 scorer。
它们会成为后续开源 baseline，而不是被忽略。

FishMesh 仍有一个明确而较小的差异：

- cooperative routing key 通过 Rendezvous Hash 得到稳定 preferred；
- preferred 没超过 queue/in-flight 硬边界时保持亲和；
- 超过任一边界时选择 least-loaded，但不改写 preferred；
- 每次决定有固定 policy/reason，而不是依赖多个浮点权重碰巧相加。

如果后续对照证明 llm-d 内置组合已经完全覆盖这个行为，并且可操作性更好，FishMesh 应删除
自定义 scorer，而不是为了项目存在感继续维护它。这也是下一阶段必须保留开源 baseline 的原因。

## 最终选择

采用“pinned llm-d Router + FishMesh 编译期 scorer + 自定义启动入口”：

```text
fishmesh-epp binary
├── llm-d Router v0.9.0：EPP/runtime/framework
└── FishMesh adapter：配置翻译 + session-key scorer
```

启动入口先调用 llm-d 的插件注册函数，再启动公开 Runner。它不是动态插件市场，也不是 fork；
它是一个把上游库和 FishMesh policy 编译到一起的定制发行物。

## 纠正了哪些旧假设

### 1. integrated adapter 不复用整个 requestpath

旧设计把 `requestpath` 当成两种入口的共同边界。源码核对后发现这不成立：

| requestpath 当前拥有 | llm-d 已经拥有 |
| --- | --- |
| EndpointSlice resolver | InferencePool/data layer discovery |
| Prometheus observation reader | metrics source/extractor |
| local in-flight lease | in-flight producer + stream cleanup |
| transport outcome circuit | EPP response lifecycle / Gateway failure handling |
| Kubernetes Service fallback | EPP 503/429 与 failureMode |

同时运行两套会造成不一致，而且 requestpath 可能选到 subset 之外的 endpoint。新的共享边界是纯
`routing` policy；standalone requestpath 和 llm-d adapter 分别把自己的运行时 snapshot 翻译给它。

### 2. 两种运行时不共享相同 fallback

standalone 在 discovery 过期时通过 Kubernetes Service fallback，保证开发模式可用。标准 EPP
收到空 subset 时必须返回 503，不能绕过 subset。

因此共享 conformance 的准确表述改为：

> 相同候选集、routing key、queue 和 in-flight 输入，得到相同 preferred、selected 和
> spillover reason。

候选集为空、EPP 掉线、retry 和 stream lifecycle 属于各 runtime 自己的 contract tests。

### 3. llm-d 插件 API 还不是稳定 ABI

v0.9.0 的注册函数是 `Register(type, factory)`；当前 main 已加入 stability 参数。这类源码级
变化说明不能写 `@main` 或浮动版本。后续升级必须是独立、可回滚的提交，并先通过 adapter
compile tests。

## 故障设计

| 故障 | R5C 目标行为 | 需要观测什么 |
| --- | --- | --- |
| endpoint subset 为空 | 503 | no-candidate 计数 |
| 所有 endpoint 过载且上游 shed | 429 | admission reason |
| EPP 不可达 | 初始 `FailClose` | Gateway/EPP availability |
| queue sample 缺失 | queue 不参与决定 | sample availability |
| request 被取消 | in-flight 最终释放 | active requests 回到基线 |
| retry 改变 endpoint | 记录 served endpoint | selected 与 served 分开 |
| endpoint 被删除 | 立即不再成为候选 | membership 与状态回收 |

`FailOpen` 会让 Gateway 在 EPP 故障时自己选择 endpoint，提升可用性但绕过策略。初始选择
`FailClose` 是为了先保证行为可解释；未来只有在有明确告警和绕过指标后才评估 `FailOpen`。

## 多副本限制

session-key-v1 的 registry 在进程内。多个 EPP 副本在稳定候选集上仍会因为 Rendezvous
Hash 得到相同 preferred；但 endpoint 扩容后的 TTL stickiness 不是全局一致的。

本项目不会为了这个尚未复现的问题立即引入 Redis。R5C 先验证稳定集合的一致性、endpoint
删除安全和最小重映射。若问题真实存在，优先把 preference 改成完全无状态，而不是增加共享
数据库。这能保持 FishMesh 是请求调度组件，而不是又长出一套状态服务。

## 本阶段改了什么

- 新增 ADR，固定集成路径、版本矩阵、能力所有权和升级规则；
- 更新总架构：integrated 只复用 routing，不复用 standalone requestpath；
- 更新 P2 计划和 README，明确不自研 EPP、LWEPP 不作为生产路径；
- 更新进度索引与项目状态，为 R5C 给出可执行验收清单。

本阶段没有新增 Go interface，也没有提前引入 llm-d 的大型依赖。这样可以保持 spike 与行为
实现分开，下一提交出现问题时能独立回滚。

## 如何验证本阶段结论

本阶段的验证分两类：

1. 事实核对：使用官方 release tag 检查协议、API、插件接口和 Runner 注册路径；
2. 仓库回归：运行 race tests、vet、build、全部 Kustomize manifest 和 `git diff --check`，确认
   文档纠偏没有夹带运行行为变化。

GPU/K3s 不参与这一阶段，因为没有需要真实模型或集群才能回答的问题。

实际结果：

- `go test -race ./...`：通过；
- `go vet ./...`：通过；
- `go build ./...`：通过；
- `make manifest`：全部 Kustomize 入口通过；
- `git diff --check`：通过。

## 下一阶段 R5C

R5C 是第一个 integrated 垂直切片，完成标准是：

1. llm-d v0.9.0 精确 pin；
2. adapter package 只有翻译和 scorer，不创建 Kubernetes/HTTP client；
3. `fishmesh-epp` 是唯一组合根，中文注释解释注册和关闭顺序；
4. queue 缺失、空 key、endpoint 增删、in-flight spillover 和并发测试通过；
5. 同一 fixture 下 standalone 与 adapter 的 endpoint/reason 一致；
6. 空 subset 走 503，不走 standalone Service fallback；
7. 所有新增生产文件遵守同包名入口、`*_impl.go`、声明顺序和 300/40 行约束；
8. 全量质量门禁通过后更新阶段 16、项目状态并独立提交推送。

R5C 不部署 GPU 集群。先用公开 llm-d 类型和 simulator 完成编译、插件契约和无 GPU 集成；
Gateway Controller/InferencePool 的真实 K3s 部署作为后续独立阶段，避免一次提交同时处理代码、
控制面安装和集群故障。
