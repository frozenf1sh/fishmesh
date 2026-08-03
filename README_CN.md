# FishMesh

> 一个 Kubernetes 原生的流量调度器，用于自托管 LLM 推理。

[English](README.md)

FishMesh 在动态的 vLLM 副本之间路由兼容 OpenAI 的流式请求。它在首选后端有容量时保持请求亲和性，在压力下将流量溢出到负载较轻的后端，并在端点发现不可用或过期时安全回退。

本项目专注于通用 Kubernetes Service 与模型服务器之间的请求服务问题：长连接流式请求、动态后端成员关系、过载隔离、路由状态生命周期以及可观测的故障语义。

## 为什么需要 FishMesh

Kubernetes Service 可以分发连接，但它不理解请求亲和性、模型服务器队列状态或长时间运行的 LLM 流的生命周期。纯粹的亲和性可以提高重复会话的复用率，但也可能让单个副本过载并放大长尾延迟。

FishMesh 实现了一种有界策略：

1. 仅发现 Ready 状态的 vLLM 端点，拒绝过期的发现状态；
2. 从协作式的请求/会话键中选择一个稳定的首选后端；
3. 在队列和在途请求限制保持在边界内时维持亲和性；
4. 当首选后端超过边界时，溢出到负载较轻的后端；
5. 记录每个请求为何被保持、溢出或通过 Service 回退发送。

## 当前能力

- 兼容 OpenAI 的 HTTP/SSE 代理，支持取消、完整的流排空和 TTFT 指标；
- 命名空间范围的 EndpointSlice watch/list，支持 Ready 过滤、定期重列举、新鲜度和 Kubernetes Service 回退；
- 每个后端的 vLLM 队列/运行中观测，带有显式的可用性和时效信息；
- `bounded-affinity-v1`：SHA-256 路由键存储、Rendezvous Hash、有界 TTL 注册表以及独立的队列/本地在途溢出阈值；
- 非阻塞准入、每后端连接上限、传输错误 EWMA 熔断以及 endpoint 范围的状态回收；
- Prometheus 指标和响应溯源，涵盖路由决策、发现状态、后端观测和溢出原因；
- 有界配置解析、就绪/存活探针、优雅关闭、最小权限 RBAC 以及经过竞态检测的调度器/发现路径；
- 确定性的负载生成器和 K3s 验证工作负载，用于验证系统行为；
- 无需 GPU 的受控 backend simulator，可注入延迟、HTTP 错误、流中断、持续流和 vLLM 观测值。

## 运行时架构

```text
OpenAI 客户端
  -> FishMesh 请求生命周期
       -> 端点资格判定
       -> 有界亲和性调度器
       -> 每个后端的传输层
       -> vLLM 副本

EndpointSlice -----> 不可变端点快照 ----+
vLLM 指标 ---------> 后端观测快照 -----+-> 调度器
本地结果 -----------> 在途/错误状态 ---+

Prometheus <-------- 决策、故障和延迟
```

当前的独立 Go Gateway 是 HTTP/SSE delivery adapter；可复用的 `requestpath` 拥有选择和 lease
生命周期。因此调度器可以在没有外部代理的情况下开发和测试，同时不会与未来标准网关 adapter
绑定。这是已实现的开发和演示模式，而非试图替代生产级网关。

面向生产的集成目标是 Envoy 兼容 Gateway 背后的 Endpoint Picker/调度器扩展。Gateway API Inference Extension 现在保留了 InferencePool 和轻量级 EPP API，而完整的 EPP 调度器已迁移至 llm-d。因此，FishMesh 将在进一步扩展自定义代理之前，先验证 llm-d 调度器插件或协议兼容的集成方式：

