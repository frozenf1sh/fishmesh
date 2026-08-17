# kvcache 包完全梳理：vLLM 官方能力、Client 调用链与本地索引设计

> 导读文档 · 面向第一次接触本项目的读者
> 范围：`internal/serving/kvcache/` 全部实现 + 上游 `github.com/llm-d/llm-d-kv-cache v0.9.0` 关键源码 + 调用方（composition / requestpath / routing）
> 关联文档：[ADR-002](../design/decisions/002-lite-kv-aware-routing.md)、[serving-domain-redesign §11](../design/serving-domain-redesign.md)、阶段 17–25 / 31 / 33 / 34 / 55

---

## 0. 一句话定位

`kvcache` 是 FishMesh Gateway 的 **KV-aware 路由数据源**：它订阅每个 vLLM Pod 通过 ZMQ 发布的 **KVEvents**（block 的 store/remove/clear 事实），在进程内维护一个**有界的、逐 Pod 的 KV block 前缀索引**，并在每次请求时回答「**每个候选 backend 与本请求 prompt 的最长公共前缀命中几个 block**」。

```text
vLLM Pod (自动前缀缓存)  --ZMQ KVEvents-->  kvcache 本地索引  --Lookup-->  requestpath  --KVAwareInput-->  routing 成本式选路
```

包注释原文：「维护逐 vLLM 实例的真实 KV block locality，并提供逐 backend 前缀命中快照。这个包拥有事件 sequence、replay freshness、Pod UID 生命周期和索引容量。它不负责分词、backend 选择或 fallback；无效状态必须由调用方显式降级，不能解释成普通 cache miss。」

---

## 1. 包内文件地图

| 文件 | 职责 | 关键类型 |
| --- | --- | --- |
| `kvcache.go` | 包契约：全部值对象、接口、错误码、Reason 枚举 | `Instance` / `Event` / `Query` / `Match` / `Snapshot` / `Index` / `EventSource` / `EventObserver` / `Error` |
| `config.go` | 资源边界配置与校验、依赖注入 | `Config` / `Dependencies` / `Clock` |
| `kvcache_impl.go` | `service`：组装 store + streams，实现 `Index`（Lookup / State / Close） | `service` |
| `vllm_index_impl.go` | `vllmStore`：组合上游 parser/index/scorer，事件 apply 与兼容边界 | `cacheStore` 接口 / `vllmStore` / `eventFault` |
| `lifecycle_impl.go` | `eventStream`：逐 Pod 的 live 订阅 + 周期 replay + sequence 连续性 + freshness | `eventStream` |
| `replay_impl.go` | 周期 replay 循环、END 后 freshness 刷新、gap 恢复 | `runReplay` / `replayOnce` |
| `reconcile_impl.go` | Pod 生命周期对齐：以 Pod UID 为事务边界启动/停止/清理 subscriber | `Reconcile` |
| `zmq_impl.go` | ZMQ transport：SUB 实时流 + DEALER replay，帧解码 | `zmqSource` |
| `kvcache_test.go` / `lifecycle_impl_test.go` / `vllm_index_impl_test.go` / `zmq_impl_test.go` | 行为契约测试 | — |

依赖方向（AGENTS.md 强制）：`kvcache -> backend`；上游 `llm-d-kv-cache` / vLLM 事件类型**只允许出现在 adapter（vllm_index_impl.go / zmq_impl.go）内部**，对外只暴露协议无关值对象。

---

## 2. vLLM 官方能力对照

### 2.1 vLLM 侧（引擎端，本包不实现）

