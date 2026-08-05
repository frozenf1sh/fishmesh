# FishMesh

> 面向自托管 LLM 推理的轻量 Kubernetes 原生、真实 KV cache 感知路由器。

[English](README.md)

FishMesh 正在演进为面向小型集群的完整推理路由产品。目标主要运行时是一个 Go HTTP/SSE
数据面：动态发现 vLLM Pod，维护真实 KV cache locality，把 locality 与实时负载和故障状态结合，
再将兼容 OpenAI 的请求直接转发到选中的 Pod。当前已经实现的 baseline 会在下文单独说明。

对于已经运行 Envoy-compatible Gateway 的平台环境，FishMesh 同时提供标准 llm-d EPP 集成。
两种运行时共享同一份协议无关的路由策略；FishMesh 不重新实现 Envoy `ext_proc`、InferencePool、
flow control 或模型执行。

## 要解决的问题

Kubernetes Service 只能分配连接，不能回答逐请求问题：

- 哪个 vLLM Pod 处于 Ready/Serving，且发现状态仍然新鲜；
- 当前 prompt 的最长缓存前缀真实存在于哪个 Pod；
- cache 收益是否值得等待该 Pod 已排队的工作；
- stream 取消、Pod 滚动发布或观测断流时，哪些本地状态必须失效；
- 一次请求为什么使用 exact cache、降级为 load-only 或执行 fallback。

仅靠 session affinity 不够。两个完全不同的会话可能共享很长的 system prompt，从而复用相同
KV blocks；而 Pod 重启后，即使 session key 不变，原有 KV cache 也已经全部丢失。

## 产品形态

### Lite mode：主要交付物

```text
OpenAI 客户端
  -> fishmesh-gateway Service / Deployment
       -> 有界请求体
       -> vLLM Render API -> 真实 Token IDs
       -> 逐 Pod KV index <- vLLM KVEvents
       -> cache + load + health 联合路由
       -> 选中的 vLLM Pod IP
       -> HTTP/SSE 流式透传
```

Lite mode 不要求 Envoy、Gateway API Inference Extension CRD、EPP、Redis 或 FishMesh Operator。
它适合通用 Service 能力不足、但完整共享网关平台又过重的小型自托管推理池。

### Standard mode：标准生态集成

```text
OpenAI 客户端
  -> Envoy-compatible Gateway
  -> llm-d EPP runtime
       -> llm-d token / precise-prefix producer
       -> FishMesh routing policy
       -> llm-d picker / flow control / response lifecycle
  -> vLLM Pod
```

Standard mode 面向共享 Gateway、多推理池和平台统一入口。FishMesh 复用标准 runtime，不维护
另一套 EPP 协议实现。

## 当前实现进度

已部署的 baseline 具备：

- OpenAI-compatible HTTP/SSE 代理、取消传播、stream outcome 分类和 TTFT 指标；
- EndpointSlice watch/list、Ready 过滤、周期 relist、freshness 和显式 Service fallback；
- 带逐字段 valid/age 的 per-backend vLLM queue/running 观测；
- `bounded-affinity-v1`：session key SHA-256、Rendezvous Hash、有界 TTL registry 和负载溢出；
- 非阻塞 admission、per-backend connection bounds、transport error EWMA circuit 和状态回收；
- Prometheus 路由/发现/backend 指标与 `X-FishMesh-*` 请求 provenance；
- 严格配置、探针、优雅关闭、最小 RBAC 和经过 race test 的请求生命周期；
- 固定 llm-d Router v0.9.0 的 adapter 与 `fishmesh-epp` 组合根，本地契约测试已通过。

R6A 真实信号门禁已经通过：vLLM Render/KVEvents/replay 为共享 system prompt 的两个不同会话
返回了逐 Pod 128-token 命中，真实 eviction、subscriber 恢复和 Pod 重建也正确移除了旧 locality。
Exact routing 仍未接入 Gateway；R6B 先按契约实现 tokenization 与 KV cache 能力域，再修改请求路径。

因此当前 `X-FishMesh-Prefix-Key` 只是兼容期 session hint，不是 prefix-cache awareness 的证据。
Exact 路由接入后，它将变成可选输入。

## 路由契约

目标策略刻意保持简单易懂：

