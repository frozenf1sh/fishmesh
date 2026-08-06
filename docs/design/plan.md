# FishMesh 设计与实施路线

> 状态：交付优先路线，2026-08-11。R6A 已完成；R5D 不取消但后移。当前进入 R6B
> tokenization 与 KV cache 能力域。最高方向约束见 [`project-charter.md`](project-charter.md)，决策见
> [`ADR-002`](decisions/002-lite-exact-kv-routing.md)。

## 1. 交付目标

FishMesh 要交付一个能在真实 Kubernetes 集群安装和运行的轻量 LLM Router，而不是调度论文、
实验平台或 llm-d 插件示例。

主交付物是 Lite mode：

- 单独的 `fishmesh-gateway` 镜像和声明式部署；
- 正确的 OpenAI HTTP/SSE 数据面；
- 动态 vLLM Pod 发现和资源生命周期；
- 基于真实 KV block locality 的跨 session prefix reuse；
- cache、负载、过载和故障联合选择；
- 清晰降级、观测、告警、runbook、发布和性能边界。

Standard mode 是第二交付物：同一纯策略通过 `fishmesh-epp` 接入 Envoy、InferencePool 和
llm-d。它证明生态兼容性，不取代 Lite 产品。

## 2. 当前系统基线

### 已实现并在真实集群运行

- Go streaming proxy、SSE 透传、取消、TTFT 和 request provenance；
- EndpointSlice Ready discovery、watch/relist、freshness/readiness 和 Service fallback；
- per-backend vLLM queue/running observation；
- bounded-affinity-v1、Rendezvous Hash、TTL/容量上限和负载 spillover；
- admission、per-backend connection bounds、transport EWMA circuit 和 endpoint state GC；
- Prometheus metrics、严格配置、graceful shutdown、最小 RBAC 和 Kustomize；
- 两个 vLLM 0.23.0 副本、prefix caching 和真实 OpenAI/SSE smoke。

### 已实现但不是主产品

- GPU-free simulator 和 loadgen；
- `fishmesh-analyst` 与只读 Diagnostics 原型；
- llm-d Router v0.9.0 Filter/Scorer adapter、`fishmesh-epp` 组合根和本地 conformance。

### 当前关键缺口

- 产品请求路径还没有启用/消费 vLLM KVEvents（R6A 实验链路已通过并在结束后恢复基础清单）；
- 当前 `X-FishMesh-Prefix-Key` 是客户端 session hint，不是真实 prefix cache 信号；
- Gateway 还没有有界读取、重放请求体和真实 tokenization；
- 没有逐 Pod KV block index、event freshness 或 eviction/restart 契约；
- 默认镜像和部署仍捆绑低优先级二进制；
- Lite mode 缺少完整安装、dashboard、alerts、runbook、release 和资源预算；
- Standard mode 尚未完成 Gateway/EPP/InferencePool wire 部署。

## 3. 目标运行时

### 3.1 Lite mode

```text
client
  -> fishmesh-gateway Service
  -> bounded OpenAI request body
  -> vLLM Render API -> exact Token IDs
  -> local KV index <- KVEvents from every vLLM Pod
  -> eligibility + cache/load/failure routing
  -> selected Pod IP
  -> SSE passthrough + outcome
```

Lite mode 只保留必要进程。第一版不引入 Redis、Controller 或 tokenizer sidecar；优先调用现有
vLLM `serve` 暴露的 Render API。若真实测量证明 render 与 inference 前端争用明显，再单独决定
是否提供 renderer Service。

### 3.2 Standard mode

```text
client
  -> Envoy-compatible Gateway
  -> llm-d EPP runtime
       -> token producer + precise prefix producer
       -> FishMesh routing adapter
       -> llm-d picker / flow control / lifecycle
  -> selected vLLM Pod
```

两种模式只共享协议无关的 routing 输入和选择语义。Lite requestpath 的 discovery、circuit、
fallback 和 lease 不能在 EPP 中再运行一份；Standard mode 的空 subset 仍返回 503。

## 4. 目标快路径

```text
request
  -> bounded admission and body capture
  -> exact tokenization
  -> endpoint / observation / cache snapshot
  -> eligibility
       Ready && Serving && fresh && circuit closed
  -> exact-cache-load policy
       estimate uncached prefill + queued work
       apply hard overload guard and hysteresis
  -> bounded transport
  -> stream response + complete lease
```