| 官方能力 | vLLM 侧形态 | 本项目对应 |
| --- | --- | --- |
| 自动前缀缓存 Automatic Prefix Caching | 按 block（默认 16 token，`--block-size`）对 prompt 前缀做 hash 链缓存，命中即复用 KV | 索引的**事实来源**（读 block_hashes） |
| KVEvents（v0.10.0+） | `--kv-events-config '{"enable_kv_cache_events":true,"publisher":"zmq","endpoint":"tcp://…:5557","topic":"kv@${POD_IP}@${MODEL}"}'` | `zmqSource.Follow` 订阅 + `VLLMAdapter` 解析 |
| ZMQ 实时流 | PUB/SUB，3 帧消息 `[topic, 8 字节大端 sequence, msgpack payload]` | `decodeLiveMessage` |
| Replay 补偿 | ROUTER/DEALER（或 REQ）：发 8 字节起始 sequence，逐条回 `[seq, payload]`，END sentinel 结束 | `zmqSource.Replay` + `decodeReplayMessage`（END = sequence == `^uint64(0)`） |
| Topic 格式 | `kv@<pod-identifier>@<model>`，pod-identifier 通常为 `<pod-ip>:8000` | `instanceTopic` / `topicPrefix = "kv@"` |
| msgpack wire format | `msgspec array_like=True, omit_defaults=True, tag=True` 的 tagged union；`EventBatch = [ts, events[]]` | `engineadapter.NewVLLMAdapter()`，兼容 array 与 map（`type` 字段）两种编码 |
| BlockStored 字段 | `[tag, block_hashes, parent_block_hash, token_ids, block_size, lora_id, medium, lora_name, extra_keys, group_idx, kv_cache_spec_kind, kv_cache_spec_sliding_window]`（尾字段可缺省） | `applyStored` 逐字段兼容性校验 |
| BlockRemoved 字段 | `[tag, block_hashes, medium, group_idx]` | `applyRemoved` |
| AllBlocksCleared | 无 payload（RLHF 权重更新等触发整 Pod 清缓存） | `applyOne` → `Clear` |
| 哈希算法 | `--prefix-caching-hash-algo sha256_cbor_64bit`；block hash = FNV-64a over **CBOR canonical** `[parent, tokens, extra]`，parent 链式累加 | `kvblock.ChunkedTokenDatabase`（FNV-64a + CBOR 与 vLLM 逐位对齐） |
| HashSeed | `PYTHONHASHSEED` 环境变量（两端必须一致，`getInitHash` 用 FNV-64a(seed) 作为链起点） | `Config.HashSeed`，由部署侧对齐 |
| 多模态 / LoRA / cache_salt | `extra_keys`（每 block 的 MM feature identifier 列表）taint 哈希 | `BlockExtraFeatures` / `foldCacheSalt`（cache_salt 只进首个 block） |
| 多设备层 / HMA 多 cache group | `medium`（gpu/cpu）、`group_idx` + `kv_cache_spec_kind`（full_attention / mla / sliding_window …） | **只支持 `gpu` 层 + `group_idx==0` + full_attention + 无 sliding window**，其余显式拒绝 |

### 2.2 上游库 `llm-d-kv-cache v0.9.0`（本项目复用的部分）

| 上游组件 | 用法 | FishMesh 包装点 |
| --- | --- | --- |
| `kvblock.NewChunkedTokenDatabase` | token → 请求侧 block hash 链（`TokensToKVBlockKeys`） | `newVLLMStore` 构造；`requestKeys` / `applyStored` 两处调用 |
| `kvblock.NewInMemoryIndex` | 有界本地索引（LRU：requestKey → PodCache；engineKey → requestKey 映射） | `newVLLMStore` 构造；`Lookup/Apply/Clear` 代理 |
| `upstreamcache.NewKVBlockScorer`（LongestPrefixMatch） | 对连续命中 chain 逐 key 取交集，累加 device tier 权重 | `newVLLMStore` 构造；`vllmStore.Lookup` 调用（`gpu` weight=1） |
| `kvevents.EngineAdapter` / `engineadapter.NewVLLMAdapter()` | 解析 topic + msgpack payload 为领域事件 | `vllmStore.Apply` |

### 2.3 关键差异：FishMesh 自研了什么（ADR-002 §4/§6/§7）

