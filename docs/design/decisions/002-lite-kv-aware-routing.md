# ADR-002｜以轻量独立网关承载真实 KV cache 感知路由

- 状态：已接受，R6A 真实集群可行性验证通过
- 决策日期：2026-08-11
- 影响范围：项目定位、MVP、standalone/integrated 主次关系、路由输入、部署与实验路线
- 关联决策：[`ADR-001`](001-llmd-router-integration.md)

## 1. 为什么重新决策

R5C 已证明 FishMesh 策略可以编译进 llm-d Router，并通过标准 EPP 生命周期运行。但如果把
`fishmesh-epp` 和一个 session-key scorer 作为唯一生产交付物，会出现三个问题：

1. 已经实现并在 K3s 运行的 Go HTTP/SSE 数据面、EndpointSlice 发现、故障状态与资源回收被
   降为“开发工具”，交付物与工程投入不匹配；
2. 当前策略主要依赖客户端传入会话键，只能保持同一会话的亲和，不能发现不同会话共享的
   system prompt；
3. llm-d 已提供 session、prefix-cache、load、queue 和 predicted-latency 等完整能力。继续给
   scorer 增加近似规则或代码行数，不能形成清晰的产品边界。

因此需要重新回答：FishMesh 的主产品是什么，怎样获得真实 KV cache locality，以及怎样继续
复用 Envoy/llm-d 而不让项目退化为插件示例。

## 2. 决策

FishMesh 采用以下产品与架构边界：

1. **Lite mode 是主要交付物。** `fishmesh-gateway` 作为独立 Kubernetes LLM Router，负责
   OpenAI HTTP/SSE 数据面、动态 endpoint、真实 KV cache 感知、负载与故障联合选择以及完整
   可观测性。
2. **Standard mode 是标准生态交付物。** 保留 Envoy-compatible Gateway、InferencePool 和
   llm-d EPP 集成；`fishmesh-epp` 复用上游 runtime，并把 llm-d 产生的真实 prefix match 数据
   翻译给同一份 FishMesh 纯策略。
3. **真实 KV cache locality 是目标主能力。** Lite mode 通过 vLLM KVEvents 获取 block
   store/remove 事实，通过 vLLM Render API 获取与模型模板一致的 Token IDs，再查询每个 Pod
   已缓存的最长公共前缀。
4. **复用上游 KV library，不复制 engine 细节。** Lite mode 优先通过薄 adapter 复用固定版本
   `llm-d-kv-cache` 的事件解析、block key 重建和索引能力；FishMesh 不自研另一套 vLLM wire
   protocol 或分词器。
5. **客户端会话键降为可选提示。** 当前 `X-FishMesh-Session-Key` 在兼容期内保留，但其语义
   明确为 session affinity。它只用于 KV-aware 数据未就绪、平局或短期稳定性，不再作为缓存命中
   的事实来源。
6. **实验只做工程门禁。** R6A 必须先在现有双 vLLM 集群证明事件、分词、跨会话公共前缀、
   eviction、重启清理和断流降级；门禁失败时不进入大规模代码实现。

## 3. 两种交付形态

### 3.1 Lite mode（主产品）

```text
OpenAI client
  -> fishmesh-gateway Service / Deployment
       -> bounded request body + vLLM render tokenization
       -> EndpointSlice / Pod lifecycle
       -> local KV-aware block index from vLLM KVEvents
       -> cache + queue/in-flight + health routing
       -> selected vLLM Pod IP
       -> SSE passthrough and outcome accounting
```

Lite mode 默认不要求 Envoy、Gateway API Inference Extension CRD、EPP、Redis 或自研
Controller。部署仍需标准 Kubernetes 生产资源：专用 ServiceAccount 和最小 RBAC、ConfigMap、
Deployment、Service、探针、资源上下限、PDB，以及在 CNI 确实支持时启用的 NetworkPolicy。

### 3.2 Standard mode（生态集成）