### 4.1 快路径输入

| 信号 | 用途 | 约束 |
| --- | --- | --- |
| Endpoint Ready/Serving/Terminating | eligibility | Kubernetes 生命周期事实 |
| discovery freshness | fallback | 不使用无限陈旧地址 |
| exact prompt Token IDs | block lookup | 来自同模型 vLLM Render API |
| per-Pod cached prefix tokens | locality | 来自 KVEvents index，必须带 freshness |
| local in-flight / queued work | load cost | 与请求生命周期同步 |
| vLLM waiting/running/KV usage | overload | 只使用有效且足够新的字段 |
| observed prefill rate | cost estimate | 无样本时使用保守静态默认或不参与 |
| recent local transport errors | circuit | 只归因实际选中 backend |
| optional session hint | tie-break/hysteresis | 不得覆盖真实 cache/load |

### 4.2 禁止冒充快路径事实的信号

- 进程生命周期累计 TTFT histogram；
- 全局累计 Prefix Cache hits/queries；
- 节点级 GPU utilization、温度和显存；
- 客户端自称的 prefix key；
- eBPF RTT、重传或 socket stall；
- LLM 生成的分数或没有量纲解释的任意权重。

## 5. 状态与资源不变量

所有外部信号使用显式 sample：

```text
Sample[T] {
  value
  valid
  observed_at
  source
  error
}
```

必须始终成立：

1. unknown/stale cache 不等于零命中；
2. Pod UID、model、cache salt 和 endpoint 身份不能混淆；
3. event 断流、gap 或无法 replay 时，相关 endpoint 的 exact sample 失效；
4. `uncached_tokens` 不小于零，cache 命中不能绕过 hard overload guard；
5. terminating、stale、open-circuit backend 不进入候选；
6. session hint 缺失时仍能 exact/load-aware 路由，不形成空 key 热点；
7. request body、token slice、KV index、connections、in-flight、observations、circuits 和 labels
   均有容量上限或回收；
8. 请求取消传播到 render/upstream，请求完成后计数必定释放；
9. response headers 发出后不透明 retry；
10. fallback、reject 和 degradation 均有固定 typed reason。

## 6. 交付物清单

### 必须进入 Lite MVP

- gateway-only release image；
- `deploy/lite` 声明式安装入口；
- vLLM exact KVEvents 配置和兼容矩阵；
- exact/load-only 配置与启动校验；
- cache index/event/tokenization/route 指标；
- Grafana dashboard、Prometheus alerts 和故障 runbook；
- 真实集群 smoke、rollout、event stale/recovery 和 benchmark 脚本；
- README 中五分钟可运行的 demo；
- multi-arch、digest、SBOM、版本和升级说明。

### 必须进入 Standard 交付

- 独立 `fishmesh-epp` image；
- Gateway/HTTPRoute/InferencePool/EPP 部署；
- llm-d precise prefix match 到 FishMesh routing 的翻译；
- ext_proc 正常流、空 subset 503、429、retry served endpoint 和 EPP failover 验收；
- Lite/Standard 相同策略输入的 conformance。

### 从默认产品移除

- `fishmesh-analyst`；
- `fishmesh-simulator`；
- `fishmesh-loadgen`；
- diagnostics demo fixture；
- 历史 experiment overlays。

它们可以作为单独 dev target 保留，但不能继续放在默认 release image、默认 Kustomize 或 README
主流程中。

## 7. 实施里程碑

### R6A：真实 KV 信号门禁（已完成）

目标：用最小代码证明数据链路真实可用，再决定生产抽象。

- 给现有 vLLM 0.23.0 开启 KVEvents publisher/replay；
- 从两个 Pod 订阅 stored/removed events；
- 调用 Render API 获取 Chat 请求的真实 Token IDs；
- 查询逐 Pod prefix match；
- 验证跨 session 共享 system prompt、eviction、Pod restart、subscriber disconnect/replay；
- 记录 event lag、render/lookup latency、index entries 与 RSS；
- 输出兼容性和 Go/no-go 结论。

验收：[`ADR-002`](decisions/002-lite-exact-kv-routing.md) 第 9 节全部通过。失败时先决定
pin/upgrade 或删除方案，禁止用 simulator/累计 hit rate 代替。

