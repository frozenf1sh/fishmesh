# 阶段 16：R5C llm-d 适配器与 EPP 组合根

## 1. 本阶段解决了什么

R5B 只回答了“应该接入哪一个开源运行时”。R5C 把这个决定变成了可编译、可测试的最小垂直
切片：

- FishMesh 的 `session-key-v1` 已能作为 llm-d Router v0.9.0 插件运行；
- 新的 `fishmesh-epp` 二进制先注册插件，再启动未经修改的上游 EPP runner；
- llm-d 的 request、endpoint、queue 和 in-flight 数据被翻译为 FishMesh 的纯 `routing.Snapshot`；
- 选择结果投影为 llm-d scorer 分值，并在 response hook 中区分“选择的 endpoint”和“retry 后
  实际服务的 endpoint”；
- 最小 `EndpointPickerConfig` 已进入 Kustomize 渲染门禁。

本阶段没有访问 GPU 集群，也没有把配置契约描述成已经部署的生产栈。

## 2. 请求经过哪些步骤

```text
llm-d InferenceRequest + candidate Endpoints
              |
              v
FishMesh Filter
  - 地址/端口是否合法？
  - required in-flight snapshot 是否存在？
              |
              v
translation_impl
  - 生成与 standalone discovery 一致的 backend ID
  - 新鲜 queue -> observation sample
  - 缺失/过期 queue -> invalid sample（不是 0）
              |
              v
routing.SessionKey.Select
  - routing key -> preferred backend
  - queue/in-flight 超过硬边界 -> least-loaded
              |
              v
FishMesh Scorer
  - selected endpoint = 1
  - other endpoints = 0
              |
              v
llm-d max-score-picker + ext_proc lifecycle
```

只有一个 FishMesh scorer 时，`1/0` 是选择投影，不是新的“加权综合评分”。这样
`inflightDelta` 和 `queueDepthDelta` 仍然是明确的硬边界。

## 3. 为什么插件同时实现 Filter 和 Scorer

llm-d 把调度拆成 `Filter -> Scorer -> Picker`：

- Filter 负责候选是否具备做决定所需的最小事实；
- Scorer 负责在合法候选中表达 FishMesh 已做出的选择；
- Picker 继续复用上游 `max-score-picker`。

FishMesh 要求 in-flight 是 required data，不允许把缺失值解释成零负载。插件通过
`ConsumerPlugin.Consumes` 声明依赖，llm-d 自动创建默认 `inflight-load-producer`；Filter 又在
运行时守住同一不变量。queue 是可选的：只有每个 eligible endpoint 都有新鲜 queue sample 时
才参与比较，否则整个 queue 维度被忽略，仍可使用可靠的 in-flight 数据。

## 4. 类型翻译规则

| llm-d 输入 | FishMesh 输入 | 规则 |
| --- | --- | --- |
| request header | routing key | header 名可配置，启动时统一为小写 |
| endpoint address + port | `backend.Backend` | 使用公共 `backend.NewHTTP`，两种 adapter 得到同一稳定 ID |
| `InFlightLoad.Requests` | `Snapshot.Inflight` | required、不可为负；缺失 endpoint 被 Filter 排除 |
| `WaitingQueueSize` | `Observation.QueueLength` | 仅在时间戳有效、未超龄、非负时标记 valid |
| endpoint membership | session-key registry reconcile | 删除 endpoint 后同步清理对应 preference |
| routing decision | scorer map | 选中为 1，其余为 0 |
| served metadata | response provenance | 与 selected ID 分开记录，避免 retry 后错误归因 |

稳定 ID 的构造先下沉到了原子的 `backend` 包；EndpointSlice discovery 和 llm-d adapter 都调用
同一个构造函数。否则相同 `podIP:port` 在两种入口中可能得到不同 ID，所谓 conformance 只会
比较两个互不相干的状态空间。

## 5. 状态与并发

session-key registry 仍是单进程、有 TTL 和容量上限的内存状态。两个 EPP 副本在候选集
相同时会因 Rendezvous Hash 得到同一初始 preferred，但扩缩容期间的 TTL stickiness 不跨进程
同步。

R5C 没有为这个限制引入 Redis 或数据库，原因是当前没有部署证据证明共享状态值得增加新的
故障域。测试已经确认：

- 稳态候选集下两个插件实例选择相同；
- endpoint 删除后不会继续选择旧 endpoint；
- spillover 不改写 preferred，压力消失后恢复亲和；
- 同一插件并发执行 `Score` 能通过 race detector。