```text
OpenAI client
  -> Envoy-compatible Gateway
  -> llm-d EPP runtime
       -> llm-d parsing / token producer / precise prefix producer
       -> FishMesh cache-and-load routing policy
       -> llm-d picker / flow control / response lifecycle
  -> selected vLLM Pod
```

ADR-001 关于“不自研 ext_proc、不复制 InferencePool、subset 和 response lifecycle”的决定继续
有效。ADR-002 只改变 standalone 的产品地位，并把真实 prefix match 加入共享策略输入。

## 4. 真实 KV 信号契约

### 4.1 写路径：vLLM 到索引

每个启用 automatic prefix caching 的 vLLM Pod 通过 ZMQ 发布 KVEvents。索引至少处理：

- block stored 与 block removed；
- Pod UID、模型、block token、额外 hash key 与 cache salt；
- subscriber 重连和 replay；
- Pod 删除、重建和 endpoint membership 变化；
- 事件延迟、断流、丢失或无法重放后的 freshness 失效。

索引状态必须有内存上限，并可按 Pod 清理。不能因为某次事件曾经存在，就无限期声称该 Pod
仍拥有对应 cache。

### 4.2 读路径：请求到逐 Pod match

Lite mode 将受支持的 OpenAI 请求交给同模型的 vLLM Render API，取得真实 Token IDs。KV index
返回每个候选 Pod 的：

```text
KVMatch {
  matched_prefix_tokens
  prompt_tokens
  observed_at
  source
  valid
}
```

不同 session 只要拥有相同的前置 system prompt，就可以匹配相同的完整 KV blocks。全局累计
`prefix_cache_hit_rate` 不能替代上述逐请求、逐 Pod 数据。

### 4.3 降级

- KV-aware cache sample 新鲜：使用 cache + load 联合选择；
- cache sample 缺失、过期或事件流不可信：使用 load-balanced；
- load sample 也不可用：只从健康候选做确定性选择；
- discovery 过期或无可用 endpoint：执行 Lite mode 明确的 Service fallback/503 契约；
- Standard mode 空 subset：仍由 EPP 返回 503，禁止调用 Lite fallback。

任何降级都必须暴露 typed reason、metric 和结构化日志，不能把未知 cache 当作零命中后静默
继续声称 KV-aware routing。

## 5. 联合选择策略

策略核心接收协议无关的 request profile 和 endpoint snapshot。第一版保持可解释，不引入任意
权重黑盒：

1. 过滤 terminating、stale、open-circuit 或不符合 subset 的 endpoint；
2. 计算 `uncached_tokens = prompt_tokens - matched_prefix_tokens`；
3. 使用 queue、in-flight uncached tokens 和观测到的 prefill rate 估算等待与 prefill 成本；
4. cache 命中只有在预计收益超过切换余量时才改变选择；
5. hard overload guard 可以否决 cache locality，避免追逐严重过载的 Pod；
6. session hint 只作为平局或短期抖动控制，不覆盖真实 cache/load；
7. 返回 chosen endpoint、reason、cache source、matched tokens 和降级状态。

第一版不宣称算法创新。项目价值来自真实信号接入、有界状态、可靠降级、轻量数据面和标准
生态适配的完整交付。

## 6. 高可用与轻量边界

Lite mode 不在 MVP 引入共享数据库。少量 Gateway 副本分别订阅全部 vLLM Pod 事件，各自维护
有界本地索引。这样会重复一小部分内存，但避免新的常驻服务和一致性故障域。

必须满足：

- Pod UID 变化后旧 block 归属被清除；
- subscriber 不健康时 readiness/metric 能说明 KV-aware 能力降级；
- 多 Gateway 副本不要求逐位一致，但在同一新鲜事件和请求输入下必须产生相同决策；
- 只有实际规模证明本地复制不可接受时，才重新评估 Redis/Valkey 或独立索引服务；
- 大型、多池、多租户或共享 Gateway 场景优先使用 Standard mode，而不是继续扩张 Lite mode。

## 7. 开源能力所有权

