# ADR-003｜以可校准 TTFT 估算替代固定 token 权重，并把在线学习限制在影子阶段

- 状态：已接受，R6I 分阶段实施
- 决策日期：2026-08-16
- 影响范围：Lite KV-aware 路由、backend 负载观测、HardOverload、prediction、压测产物与结论口径
- 前置决策：[`ADR-002`](002-lite-kv-aware-routing.md)

## 1. 背景与问题

R6H 已把 KV-aware 从“优先最大缓存命中”改为可解释的等价 token 成本：

```text
uncached_tokens
+ queue × queue_token_penalty
+ running × running_token_penalty
+ local_inflight × inflight_token_penalty
```

该实现解决了 cache locality 可以无限压过负载的问题，但还不是可交付的 latency model：

1. `512/128/64` 是未校准的固定起点，不能解释为目标硬件上的毫秒成本；
2. 长上下文增量 prefill 同时受总 prompt 和 cached prefix 影响，不能只由 uncached token 数线性代表；
3. queue/running 是 vLLM 已接收的状态，Gateway local in-flight 可能包含同一批请求，直接相加会重复计数；
4. backend observation 默认 15 秒一次，不能代表十几秒突发压测中的逐请求负载；
5. `HardOverload` 已有策略字段和单元测试，但 standalone 请求路径没有从真实 queue/local state 产生该事实；
6. prediction 已有有界非负 ridge 影子模型，但尚无真实集群误差证据，不能直接接管路由；
7. 现有长上下文报告使用 4/12 KiB 文本而非 4K/12K token，且跨 treatment 未清空 KV，不能作为模型校准数据。

项目不通过继续增加任意动态权重解决这些问题。未经约束的在线 `alpha/beta/gamma` 会产生反馈振荡、
选择偏差和不可解释的回归，反而削弱 Lite 产品的可靠性。

## 2. 决策

FishMesh 后续 KV-aware 主目标固定为：

> **在信号可信且候选安全时，选择预计 TTFT 最小的 backend；估算不可用时回到经过校准的静态模型，
> KV/load 不可信时继续使用既有 typed load-balanced fallback。**

正式交付顺序为：

1. 先修正 load observation 的时效、重复计数和 HardOverload 运行时接线；
2. 再交付以毫秒为单位、可版本化的 calibrated-static estimator；
3. 现有 prediction 只做 learned-shadow，先证明误差和候选一致性；
4. 只有影子门禁通过后，才单独决策 learned-active，且必须保留 static fallback 和 HardOverload；
5. 不引入 Redis、独立目录服务、Operator、远程 KV 或在线探索服务。

## 3. 目标请求模型

### 3.1 选择目标

第一版只优化 TTFT，不同时优化完整请求耗时。当前请求的输出长度对其自身 TTFT 影响较小，而正在运行
请求的 decode 会影响 GPU 可用计算；将 TTFT 与 E2E 混成一个 score 会让目标不可验证。

每个候选的目标估算为：

```text
EstimatedTTFT(candidate)
  = EstimatedQueueWait(candidate)
  + EstimatedIncrementalPrefill(candidate)
  + SafetyMargin(candidate)
```

Render 和本地 index lookup 是同一请求所有候选共享的选路前成本，不进入候选排序，但必须继续作为 Gateway
端到端开销单独观测。

### 3.2 增量 prefill 特征

稳定特征至少包含：

```text
prompt_tokens
cached_prefix_tokens
uncached_tokens = prompt_tokens - cached_prefix_tokens
```

理论上长上下文增量注意力工作近似受 `uncached × cached + uncached²/2` 影响；真实 vLLM 还会受 block、
chunked prefill、batch 和硬件影响。因此产品不直接宣称理论公式准确，而是用二维 profile：

```text
prefill_ms = f(prompt_tokens, cached_prefix_tokens)
```

第一版 `f` 使用有界、单调、版本化的分段线性 profile。没有目标硬件数据时不得把占位 profile 标为已校准。

### 3.3 负载特征与去重

负载快照至少包含：

```text
queue_depth
running_requests
load_observed_at
load_valid
current_local_inflight
```

R6I-1 先采用保守去重：

- queue/running 新鲜且完整时，成本只使用外部 load，不再额外叠加完整 local in-flight；
- 外部 load 缺失/过期时，使用 local in-flight 作为 Gateway 已知 fallback；
- HardOverload 仍可同时读取 queue 与 local in-flight，因为它是安全门而非可相加成本。

后续若需要补偿采样窗口，只增加 `local_delta`：在新 observation 首次进入 requestpath 时记录当时 local
in-flight，之后只计算 `max(0, current - observed_baseline)`。在该语义和测试落地前，不把三个计数直接相加。

## 4. HardOverload 契约

HardOverload 是候选安全边界，不属于学习模型。第一版规则为：

```text
hard_overload =
  (load_valid && hard_queue_depth > 0 && queue_depth >= hard_queue_depth)
  ||
  (hard_local_inflight > 0 && local_inflight >= hard_local_inflight)
```