1. 排除 terminating、stale、open-circuit 或其他不合格 endpoint；
2. 计算每个 Pod 真实的 `matched_prefix_tokens` 和 `uncached_tokens`；
3. 使用新鲜负载估算 queued work 和未缓存 prefill 成本；
4. 使用 hard overload guard，禁止 cache locality 覆盖严重压力；
5. 使用小幅 benefit margin/hysteresis，避免为了很小的 cache 差异频繁抖动；
6. KV 状态缺失或过期时，从 exact-cache-load 明确降级为 load-aware；
7. 可选 session hint 只用于平局或短期稳定性；
8. 每次决策暴露 typed policy、reason、cache source 和 degradation state。

FishMesh 不声称发明新调度算法。工程贡献是把真实 engine state、有界状态、可靠降级和轻量流式
数据面完整交付，并且能够通过标准 llm-d 运行时进入平台环境。

## 运行当前 baseline

验证仓库和集群：

```bash
make ci

kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm get deploy,pod,svc,endpointslice
```

转发已经部署的 Gateway：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

发送流式请求。该 header 对连通性不是必需的；在当前 baseline 下，它用于 session affinity：

```bash
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

## 交付优先级

| 优先级 | 范围 | 决定 |
| --- | --- | --- |
| 主产品 | Gateway、requestpath、routing、discovery、observation、circuit、admission、transport | 产品化 |
| 新核心 | tokenization、KVEvents/index、exact-cache-load policy | R6A 已通过，R6B 按契约实施 |
| 标准集成 | llmd adapter、`fishmesh-epp`、Gateway/InferencePool 部署 | 保留，Lite MVP 后完成 |
| 开发工具 | simulator、loadgen | 冻结功能，只用于回归/benchmark |
| 冻结 | analyst、Diagnostics Context | 只接受安全/构建修复，从默认镜像/部署移除 |
| 排除 | eBPF、Agent actuator、FishMesh CRD/Operator、P/D、共享数据库、通用 AI Gateway | 不进入 MVP |

从产品面移除不等于立即删除历史代码。低优先级模块先冻结并从默认 release artifact 拆出；是否
物理删除由后续独立变更决定。

## 路线图

1. **R6A——真实 KV 信号门禁（已完成）：** 在现有 K3s 集群验证 Render/KVEvents/replay、
   跨会话 128-token 命中、eviction、restart 清理与显式失效/恢复。
2. **R6B——能力域：** contract-first 的 tokenization/kvcache、有界 index、纯 cache/load
   策略、requestpath 接入和 llm-d precise match 翻译。
3. **R6C——Lite 交付：** gateway-only 镜像、一键部署、安全/资源、dashboard、alerts、runbook
   和真实 rollout/failure 验收。
4. **R6D——有限对照：** Service、FishMesh load-only、FishMesh exact 和 llm-d precise，只跑
   cache-cold、shared-system-prefix 和 overload 三类工程场景。
5. **R6E——Standard 交付：** Gateway/HTTPRoute/InferencePool/EPP 部署与 wire-level 契约。

实验只用于决定实现、验证验收或防止回归。现有 simulator 和历史实验不是独立产品路线。

## 工程规范

Go 变更必须遵守[代码组织规范](docs/design/code-organization.md)。新能力固定按“契约和值对象 →
contract test → 原子实现 → 编排 → 外部 adapter → 真实集群验证 → 阶段文档”实施。主编排函数
只保留 3–7 个同层级步骤，禁止把协议解析、block index 和 fallback 决策堆进一个大函数。

生产代码使用清晰的中文注释解释不变量、owner、取消、freshness 和降级原因；注释要说明为什么
存在，不能逐行翻译 Go 语法。

进一步阅读：[项目章程](docs/design/project-charter.md)、[代码架构](docs/design/architecture.md)、
[实施计划](docs/design/plan.md)、[ADR-002](docs/design/decisions/002-lite-exact-kv-routing.md)、
[当前状态](docs/notes/project-status.md)。

## 已验证环境与限制

当前集群为 K3s `v1.36.3+k3s1`，vLLM `0.23.0`，两个 vLLM 进程共享一块 time-sliced RTX 4060
Laptop GPU。它适合验证 engine compatibility、路由行为、故障恢复和相对开销，不代表两个独立
GPU failure domains，也不能支持生产规模或水平扩展声明。

raw benchmark、日志和集群 snapshot 保留在 Git 之外；仓库历史只提交源码、声明式配置、schema
和经过评审的结论。