| 能力 | FishMesh | 上游 |
| --- | --- | --- |
| vLLM KV event 格式与 block 重建 | 薄 adapter、freshness 和生命周期 | vLLM + `llm-d-kv-cache` |
| OpenAI 请求的真实 tokenization | 调用、超时、降级和结果契约 | vLLM Render API |
| Lite HTTP/SSE 数据面 | 实现并负责 | Go 标准库 |
| EndpointSlice、Pod 与状态回收 | 实现并负责 | Kubernetes API |
| cache/load/failure 联合策略 | 实现并负责 | 纯 FishMesh routing domain |
| Standard ext_proc/flow control | 不实现 | Envoy + llm-d |
| Model execution 和本地 KV cache | 不实现 | vLLM |

“使用开源库”不是削弱项目价值。FishMesh 的边界类似使用 `client-go` 构建 Controller：复用协议
和基础数据结构，自己拥有产品行为、状态生命周期、故障语义和交付形态。

## 8. 被拒绝的方案

| 方案 | 拒绝原因 |
| --- | --- |
| 继续以 session key 为主策略 | 无法利用跨会话公共 system prompt，产品能力过薄 |
| 自研 approximate prefix tree 作为主能力 | 与 llm-d 重复，且无法证明真实 cache residency |
| 从累计 vLLM hit rate 推断当前请求 | 指标不是逐请求、逐 Pod locality，语义错误 |
| 自研 KVEvents parser/indexer | 复制快速变化的 engine 细节，维护成本高 |
| 只交付 llm-d scorer | 产品退化为薄插件，已实现的数据面和运维能力无法成为交付物 |
| Lite mode 引入 Redis/Operator | 小集群尚无规模证据，会破坏轻量边界 |
| 放弃 Envoy/llm-d | 无法覆盖标准平台集成，也无法回答生态兼容性 |

## 9. 实施门禁

R6A 只有同时满足以下条件才允许进入 R6B：

1. 两个 vLLM Pod 均能发布、重放并停止发布 KVEvents；
2. FishMesh spike 能按 Pod 建立和清除 block locality；
3. 两个不同 session、相同长 system prompt 的请求得到非零公共 prefix match；
4. eviction 与 Pod restart 后不继续使用旧 locality；
5. event stream 过期时路由输入明确标记 invalid，并能降级到 load-balanced；
6. 记录 token render、index lookup、事件延迟和内存的初始成本；
7. 全部操作使用现有真实集群，不再扩展 simulator 来伪造成功。

若上游版本或接口不满足条件，先记录兼容性缺口并决定 pin/upgrade；不得以自行编造 cache
信号绕过门禁。

## 10. 对既有决策的影响

- ADR-001 的 EPP runtime、协议所有权、空 subset 和版本 pin 决策继续有效；
- ADR-001 中“standalone 只保留开发/演示”和“精确 KV cache 不进入项目”的范围被本 ADR
  替代；
- session-key-v1 保留为兼容与降级策略，不再代表最终主能力；
- R5D 标准部署不取消，但顺序后移到 Lite KV-aware MVP 和产品化之后；
- simulator、loadgen、analyst 与旧实验不删除历史，只从主产品和当前开发路线移除。

## 11. R6A 验证记录

2026-08-11 在现有双 vLLM 0.23.0 K3s 集群完成门禁：不同会话共享 system prompt 得到逐 Pod
128-token 公共前缀；subscriber 断开时信号先 invalid，随后从 replay 补回；真实缓存压力产生
`BlockRemoved` 并把旧命中降为零；旧 Pod UID 删除后索引清理，新 Pod 从 sequence 0 独立发布。

同时确认 `llm-d-kv-cache v0.9.0` 提供可靠的事件解析、block key 和索引能力，但它的
`SubscriberManager` 不负责 sequence gap、replay heartbeat 或 Pod 删除后的 index 清理。因此
R6B 仍复用上游 parser/indexer，只由 FishMesh `kvcache` owner 补齐 transport freshness 和
Kubernetes 生命周期。完整证据见[阶段 18](../../stages/18-R6A真实KV信号闭环.md)。