1. **不用上游 `SubscriberManager` / `zmqSubscriber` / `Pool`**。上游只做 live 订阅 + 无回执 worker queue，**不负责 sequence gap、replay heartbeat、freshness TTL、Pod 删除后 index 清理**（ADR-002 §11 明确记录）。FishMesh 自己实现了 `eventStream` 双 goroutine（live + 周期 replay）补齐全部四件事。
2. **同步 apply 语义**：上游是异步队列（无回执），FishMesh 的 `EventSource.Follow/Replay` 以**同步回调**交付事件，`accept` 里 `Apply` 成功**之后**才推进 `lastSeq` —— 这是与上游 worker queue 的关键差异（`lifecycle_impl.go` 注释原文），天然形成背压并保证 sequence 语义可依赖。
3. **本地索引而非全局服务**：官方 kv-cache-manager 是独立部署 + Redis/Valkey 全局索引 + HTTP `/score_*` API；FishMesh Lite mode 是**进程内本地索引、多副本各自维护、无共享存储**（ADR-002 §6：多副本不要求逐位一致，但同一事件与请求输入下必须产生相同决策）。
4. **Pod UID 生命周期**：以 Kubernetes Pod UID 为唯一实例身份（IP 可能复用），reconcile 事务化处理启动/停止/清理。

---

## 3. Client 如何调用（完整链路）

### 3.1 装配（组合根 `cmd/fishmesh-gateway/composition.go`）

只有 `config.Routing.Mode == routing.ModeKVAware` 时才创建 KV 相关组件（产品模式边界，requestpath 不做运行时探测）：

```go
index, err = kvcache.NewVLLM(context.Background(), config.KVCache, kvcache.Dependencies{
    EventSource:   kvcache.NewZMQSource(),          // 无隐藏 goroutine 的 transport
    EventObserver: kvEventMetricsObserver{...},     // 只把观测翻译成 metrics
})
reconcile = kvAwareReconcile(index, config.Tokenization.Model) // discovery -> kvcache 的翻译闭包
```

- **`kvAwareReconcile`**：把 EndpointSlice 发布的 `[]backend.Backend` 逐条翻译为 `kvcache.Instance`（每次 reconcile 都按完整 membership 构造 desired，由 kvcache owner 决定 diff 与启停清理）。**翻译集中在组合根**：routing/requestpath 不识别 Kubernetes wire type、端口约定或 ZMQ topic。
- **`kvInstance`**：`backend.URL` 解析出 host → `PodIdentifier = host:8000`（**必须与 `kv@<pod-identifier>@<model>` topic 一致**）、`EventsEndpoint = tcp://host:5557`、`ReplayEndpoint = tcp://host:5558`、`PodUID` 取自 `backend.Metadata[backend.MetadataPodUID]`（**缺失即报错，宁可 reconcile 失败也不把新 Pod 事件接到旧缓存**）。
- **`kvEventMetricsObserver`**：`kvcache.EventObservation` → `gateway.Metrics.ObserveKVEvent`，只进 metrics，不反向影响选路（协议翻译点）。

### 3.2 请求路径（`internal/serving/requestpath/requestpath_impl.go`）

每个请求在选路前执行 `buildKVAwareInput`（仅 `ModeKVAware`）：

```text
并行:
  goroutine A: tokenizer.Tokenize(input)        → tokenization.Result (Model / CacheSalt / Prompts[].TokenIDs)
  goroutine B: s.kvReconcile(ctx, candidates)   → 选路前对齐最新 Pod membership（desired reconcile）
汇合后:
  query := kvcache.Query{Model, CacheSalt, Backends: 候选 backend IDs}
  for prompt: query.TokenGroups = append(..., prompt.TokenIDs())
  snapshot, err := s.kvCache.Lookup(ctx, query)          ← 核心调用
  kvInput := routingInputFromMatches(profile.TotalTokens(), snapshot.Matches())
  if !kvInput.UsableFor(candidates) → 降级 load-balanced (ReasonKVAwareSignalUnavailable)
```

关键语义（`routingInputFromMatches` / `routing.KVMatch` / `KVAwareInput.UsableFor`）：

- `kvcache.Match` 被**投影**为 `routing.KVMatch{Valid, MatchedTokens}` —— routing 不暴露 kvcache 或第三方类型；
- **`Valid=false` ≠ 零命中**：未知/过期/模型不一致都显式标记 invalid，`UsableFor` 只要有一个候选 invalid 就整体不可用 → 显式降级到普通 load-balanced（不会把「不知道」当「没命中」）；
- `State()` 侧：`projectKVCacheState` 把 `kvcache.StateSnapshot` 投影为 requestpath `State.KVCache`（每 backend 的 valid/reason/freshness/sequence/batch 计数），供观测与 readiness 说明 KV-aware 能力降级。