- [Gateway API Inference Extension](https://github.com/kubernetes-sigs/gateway-api-inference-extension)
- [llm-d Request Scheduler](https://llm-d.ai/docs/architecture/core/router/epp/scheduling)

## 故障契约

| 条件 | 当前行为 | 下一步加固 |
| --- | --- | --- |
| EndpointSlice 不可用但快照新鲜 | 从有界快照继续；已有进程级 E2E | 恢复 SLO 与长时间 soak |
| 快照过期或无 Ready 端点 | 使用 Kubernetes Service 回退；已有自动 E2E | 显式告警和恢复 SLO |
| 首选后端超过负载边界 | 溢出但不重写亲和性 | 持续饱和 soak |
| 部分/缺失队列观测 | 从决策中排除队列；逐字段采样 | 观测恢复 E2E |
| 上游传输错误 | 短 TTL EWMA 熔断；取消保持中性；已有故障 E2E | circuit 恢复 soak |
| 端点被移除 | 停止选择并回收状态；已有动态发现 E2E | 高频 churn soak |

## 无 GPU 的本地故障验证

启动受控 backend：

```bash
go run ./cmd/fishmesh-simulator --listen :8090 --events 2
```

在另一个终端让 standalone Gateway 使用它：

```bash
FISHMESH_UPSTREAM_URL=http://127.0.0.1:8090 \
  go run ./cmd/fishmesh-gateway
```

`PUT /control/behavior` 会原子修改后续请求，不影响已经开始的流。例如注入 503：

```bash
curl -X PUT http://127.0.0.1:8090/control/behavior \
  -H 'Content-Type: application/json' \
  -d '{"status_code":503,"events":1}'
```

该控制 API 只服务于本地/CI 验证，不应暴露在生产网络。

## 在本地针对 K3s 集群运行

首先验证仓库：

```bash
make ci
```

查看已配置的集群：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm get deploy,pod,svc,endpointslice
```

转发 Gateway 并发送流式请求：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm port-forward svc/fishmesh-gateway 8080:8080

curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-FishMesh-Prefix-Key: demo-session' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [{"role": "user", "content": "简要介绍一下 FishMesh。"}],
    "stream": true,
    "max_tokens": 64
  }'
```

响应通过 `X-FishMesh-*` 头暴露所选策略、首选后端、实际选择的后端和溢出原因。

## 工程范围

FishMesh 负责：

- 端点资格判定和路由状态生命周期；
- 有界、可解释的端点选择；
- 请求路径上的过载和故障行为；
- 运维和测试这些组件所需的可观测契约。

FishMesh 复用上游 vLLM 进行推理，并将复用 Gateway API/Envoy 或 llm-d 进行生产级入口和请求控制集成。认证、租户计费、通用 API 网关功能、精确的 token-block 缓存索引、GPU 内核和模型执行不在本项目范围内。

只读的 `fishmesh-analyst` 仍是一个辅助性的诊断原型。在请求路径可靠性、标准集成和端到端运维完成之前，其范围被冻结。

## 路线图

1. **请求路径可靠性：** 准入、熔断、状态回收、连接上限和 simulator 故障 E2E 已实现；仍需 soak 覆盖。
2. **标准集成：** 可控 backend simulator 已落地；下一步进行 EPP/llm-d 集成探路并确定运行时路径。
3. **可运维性：** 自动化故障端到端测试、仪表盘、链路追踪、多架构发布镜像和供应链元数据。
4. **对比验证：** 针对 Service、最少负载和一个开源调度器的有界工作负载矩阵。

实验可能改变工程决策或验证验收标准；它们不是独立的产品线。参见持久的
[项目章程](docs/design/project-charter.md)、[实现计划](docs/design/plan.md)和
[实验策略](docs/experiments/plan.md)。

Go 代码变更必须遵守[代码组织规范](docs/design/code-organization.md)。
[Domain 重设计](docs/design/serving-domain-redesign.md)的四个阶段已经完成：核心类型和 I/O 能力
拥有明确 owner，一次选择由可幂等结算的 requestpath lease 表达，import 方向有自动门禁，命令
入口也成为显式组合根。Gateway 现在只保留 standalone HTTP/SSE delivery 和指标投影。

## 已验证的环境与局限性

当前集群为 K3s `v1.36.3+k3s1`，vLLM `0.23.0`，两个 vLLM 进程共享一块时间分片的 RTX 4060 Laptop GPU。它验证了请求生命周期、发现、路由和恢复行为。它不代表两个独立的 GPU 故障域，也不用于声称生产规模性能。

最新的有界亲和性 K3s 冒烟测试完成了 24/24 个请求，并同时触发了亲和性和本地在途溢出。原始基准输出和集群快照保留在 Git 之外；只有代码、可复现的配置和经过审查的结论属于仓库历史。
