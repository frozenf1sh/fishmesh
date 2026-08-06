# FishMesh 代码架构

> 状态：R1–R5C、R6A 已完成，2026-08-11 进入 R6B。方向见
> [`project-charter.md`](project-charter.md)，代码规则见
> [`code-organization.md`](code-organization.md)，Exact KV 决策见
> [`ADR-002`](decisions/002-lite-exact-kv-routing.md)。

## 1. 架构原则

FishMesh 不采用完整 DDD，也不引入 Repository、Event Bus 或无业务所有权的 shared 层。采用：

> **限界上下文 + 能力包 + 单向依赖 + 显式组合根**

每个能力包只拥有一种状态或行为；编排层只表达顺序和降级；外部协议停留在 adapter；每个
`cmd/*` 创建并注入具体实现。代码行数不是能力边界，复杂第三方协议也不能泄漏进纯策略。

同一上下文内允许在最近的共同 owner 下增加 `entity/<concept>` 纯实体子包。例如上下文通用
配置可以使用 `internal/serving/entity/conf`，大型 kvcache domain 内多个子能力共享的配置可以
使用 `internal/serving/kvcache/entity/conf`。实体包只依赖标准库，可提供 `Validate` 等纯行为，
但不能读取环境、执行 I/O 或持有运行时状态。它是按具体概念命名的模型 owner，不是新的
`shared/types` 根仓库；单一实现专属 Config 仍由原 domain 拥有。

## 2. 产品运行时

### 2.1 Lite mode：主运行时

```text
OpenAI client
  -> fishmesh-gateway
       -> admission + bounded request body
       -> tokenization (vLLM Render API)
       -> requestpath
            -> discovery / observation / circuit
            -> kvcache lookup (KVEvents index)
            -> routing policy
       -> transport to selected Pod IP
       -> SSE passthrough + lease completion

vLLM Pods -- KVEvents/ZMQ --> bounded per-Pod KV index
Kubernetes EndpointSlice --> immutable membership snapshot
vLLM metrics -------------> per-field load snapshot
```

Lite mode 是一个完整但边界受控的 LLM Router，不实现 TLS、认证、计费、通用路由规则、CRD 或
模型执行。它可以通过 ClusterIP Service 被现有 Ingress/Gateway 暴露。

### 2.2 Standard mode：生态运行时

```text
OpenAI client
  -> Envoy-compatible Gateway
  -> llm-d EPP runtime
       -> InferencePool / parser / data producers / flow control
       -> FishMesh llmd adapter
       -> shared pure routing policy
       -> llm-d picker / response lifecycle
  -> vLLM Pod
```

Standard mode 不 import 或启动 Lite `requestpath`。llm-d 的 precise producer 负责 tokenization、
KVEvents/index 和 `PrefixCacheMatchInfo`；FishMesh adapter 只翻译稳定值对象。

## 3. 当前能力包

| Package | 唯一所有权 | 对外能力 |
| --- | --- | --- |
| `backend` | backend ID、地址和稳定 metadata | 构造、校验、稳定身份 |
| `discovery` | membership snapshot 与 freshness | `Resolver.Snapshot/Status/Close` |
| `identity` | backend 到 Kubernetes workload identity | `Provider.Resolve` |
| `observation` | per-backend `Sample[T]` 与采集 freshness | `Reader.Snapshot/Close` |
| `routing` | mode、reason、decision 与纯选择状态 | `Strategy.Select/Reconcile` |
| `circuit` | transport outcome EWMA 与 open state | `Record/State/Reconcile` |
| `admission` | 进程并发许可 | `TryAcquire` 和幂等 permit |
| `transport` | endpoint-scoped HTTP client/connection | `ClientFor/Remove/Close` |
| `tokenization` | 推理请求到真实模型 Token IDs | `Tokenizer.Tokenize` 与只读 prompt profile |
| `kvcache` | 逐 vLLM 实例的真实 KV block locality | `Index.Lookup/Reconcile/State/Close` |
| `requestpath` | Lite selection、fallback、lease 与成员回收 | `Select` 和 `Lease.Complete` |
| `gateway` | HTTP/SSE delivery 与指标投影 | health/ready/metrics/v1 handler |
| `llmd` | llm-d 数据到 routing 的翻译 | plugin 注册、Filter/Scorer/hooks |
| `config` | 环境变量到各 domain config 的一次映射 | `LoadEnvironment/Validate` |

R1–R4 已完成上述 owner 和显式组合根；R5A simulator 与 R5C llmd adapter 都没有反向依赖
Gateway。

## 4. R6 新能力包

R6A spike 已通过且没有提前固化生产包。R6B 现在按顺序增加两个能力域：