## 6. 故障语义没有被混在一起

standalone 与 integrated 只共享纯选择策略，不共享 delivery 故障处理：

| 场景 | standalone | integrated |
| --- | --- | --- |
| EndpointSlice/InferencePool 没有候选 | Kubernetes Service fallback | llm-d 返回 503，不绕过 subset |
| queue 缺失或过期 | 忽略 queue，继续用可靠信号 | 同样忽略 queue，继续用 required in-flight |
| in-flight 生产失败 | 本地 lease 仍由 requestpath 持有 | Filter 无有效候选，调度失败，不伪造零值 |
| upstream retry | standalone 不在响应开始后透明 retry | retry 由上游控制，FishMesh 分别记录 selected/served ID |
| stream 结束或取消 | requestpath lease 结算 | llm-d request lifecycle 释放 in-flight |

测试锁定了 pinned llm-d 的 `ServiceUnavailable -> HTTP 503 ImmediateResponse` 映射，并验证空/非法
候选无法越过调度 profile。完整 Envoy/Gateway 的 wire-level 503 将在 R5D 部署切片中验证。

## 7. 代码与配置在哪里

```text
internal/serving/llmd/
├── llmd.go                 # 插件类型、参数、注册和构造
├── scorer_impl.go          # Filter/Scorer/response hook
├── translation_impl.go     # llm-d -> FishMesh 值对象
├── llmd_test.go
└── scorer_impl_test.go

cmd/fishmesh-epp/
├── main.go                 # signal 和进程退出
├── composition.go          # 注册插件，再启动上游 runner
└── composition_test.go

deploy/integrated/llmd-config/
├── epp-config.yaml         # 最小 EndpointPickerConfig
├── kustomization.yaml
└── README.md               # 当前边界与 R5D 待办
```

`internal/serving/architecture` 新增了自动 import 规则：`llmd` 只能依赖 FishMesh 的
`backend + observation + routing`，不能依赖 `requestpath`、`gateway`、`transport` 或
standalone config。

## 8. 为什么 Go 依赖明显变多

`fishmesh-epp` 直接复用完整 llm-d runner，因此 Go module 图会带入它的 Kubernetes API、Envoy
ext_proc、gRPC、OpenTelemetry、Gateway API Inference Extension 和可选 data-layer 实现。这个
代价是真实存在的，但它与“FishMesh 自己实现了很多框架”不是一回事：新增能力仍被限制在一个
adapter 和一个组合根中，版本精确固定为 v0.9.0。

如果只 import 几个接口，依赖会更少，但无法交付真正可启动的 EPP；如果复制 runner，又会得到
更难升级的 fork。当前选择以较大的构建依赖换取标准协议和生命周期复用。后续升级必须单独提交，
先跑 adapter contract tests，再检查 module diff，不能无审查跟踪上游 main。

## 9. 如何验证

本阶段本地门禁为：

```bash
go test -race ./...
go vet ./...
go build ./...
make manifest
```

Docker 中使用 `CGO_ENABLED=0`，所以还额外对 Linux `amd64` 与 `arm64` 执行了
`fishmesh-epp` 交叉编译，避免本机 macOS 构建成功而容器构建失败。

适配器测试额外覆盖：参数严格解析、required data 声明、指标 freshness、in-flight/queue
spillover、endpoint churn、多实例确定性、并发、纯 routing 选择/reason conformance、上游 profile
边界和 503 映射。

## 10. 明确没有完成什么

- 没有安装 Gateway API Inference Extension CRD 或 Gateway Controller；
- 没有创建完整 EPP Deployment、Service、RBAC、InferencePool 和 Gateway；
- 没有执行 Envoy `ext_proc` wire-level conformance；
- 没有声称 llm-d 集成路径已经在 GPU/K3s 上运行；
- 没有加入共享 affinity 数据库、P/D disaggregation 或精确 KV block cache。

## 11. 下一阶段：R5D

R5D 负责把“可编译插件”变成“可部署的标准请求路径”：

1. 固定并安装 GIE v1.5.0 CRD 与选定的 Envoy-compatible Gateway 实现；
2. 补齐 EPP Deployment/Service/RBAC、ConfigMap mount、InferencePool 和 Gateway route；
3. 在无 GPU simulator 上完成 ext_proc 请求、空 subset 503、取消、retry served endpoint 和
   endpoint churn smoke；
4. 再决定是否需要真实 GPU 兼容 smoke，避免把部署调试与模型性能实验混在一起。
