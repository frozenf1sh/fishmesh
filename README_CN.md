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

R6A–R6B 的真实信号与请求路径已完成：vLLM Render/KVEvents/replay 为共享 system prompt 的不同会话
提供逐 Pod 命中，Gateway 已据此执行 `exact-cache-load-v1`。真实 eviction、subscriber 恢复和 Pod
重建会移除旧 locality；unknown/stale 明确降级到 load-aware，绝不伪装成零 token 命中。

`X-FishMesh-Prefix-Key` 仍是可选兼容 session hint；下面的 exact 演示刻意不发送它，以证明不同 user
message 之间的缓存复用。

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

## 五分钟 Lite 演示

演示前提见 [`deploy/lite-exact/README.md`](deploy/lite-exact/README.md)：已有 K3s、模型 PV 和可导入
的 Gateway 镜像。先确认安全默认值 `bounded-affinity`，再临时启用 exact overlay。Standard mode / llm-d
集成本轮后置。

验证仓库和 bounded-affinity 基线：

```bash
make ci

kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get deployment
```

安装 exact overlay 并等待真实 KV 信号链路就绪。已提交 overlay 使用 `r6d2-r1`：

```bash
make image VERSION=r6d2-r1
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/lite-exact
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deploy/qwen-vllm --timeout=10m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deploy/fishmesh-gateway --timeout=5m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

另开终端，用相同的长 system prompt 和不同 user message 发送两个流式请求。**不要**发送
`X-FishMesh-Prefix-Key`，并分别保存响应头：

```bash
SYSTEM_PROMPT="$(printf 'FishMesh demo policy: answer concisely, state assumptions, preserve streaming semantics, and never reveal hidden reasoning. %.0s' {1..32})"

curl -sS -D /tmp/fishmesh-first.headers -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [
      {"role": "system", "content": "'"${SYSTEM_PROMPT}"'"},
      {"role": "user", "content": "first request"}],
    "stream": true,
    "max_tokens": 64
  }'

curl -sS -D /tmp/fishmesh-second.headers -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [
      {"role": "system", "content": "'"${SYSTEM_PROMPT}"'"},
      {"role": "user", "content": "second request"}],
    "stream": true,
    "max_tokens": 64
  }'

grep -iE 'x-fishmesh-(exact-status|policy|route-reason|cached-prefix-tokens)' \
  /tmp/fishmesh-first.headers /tmp/fishmesh-second.headers
```

首请求在 replay 变为 fresh 前可以正确返回 `match-unavailable` 和 `exact-load-fallback-v1`。有效预热后，
第二请求应返回 `available`、`exact-cache-load-v1`、`exact-cache-load` 及非零
`X-FishMesh-Cached-Prefix-Tokens`。`available` 但 cached tokens 为零是真实零命中，不等于 unavailable。
结束后恢复默认值：`kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/baseline/base`。

参考集群已运行 `deploy/monitoring` 中的 Prometheus + Grafana：Gateway 有真实 scrape target，规则已经加载，
`FishMesh Gateway` dashboard 已自动 provision。它有意不部署 Alertmanager/通知接收器，所以规则会计算并可见，
但不会向外投递。见 [`deploy/monitoring/README.md`](deploy/monitoring/README.md) 与
[`docs/notes/runbook.md`](docs/notes/runbook.md)。

## 交付优先级

| 优先级 | 范围 | 决定 |
| --- | --- | --- |
| 主产品 | Gateway、requestpath、routing、discovery、observation、circuit、admission、transport | 产品化 |
| 新核心 | tokenization、KVEvents/index、exact-cache-load policy | 已完成；Lite exact overlay 可用 |
| 标准集成 | llmd adapter、`fishmesh-epp`、Gateway/InferencePool 部署 | 保留，Lite MVP 后完成 |
| 开发工具 | simulator、loadgen | 冻结功能，只用于回归/benchmark |
| 冻结 | analyst、Diagnostics Context | 只接受安全/构建修复，从默认镜像/部署移除 |
| 排除 | eBPF、Agent actuator、FishMesh CRD/Operator、P/D、共享数据库、通用 AI Gateway | 不进入 MVP |

从产品面移除不等于立即删除历史代码。低优先级模块先冻结并从默认 release artifact 拆出；是否
物理删除由后续独立变更决定。

## 路线图

1. **R6A–R6C（已完成）：** 真实 Render/KVEvents/replay、能力域、exact requestpath、
   gateway-only Lite 镜像和真实 rollout/failure 验收。Prometheus/Grafana 面板和规则已实集群接入，
   但尚未部署外部告警通知。
2. **R6D（已完成）：** 有限的 Service/load-only/exact profile，包含受控 c=1 前缀分段；数据仅是
   correctness/profile 证据。
3. **Lite release 收尾（进行中）：** 用户 demo、发布制品及升级/回滚说明。
4. **R6E——Standard 交付（后置）：** Gateway/HTTPRoute/InferencePool/EPP 部署与 wire-level 契约。

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