- `0` 表示该门槛未启用，便于默认配置保持兼容；正式 KV-aware 实验 overlay 必须显式给出非零值；
- running 数量不直接作为硬门，因为 vLLM continuous batching 下“运行中很多”不等价于不可承接；
- 所有健康候选都 hard-overload 时，继续使用 typed hard-overload fallback，不把请求随机送入 Service；
- 未来可加入 `estimated_queue_wait >= hard_wait_budget`，但必须建立在校准模型之上。

HardOverload 由 requestpath 在构建不可变 routing snapshot 时发布，因为它合并了外部 observation 与本
Gateway 的实时 lease 状态；routing 负责强制执行，prediction 不得覆盖。

## 5. 观测时效与规模边界

当前双 backend Lite profile 使用直接抓取每个 vLLM `/metrics` 的方式。正式调度 profile 初始配置：

```text
interval:        500ms
max_age:         2s
request_timeout: 400ms
```

这些是受控起点，不是跨规模默认结论。真实验收同时记录抓取错误、Gateway CPU/RSS 和 vLLM metrics 开销。
若 500ms 抓取在当前两个 backend 上不可接受，先放宽周期并记录信号时效限制，不引入新的常驻服务。

扩展到大量 worker 时，逐 Gateway 抓取与本地 exact KV directory 的瓶颈分别是 O(N) load I/O、事件连接、
本地索引复制和候选扫描。该问题保留为 scale-out 设计分析；R6I 不为尚未出现的规模增加 push aggregator。

## 6. Estimator 所有权与依赖方向

R6I 不建立新的 `shared/common` 包，也不让 routing import prediction：

```text
prediction  -> backend + standard library
requestpath -> prediction + routing
routing     -> backend + observation
```

- `prediction` 继续拥有有界样本、拟合和置信度；
- `requestpath` 是把状态模型结果投影为一次请求不可变值的编排点；
- `routing` 只解释候选的稳定 `LatencyEstimate`，不访问模型状态；
- calibrated-static 与 learned-shadow 形成真实替换边界后才允许增加小接口，不为目录对称制造接口。

正式 active 接入前，必须另行更新当前“prediction 不参与 routing”的架构白名单与 ADR 状态。影子阶段不改变
既有依赖和行为。

## 7. Learned-shadow 约束

现有非负 ridge 模型继续使用，但扩展后也必须满足：

- 系数非负且有上界；
- 每 backend 最少样本和最大样本年龄；
- 最多每固定数量完成请求重拟合一次，不能每请求无界抖动；
- 输出 estimate、absolute error、confidence、model version 和 would-select；
- 不记录 prompt、Token IDs、session、原始 SSE、API key 或高基数 prefix identity；
- 未选 backend 没有反事实 TTFT，不能把 selected-only 样本解释为无偏训练集；
- 不在正式请求中加入随机探索。需要平衡样本时使用受控 calibration workload。

learned-active 的最低门禁：

1. 每 backend 在至少两个独立 run 中达到有效样本门槛；
2. MAE、P95 error 和 would-select agreement 均有预先定义的通过值；
3. shared-prefix、cold、skew/overload 都没有破坏 HardOverload、fallback 和成功率；
4. static 与 learned 可以通过一个配置原子回滚；
5. GPU/模型改变后旧 profile/model 明确失效，不跨硬件静默复用。

## 8. 决策可解释性

最终每个 bench request 必须能安全记录：

```text
prompt_tokens
cached_prefix_tokens
uncached_tokens
load_valid
load_sample_age_ms
queue_depth
running_requests
local_inflight
hard_overload_applied
estimated_ttft_ms
estimator_version
estimator_confidence
```

HTTP 默认响应不暴露全部候选或 prefix identity。逐请求 JSONL 只保存数值形状和最终 backend；候选级明细可进入
有界 debug log/实验 artifact，不进入 Prometheus 高基数 label。

## 9. 实验与缓存隔离契约

实验不是架构前置条件，但每个正式性能结论必须满足：

- 使用 actual prompt tokens，不再用 KiB 名称代替 token 档位；
- 每个 run 使用独立 `cache_salt` 或独立 vLLM cache generation；
- 明确区分 cold、controlled-warm 和 steady-warm；
- run nonce 进入 prefix 生成，`unique-0` 不得跨 treatment 意外复用；
- treatment 顺序随机或使用多轮平衡顺序，至少五轮；
- 保存 Git SHA、镜像 digest、Pod UID、vLLM args、observation mode、模型版本、estimator config 和随机 seed；
- 报告 pooled P50/P95/P99、跨轮中位数和置信区间，不只平均两个 run-level quantile；
- 失败 attempt、KV unavailable 和 load unavailable 全部保留。

当前 `max-model-len=4096` 下先验证 512/1024/2048/3072 token。只有双 vLLM 容量检查通过后，才把
`max-model-len` 提高并增加 4096/8192/12288 token；12 KiB 文本结果不能改名为 12K token。

## 10. 回滚与非目标

任一 R6I 切片都必须可以恢复：

```text
load-balanced
或
kv-aware + calibrated-static + prediction off
```

R6I 不实现 TLS、认证、计费、Redis、共享 KV store、远程 KV、Operator、CRD、在线探索、多模型混部或
Standard/llm-d 完整部署。它们不能帮助回答当前“何时值得为 locality 接受 queue”的核心问题。