### 4.1 `tokenization`

一句话职责：拥有“推理请求到真实模型 Token IDs”的契约，返回可用于 KV lookup 的不可变
prompt profile。

```text
tokenization/
├── tokenization.go          # Input、Prompt、Tokenizer、错误与 Config
├── vllm_render_impl.go      # vLLM Render HTTP adapter
├── tokenization_test.go     # 契约、取消、大小和错误边界
└── vllm_render_impl_test.go # wire format、timeout 与兼容性
```

它负责：

- 支持的 OpenAI route 到 Render API 的显式映射；
- model、token IDs、cache salt/额外 hash 输入的稳定值对象；
- timeout、取消、响应体上限和错误分类；
- exact 不可用时返回 typed degradation，不自行选择 backend。

它不负责 KVEvents、index、routing、HTTP response 或 fallback。

### 4.2 `kvcache`

一句话职责：拥有“哪些 KV blocks 当前属于哪些 backend”的有界状态，按请求 tokens 返回逐
backend prefix match。

```text
kvcache/
├── kvcache.go               # Match、Snapshot、Instance 与 Index 契约
├── config.go                # index/event/replay/query 容量和 freshness
├── kvcache_impl.go          # lookup owner 与关闭事务
├── vllm_index_impl.go       # 上游 parser/hash/index/scorer 薄 adapter
├── lifecycle_impl.go        # 同步 live event 与 sequence 状态
├── replay_impl.go           # replay heartbeat 和 gap 恢复
├── reconcile_impl.go        # Pod UID membership 与清理事务
├── zmq_impl.go              # vLLM PUB/SUB 和 ROUTER/DEALER transport
├── kvcache_test.go          # lookup、stale、eviction、UID 不变量
└── *_impl_test.go           # wire、重连、容量和并发
```

它负责：

- block stored/removed、Pod UID、model/cache salt 隔离；
- replay/reconnect、event freshness 和 endpoint lifecycle；
- 有界内存、逐 Pod 删除和 immutable match snapshot；
- `llm-d-kv-cache` 第三方类型到 FishMesh 值对象的隔离。

它不负责请求 tokenization、负载采集、路由权重或 HTTP proxy。

## 5. 目标依赖 DAG

```text
backend      -> standard library only
admission    -> standard library only
circuit      -> backend
discovery    -> backend + platform/kubernetes
identity     -> backend + platform/kubernetes
observation  -> backend + identity
tokenization -> standard library（vLLM HTTP 只在 adapter）
kvcache      -> backend（llm-d-kv-cache/vLLM 只在 adapter）
routing      -> backend + observation + kvcache values
transport    -> backend
requestpath  -> backend + discovery + observation + tokenization + kvcache + routing + circuit
gateway      -> admission + requestpath + transport
llmd         -> backend + observation + kvcache values + routing
config       -> 各 domain Config
cmd          -> config + delivery + all concrete implementations
entity/*     -> standard library only（仅在出现真实跨 domain 稳定模型时建立）
```

强制禁止：

- `routing` import HTTP、Kubernetes、Prometheus、ZMQ、vLLM 或 llm-d；
- `kvcache` contract 暴露第三方 block/event 类型；
- `tokenization` 选择 endpoint 或静默 fallback；
- `gateway` 解析 KVEvents、维护 block map 或实现调度公式；
- `llmd` 启动 Lite discovery/index/requestpath；
- 原子 domain import `gateway/requestpath/cmd`；
- 新建 `shared/common/utils/helpers/manager` 隐藏所有权。
- 把所有 Config/DTO 无差别搬入根 `entity` 包；实体必须按概念分包，并具有真实跨 domain 语义。

架构测试在每个新包落地时同步扩展，不等待 R6 全部完成后补门禁。

## 6. Lite 请求流程

R6B 完成后，`requestpath` 主流程保持一个抽象层级：

```go
func (s *service) Select(ctx context.Context, request Request) (Lease, error) {
	// 1. 获得与模型模板一致的真实 Token IDs；失败时记录 exact 降级原因。
	prompt := s.tokenize(ctx, request)

	// 2. 构建 endpoint、负载、故障和逐 Pod cache match 的不可变快照。
	snapshot := s.snapshot(ctx, prompt)

	// 3. 应用 eligibility 与 exact-cache-load 纯策略。
	decision, err := s.router.Select(request.Profile(prompt), snapshot)
	if err != nil {
		return Lease{}, err
	}

	// 4. 为实际选中的 backend 建立可幂等结算的请求 lease。
	return s.leases.Acquire(decision), nil
}
```