### 3.3 策略消费（`internal/serving/routing/kv_aware_impl.go`）

```go
// kvAwareCost：等价未缓存 token 成本
cost = PromptTokens - match.MatchedTokens          // 未命中部分（prefill 成本）
     + queue × QueueTokenPenalty                   // 排队请求（最难受）
     + running × RunningTokenPenalty               // 正在执行
     + inflight × InflightTokenPenalty             // 本 Gateway 在途增量
```

- 候选先排除硬过载（`HardOverload`：queue/running/runtime CPU/GPU 阈值），全部过载时退化为 least-loaded；
- `saturatingAdd/Multiply` 防溢出（上限 `2^63-1`），cost 永不取负；
- 另有 static-ttft 估算分支（`Estimates`）与预测器 shadow 分支，均以同一份 `KVMatch` 投影为特征。

### 3.4 关闭路径

`runtime.Close()` → `kvCache.Close()`：幂等取消全部 stream → 逐 stream `Invalidate(ReasonClosed)` + `Close()`（等 live/replay goroutine 退出）→ `store.Clear(podIdentifier)` 清理索引归属，保证生命周期 Clear 后不会有旧事件重新写回。

---

## 4. 本地索引设计（内部实现）

### 4.1 数据结构：双层 hash 体系（核心）

```text
engineKey（vLLM 事件携带的 block hash，ExternalBlockHash）
   ↕ engineToRequestKeys（LRU 映射，Add 时按长度比例 1:1 / many:1 / 1:many 建链）
requestKey（FishMesh 用 token 重建的 canonical hash chain）
   → data（LRU<requestKey, PodCache>，PodCache = LRU<PodEntry>，PodCacheSize = MaxBackendsPerKey）
```

- **写路径（Add）**：`BlockStored` 事件携带 `block_hashes`（engine key）与 `token_ids`；FishMesh 用同一模型/seed 从 `parentHash`（先 `GetRequestKey` 解析 engine→request）重建 request key 链，然后 `index.Add(engineKeys, requestKeys, entries)` 同时落两张表。
- **读路径（Lookup）**：请求 token → `TokensToKVBlockKeys`（同一 hash 算法）→ `InMemoryIndex.Lookup`（逐 key 查 PodCache，命中即继续，链断即停止）→ `LongestPrefixScorer.Score` 沿 key 顺序做 active pods 交集，累加每个存活 pod 的权重 → 每个 pod 的命中 block 数（`math.Round(score)`）。
- **Evict（BlockRemoved）**：engine key → `engineToRequestKeys` 解析出 request keys → 逐 request key 移除 pod 条目；全部空则连 engine 映射一起删。
- **Clear（AllBlocksCleared / Pod 退役）**：O(N) 扫描全表删除指定 podIdentifier 的条目（Clear 在热路径外，低频）。

> 关键点：**engine key 与 request key 的哈希链必须逐位一致**，依赖两端 BlockSize、HashSeed(PYTHONHASHSEED)、hash algo(sha256_cbor_64bit)、CBOR canonical 编码完全对齐 —— 这就是为什么 vLLM 部署参数（`--block-size`、`PYTHONHASHSEED`、`--prefix-caching-hash-algo`）与 Gateway 配置必须严格匹配（ADR-002 §9、阶段 18 已验证）。

### 4.2 事件流与 sequence 严格性（`lifecycle_impl.go` + `replay_impl.go`）

每个 `eventStream` 对应**一个 Pod UID**，`Start()` 起两条 goroutine：

```text
runLive  : source.Follow(ctx, instance, handler=accept(event, replayed=false))
            异常 → 记录错误 → 非 fatal → ReconnectDelay 后重连（hasFatalReason 才退出）
runReplay: 立即 replayOnce → 之后每 ReplayPeriod(2s) replayOnce
            replayOnce: source.Replay(ctx, instance, nextSequence, handler=accept(event, replayed=true))
                        只有收到 END sentinel 后才刷新 lastReplayAt + 按序恢复 reason
```