实测结果为跨会话 128-token 逐 Pod 命中、断流先 invalid 后 replay、3105 个真实 removed 后旧
命中归零，以及 Pod UID 重建清理；详见[阶段 18](../stages/18-R6A真实KV信号闭环.md)。

### R6B：能力域与纯策略

按以下顺序实现，每一项独立可 review：

1. `tokenization` 契约、值对象、contract tests（阶段 19 已完成）；
2. vLLM Render adapter（阶段 19 已完成）；
3. `kvcache` 契约、match/freshness 值对象、contract tests（阶段 20 已完成）；
4. KVEvents/index adapter 和 membership cleanup（阶段 20 已完成）；
5. `routing` exact-cache-load 输入与纯选择；
6. `requestpath` tokenization/cache/load/degradation 编排；
7. Gateway bounded body/replay 与 response passthrough；
8. llmd adapter 的 precise `PrefixCacheMatchInfo` 翻译。

每个切片都必须保持主编排函数可读、状态有界，并通过完整 CI。不得在一个提交同时引入协议
adapter、策略、部署和大规模包移动。

### R6C：Lite 产品化

- 拆分 release images 和构建目标；
- 建立 `deploy/lite`，包含 SA/RBAC、ConfigMap、Deployment、Service、probes、resources、PDB、
  security context；
- 在支持 policy enforcement 的 CNI 上再启用 NetworkPolicy，不做虚假声明；
- 完成两 Gateway 副本的本地索引行为和资源预算；
- 执行 vLLM rollout、endpoint churn、event stale/replay、Gateway restart 和 cancellation 验收；
- 交付 dashboard、alerts、runbook 和五分钟 demo。

### R6D：轻量与性能边界

使用现有真实集群先完成工程 profile，再决定是否需要独立物理 GPU 扩展结论：

- direct Service；
- FishMesh load-only；
- FishMesh exact；
- llm-d precise。

预设工程目标而非提前声明结果：

- routing decision p99 目标小于 1 ms（不含 Render）；
- 长 SSE steady-state token throughput 目标达到 direct Service 的 95% 以上；
- 2–8 endpoint、受控 index 容量下 Gateway RSS 目标为 256 MiB 级；
- cache-cold workload 不显著劣于 load-only；
- 公共长 prefix workload 的 TTFT 有稳定改善；
- event stale 时不产生错误的 exact-cache 声明。

若目标未达成，优先优化 body copy、tokenization 调用、index bounds 和 proxy hot path，不通过
删除正确性检查换性能。

### R6E：Standard mode 闭环

- 完成原 R5D 的标准栈部署；
- 配置 llm-d token producer、precise prefix producer 和 FishMesh scorer；
- 验证 wire protocol 与 failure contract；
- 对比内置 llm-d policy，若 FishMesh policy 没有行为差异或运维价值，允许把 Standard mode
  收缩为配置/兼容性证明，不为保留自定义代码而继续扩展。

## 8. 开发与提交规则

- 一次只迁移或实现一个能力域；
- 先契约和值对象，再 contract test，再实现，再编排，再 adapter/deploy；
- 编排函数只保留 3–7 个同层级步骤，目标不超过 40 行；
- 中文注释解释不变量、失败语义和降级原因，不翻译 Go 语法；
- 第三方类型只停留在 adapter；
- behavior change 与 mechanical move 分开；
- 每个阶段更新 `docs/stages/`、阶段索引和 `docs/notes/project-status.md`；
- 每次完成后通过 `go test -race ./...`、`go vet ./...`、`go build ./...`、`make manifest` 和
  `git diff --check`，再规范提交并推送；
- 用户的本地 artifact、raw benchmark、日志和无关未跟踪文件不得进入提交。

## 9. 停止扩张条件

以下事项不阻塞 MVP：

- 更多 simulator fault 类型或长时间无 GPU soak；
- Analyst 新 collector 或 LLM diagnosis；
- 更大的 workload 矩阵和论文式统计展示；
- Redis、多集群、P/D、offloading、Agent、eBPF、CRD/Operator；
- 与所有 Gateway provider 的安装矩阵。

当 Lite MVP、有限对照和 Standard 兼容性完成后，项目应优先整理演示、简历和面试讲述，而不是
自动开启新的技术方向。