示例只表达目标阅读体验，不提前规定最终函数签名。tokenization、snapshot、degradation 和 lease
分别由小方法/能力包实现；主函数中不得出现 JSON、ZMQ、block hash、Prometheus label 或 map
清理循环。

Gateway 主流程同样只保留：admission、bounded body、Select、stream、Complete。请求 body 只保存
一次，选路后以新的 reader 转发原始字节；响应不做完整缓存。

## 7. 路由输入与算法边界

纯 routing 输入至少包含：

```text
RequestProfile {
  prompt_tokens
  expected_output_tokens (optional)
  session_hint_digest (optional)
  cache_signal_status
}

EndpointSnapshot {
  backend
  cached_prefix_tokens Sample[int]
  queued_work Sample[int64]
  local_inflight
  prefill_rate Sample[float64]
  kv_usage Sample[float64]
  eligibility
}
```

第一版策略：

1. 过滤不合格 endpoint；
2. 计算 `uncached_tokens`；
3. 根据 queued work/prefill rate 估算等待与 prefill 成本；
4. 使用 hard overload guard；
5. 用最小 benefit margin/hysteresis 避免为很小 cache 差异抖动；
6. exact 无效时回到 load-aware；
7. session hint 只做 tie-break；
8. 返回 typed policy/reason/degradation 和解释字段。

禁止第一版加入十几个归一化指标和任意权重。每个公式项必须有量纲、来源、缺失语义和测试。

## 8. 并发、生命周期和 HA

### Goroutine owner

- discovery watcher 由 discovery owner 启停；
- observation collector 由 observation owner 启停；
- 每 Pod KV subscriber 由 kvcache lifecycle owner 启停；
- composition root 只负责按反序 `Close/Wait`；
- 请求路径禁止 fire-and-forget goroutine。

每个 goroutine 都必须有 context/cancel、退出 wait、错误指标和不会永久阻塞的 channel/backpressure
说明。

### 本地索引与多副本

Lite MVP 不使用共享数据库。每个 Gateway 副本建立本地索引：

- 适合 2–8 个 endpoint 的小集群；
- 避免 Redis/一致性故障域；
- 使用相同事件和输入时应产生相同决策；
- 副本间瞬时事件延迟允许不同，但必须通过 freshness/degradation 可见；
- 规模超出内存/连接预算时切换 Standard mode 或重新开 ADR。

## 9. 部署边界

### Lite 必需资源

- Namespace（或明确使用已有 namespace）；
- dedicated ServiceAccount、Role/RoleBinding；
- ConfigMap；
- Gateway Deployment 和 ClusterIP Service；
- readiness/liveness/startup probes；
- requests/limits、安全上下文和非 root；
- PDB 和滚动发布策略；
- vLLM KVEvents/replay 内部端口；
- CNI 支持时启用 default-deny + 明确 allow 的 NetworkPolicy；
- 可选 PodMonitor/ServiceMonitor。

Service 只负责把客户端连接送到 FishMesh；FishMesh 根据 EndpointSlice 选择后直接连接具体 vLLM
Pod IP，不能再把已选择请求交给随机 backend Service。

### Standard 额外资源

- GatewayClass/Gateway/HTTPRoute；
- InferencePool；
- EPP Deployment/Service/RBAC/ConfigMap；
- Gateway 到 EPP 的 ext_proc 和 failureMode 配置。

## 10. 次要上下文

### Diagnostics

`fishmesh-analyst`、Diagnostics domain/application/adapters/delivery 全部冻结。只允许安全或构建
修复；R6C 从默认镜像、默认部署和 README 主流程移除。不得为 exact KV 增加新 collector 或让
Analyst 进入请求路径。

### Simulator

`internal/simulator` 保留现有 delay、queue/running、HTTP error、stream abort、held stream 和
控制 API，用于 deterministic fault/race regression。R6 不增加模拟 KVEvents 来替代真实门禁；
只有生产实现已有 KV event 状态机后，才允许加入最小 deterministic fixture 防回归。

### Workload

loadgen 只作为独立客户端和有限 benchmark 工具，不 import Serving 内部包，不进入 product
image，不扩展成实验平台。

## 11. 演进顺序

1. R6A：真实集群 signal spike，无生产抽象；
2. R6B-1：tokenization contract + adapter；
3. R6B-2：kvcache contract + bounded index + lifecycle；
4. R6B-3：routing policy + conformance；
5. R6B-4：requestpath/gateway 接入；
6. R6B-5：llmd precise match 翻译；
7. R6C：Lite deploy/release/operability；
8. R6D：有限性能和资源对照；
9. R6E：Standard deploy/wire closure。

behavior change、机械包移动、第三方升级和部署变化分别提交。每个阶段都必须可构建、可回滚并
更新阶段文档。