`accept` 的同步处理管线（`processMu` 串行化 live/replay 事件落地 index）：

1. 大小门禁 `MaxEventBytes`、topic 精确匹配 `kv@<pod-identifier>@<model>`；
2. `classifySequence`：期望 `lastSeq+1`；
   - `< expected` → 重复，丢弃（`duplicateBatches++`）；
   - `== expected` → 继续；
   - `> expected` 且 live → `ReasonSequenceGap` + 记录 `gapUntil`（等 replay 补）；
   - `> expected` 且 replay → **不可恢复**，`ReasonUnrecoverableSequenceGap`（replay buffer 起点比期望晚，说明丢失无法补偿）；
3. `store.Apply`（同步，成功才继续）→ 推进 `lastSeq` / `lastEventAt` / `appliedBatches`；
4. 发布 `EventObservation`（publisher timestamp 与本地时钟偏差受控，无有效时间戳则不产生，缺失≠零延迟）。

**freshness 语义**：只有 replay 成功收到 END 才 `lastReplayAt = now`；`Snapshot()` 里 `freshness = now - lastReplayAt`，超过 `FreshnessTTL(5s)` → `Valid=false`（reason 记为 `ReplayHeartbeatStale`）。gap 恢复条件：replay 后 `lastSeq >= gapUntil` 才从 `ReasonSequenceGap` 回到 `ReasonNone`。

### 4.3 Pod 生命周期（`reconcile_impl.go`）

`Reconcile(ctx, desired []Instance)` 以 **Pod UID 为事务边界**（`reconcileMu` 串行化）：

1. `validateInstances`：去重（backend / UID / podIdentifier 均不可重复）+ `MaxInstances(8)` 上限 + 端点合法性；
2. `reconcileChanges` diff：`Same(instance)` 且未关闭 → 复用；否则旧的进 `retired`、新建进 `additions`；desired 里消失的也进 `retired`；
3. **顺序约束（防竞态）**：先对 retired 逐个 `Invalidate(ReasonLifecycleChanging)` + `Close()`（等 goroutine 退出）**再** `store.Clear(podIdentifier)` —— 否则迟到事件可能在 Clear 后重新写回旧 locality；之后才发布新 streams 并 `Start()`。

**为什么用 UID 不用 IP**：Pod IP 与 backend ID 都可能被复用，只有 UID 变化能确定这是新缓存实例（`WorkloadUID` 注释原文）。

### 4.4 Lookup 的 4 步（`kvcache_impl.go`）

1. `validateQuery`：模型/prompt/backend 去重校验 + `MaxQueryTokens(131072)` / `MaxCacheSaltBytes(1024)` 门禁（**先于 hash 计算保护 CPU/内存**）；
2. `lookupInputs`：锁内对齐候选 streams → 未知 backend 直接建 `Match{Reason: BackendUnknown}`；模型不一致 → `ModelMismatch`；invalid → `invalidMatch`（保留 reason，**不解释成 miss**）；
3. `store.Lookup`（只查 freshness 有效且模型一致的实例）；
4. 发布前**再次** `currentState` 复核（防并发 Pod 替换后发布旧实例命中，`ReasonLifecycleChanging`）。

### 4.5 兼容边界（`vllm_index_impl.go`，把协议/兼容错误映射为稳定 reason）

- `BlockStored`：`block_size` 必须等于本包 `BlockSizeTokens`；拒绝 LoRA（`lora_id/lora_name` 非空）；拒绝 HMA 非默认组（`group_idx != nil && != 0`，或 `kv_cache_spec_kind != full_attention`，或 sliding window 非空）；`tokens` 必须完整 block 对齐；`extra_keys` 粒度必须等于 block 数；device tier 仅 `gpu`；
- `BlockRemoved`：拒绝 HMA 移除（`group_idx != 0`）；
- 其余事件类型 → `ReasonUnsupportedEvent`（未来 vLLM 加事件类型时安全失败，而不是静默忽略）；
- 任何失败路径都 `invalidateAndClear`：先 `Invalidate(reason)` 再 `store.Clear(podIdentifier)` 清掉可能的部分索引，然后由调用方决定降级。

### 4.6 配置默认值（`internal/serving/config/defaults.go`）

| 配置 | 默认 | 含义 |
| --- | --- | --- |
| `BlockSizeTokens` | 16 | block token 数，必须等于 vLLM `--block-size` |
| `HashSeed` | （空字符串） | 必须等于 vLLM `PYTHONHASHSEED` 的 FNV-64a 结果 |
| `MaxIndexKeys` | 100_000 | 本地索引 LRU 容量（requestKey 数） |
| `MaxInstances` | 8 | 同时订阅的 Pod 数上限 |
| `MaxBackendsPerKey` | 8 | 每个 requestKey 可登记的 Pod 数（必须 ≥ MaxInstances） |
| `MaxEventBytes` | 4 MiB | 单条事件 batch 上限 |
| `MaxReplayEvents` | 4096 | 单次 replay 上限（超出 → `ReplayCapacityExceeded`） |
| `MaxQueryTokens` | 131072 | 单查询 token 总数上限 |
| `MaxCacheSaltBytes` | 1024 | cache_salt 长度上限 |
| `ReplayPeriod` / `ReplayTimeout` / `FreshnessTTL` | 2s / 3s / 5s | 心跳周期 / 超时 / 新鲜度 TTL（TTL 必须 > Period，否则配置校验失败） |
| `ReconnectDelay` | 1s | live 流断线重连间隔 |

### 4.7 锁层次（并发正确性）

```text
service.reconcileMu  串行化 Pod 生命周期事务（Reconcile / Close）
service.mu           只保护 streams publication 与 closed（不持有它做外部 I/O）
eventStream.processMu 串行化单流事件落地（live 与 replay 互斥 apply）
eventStream.mu        只保护可发布状态（reason/seq/freshness 等）
```

约定（注释原文）：**不能在持有发布锁的情况下执行外部 I/O**；锁内只做状态切换，I/O 在锁外完成。

---

## 5. 演进脉络（对应阶段文档，可按需深读）

| 阶段 | 内容 |
| --- | --- |
| 17 | R6 方向复位，确立 KV-aware 主线 |
| 18 (R6A) | 真实集群 spike 闭环：跨 session 公共前缀、eviction、restart 清理、断流降级；确认 llm-d-kv-cache v0.9.0 可用但 SubscriberManager 不负责 sequence/replay/清理 |
| 19 (R6B-1) | 真实分词能力域（vLLM Render API 适配） |
| 20 (R6B-2) | **真实 KV 状态域（本包 contract + store + lifecycle 落地）** |
| 21 (R6B-3) | 缓存/负载联合纯路由（kv-aware 策略） |
| 22 (R6B-4) | 请求路径 KV-aware 编排（tokenize ∥ reconcile → Lookup → 降级） |
| 23 (R6B-5) | 有界 Body 与 KV-aware 交付 |
| 24 (R6B-6) | 组合根真实 KV 接入（composition.go 的翻译闭包） |
| 25 (R6C) | Lite 产品化（simulator/loadgen 移出主交付） |
| 31 (R6F) | KVEvents 与逐请求观测契约（`EventObservation` → metrics） |
| 33 (R6H) | 成本式 KV-aware 路由（等价 token 成本 + 硬过载门禁） |
| 34 (R6H-2) | 预测 TTFT 影子契约（预测器不反向影响选路） |
| 55 | Tokenization 与 KV 并发边界（tokenize ∥ reconcile 的并行与降级边界） |

---

## 6. 阅读建议（新人路径）

1. 先读 `kvcache.go`（契约与枚举）→ `config.go`（边界）→ `kvcache_impl.go`（编排骨架）；
2. 再读 `vllm_index_impl.go`（与 vLLM/上游的接缝，理解 engineKey/requestKey 双层 hash）；
3. 然后 `lifecycle_impl.go` + `replay_impl.go`（sequence 与 freshness，这是本项目相对上游的核心增量）；
4. 最后从 `composition.go` 的 `kvAwareReconcile`/`kvInstance` 看入口，从 `requestpath_impl.go` 的 `buildKVAwareInput` 看出口；
5. 对照 ADR-002 §4（信号契约）、§5（联合选择）、§6（高可用边界），以及阶段 20/22 的验收记录。
