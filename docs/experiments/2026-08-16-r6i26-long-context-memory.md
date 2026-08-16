# R6I-26 Long-context low-concurrency and index memory experiment：长上下文低并发与索引内存实验

## 结论先行

本轮将重点从短上下文移到 2048、3072 和 3584 prompt tokens，并把客户端并发降到 4。当前
vLLM 的 `max-model-len=4096`，因此没有未经验证地切到 8K；3584 档加上 `max_tokens=32`
仍在当前模型上限内。

两侧各执行 2 轮、每轮 288 请求，共 1152 个正式请求，全部成功：

- 纯 `load-balanced` pooled TTFT P95 为 `173.74 ms`；KV-aware 为 `183.65 ms`，变化
  `+5.71%`，bootstrap 95% CI `[+1.14%, +8.36%]`。在本轮低并发、低 offered load 下，KV-aware
  没有形成整体尾延迟收益。
- TTFT P50 为 `56.22 -> 59.47 ms`（`+5.78%`），P99 为 `209.83 -> 236.34 ms`
  （`+12.64%`）。Gateway accepted QPS 基本相同（`1.8629 -> 1.8633`），Little's Law `W`
  为 `195.15 -> 201.16 ms`（`+3.08%`）。
- 收益只在个别长 shared 档出现：3072-token shared 的两轮 run-level P95 平均下降约
  `25.05%`；但 3584-token shared 反而上升约 `43.96%`，因此不能把单一长度/前缀结果推广成
  KV-aware 总体收益。
- KV 路径 576/576 为 `available`，没有 degradation、short-context bypass 或 admission
  rejection；random 场景的 `available + 0 cached tokens` 是真实 miss，不是失败。KV cached-prefix
  samples 为 `576/576`，累计 cached prefix tokens 为 `910,528`。

## 请求规模与运行条件

此前 R6I-25 的路由矩阵是 `8 场景 × 3 batch × 32 = 768 请求/轮`，两侧各 2 轮；本轮改为更贴近
长上下文的低并发矩阵：

| 项目 | R6I-26 值 |
| --- | ---: |
| 长度档 | 2048 / 3072 / 3584 target prompt tokens |
| 前缀模式 | shared / mixed / random |
| 场景数 | 9 |
| 每场景 | 2 batch × 16 requests = 32 |
| 每轮 | 288 requests |
| 每 arm | 2 rounds = 576 requests |
| 客户端并发 | 4 |
| offered QPS | 1、2、4 |
| Gateway in-flight | 64 |
| upstream connections | 16 |
| admission | active，initial/min/max = 16/8/64，step=8 |
| vLLM | Qwen2.5-0.5B，vLLM 0.23.0，`max-model-len=4096`，GPU utilization=0.40 |

两个 arm 使用独立 vLLM generation，并在每轮使用独立 run nonce；Gateway、vLLM、GPU、计划和
active admission 边界保持一致。低并发是为了把长 prompt 的 prefill/命中差异从高并发排队噪声中
分离出来，不代表生产最大吞吐容量。

## Pooled 性能结果

| 指标 | 纯 load-balanced | KV-aware | 变化 |
| --- | ---: | ---: | ---: |
| 请求成功 | 576/576 | 576/576 | 0 |
| TTFT P50 | 56.22 ms | 59.47 ms | +5.78% |
| TTFT P95 | 173.74 ms | 183.65 ms | +5.71% |
| TTFT P99 | 209.83 ms | 236.34 ms | +12.64% |
| run median TTFT P95 | 170.90 ms | 181.33 ms | +6.10% |
| accepted QPS | 1.8629 | 1.8633 | +0.02% |
| average in-flight | 0.3636 | 0.3748 | +3.10% |
| Little's Law W | 195.15 ms | 201.16 ms | +3.08% |
| admission rejection QPS | 0 | 0 | — |

Pooled P95 的 bootstrap CI 使用 20,000 次重采样、固定 seed `20260816`。完整机器报告见
`artifacts/bench/r6i26-long-comparison-r2/comparison.{json,md}`。

## 按长度和前缀模式

下表是两轮中每个场景 run-level P50/P95 的平均值；每个 cell 共有 64 个请求，适合做分层观察，
不作为单场景 promotion 的统计显著性结论。

| 场景 | LB P50/P95 ms | KV P50/P95 ms | P95 变化 | KV cached tokens |
| --- | ---: | ---: | ---: | ---: |
| 2048 shared | 36.40 / 124.16 | 44.19 / 125.13 | +0.78% | 120,960 |
| 2048 mixed | 34.89 / 112.50 | 39.98 / 116.13 | +3.23% | 89,408 |
| 2048 random | 106.09 / 120.03 | 114.05 / 121.98 | +1.62% | 0 |
| 3072 shared | 40.42 / 80.03 | 49.34 / 59.98 | -25.05% | 189,184 |
| 3072 mixed | 39.09 / 184.54 | 45.05 / 214.76 | +16.38% | 134,464 |
| 3072 random | 151.17 / 160.70 | 160.45 / 164.17 | +2.16% | 0 |
| 3584 shared | 39.65 / 45.69 | 51.17 / 65.78 | +43.96% | 220,224 |
| 3584 mixed | 49.90 / 174.99 | 53.88 / 180.97 | +3.42% | 156,288 |
| 3584 random | 167.14 / 187.76 | 179.51 / 186.70 | -0.56% | 0 |

这组结果说明：KV locality 仍能在某些长 shared 请求中降低尾延迟，但低 offered load 下
tokenization、KV lookup、事件状态和跨 generation 差异会抵消收益；需要更高但仍受控的长上下文
arrival ladder，才能判断真实吞吐区间的收益曲线。

## Hash index 内存统计边界

当前默认 KV 配置为：`BlockSizeTokens=16`、`MaxIndexKeys=100000`、`MaxBackendsPerKey=8`。
上游 `llm-d-kv-cache v0.9.0` 的 `InMemoryIndex` 内部包含两个 Hashicorp LRU：

1. `requestKey -> PodCache`；
2. `engineKey -> requestKeys`。

该上游版本没有公开当前 live key count、底层 bucket 数或 `InMemoryIndex` 的 exact byte stats；
因此本轮没有把“整个 Gateway 的 RSS”误写成“哈希表精确内存”。本轮改进的是 benchmark 观测：
每个 Gateway metrics window 现在保存 `process_resident_memory_bytes` 和
`go_memstats_heap_alloc_bytes` 的 start/peak/end/delta。

两轮平均内存窗口如下：

| arm | RSS start/peak/end MiB | RSS peak-start | Go heap start/peak/end MiB | Heap peak-start |
| --- | ---: | ---: | ---: | ---: |
| pure LB | 32.96 / 37.09 / 35.52 | 2.55 MiB | 3.84 / 7.19 / 4.97 | 3.35 MiB |
| KV-aware | 36.99 / 63.91 / 39.36 | 2.37 MiB | 4.23 / 30.83 / 6.03 | 26.61 MiB |

因此，在相同 workload 下，KV arm 的峰值相对 LB 高约 `26.82 MiB RSS`、`23.65 MiB Go heap`。
这与本地 index 填充高度相关，但还包含 tokenizer、ZMQ subscriber、KV event payload、Prometheus
状态和请求暂存，严格说是“KV-aware 运行时额外内存的观测上界/近似”，不是 hash bucket 的精确
拆分。要得到 hash-only bytes，需要上游提供 stats API，或维护一个与生产 index 语义一致且不会
改变内存布局的 instrumented index；反射/unsafe 读取第三方私有字段不适合作为生产统计方案。

## Dynamic admission 与集群状态

实验 active admission 从 target 16 起步，边界为 8–64。KV 第二轮完成后观察到 target=64、
`admission_tuning_actions_total=6`、latest reason=`hold`，两侧请求均无 soft/hard rejection；
这说明在本轮低并发 workload 下 controller 主要扩容后停在上限，没有发生连接撤销。当前 R6I-26
报告保存的是 metrics window 的容量与内存摘要，没有保存每个采样点的 target/reason 时间序列，
因此不把这三个最终值冒充完整动态轨迹。

本轮没有 vLLM Pod CPU/GPU runtime exporter；仍不能对 GPU utilization、GPU memory 或 runtime hard
gate 做性能归因。实验结束后已恢复：`load-balanced`、admission `off`、max in-flight `128`、
max connections `32`、vLLM `gpu-memory-utilization=0.35`、`max-model-len=4096`。

## 产物

- 计划：[r6i26-long-context-memory.json](../../configs/r6i26-long-context-memory.json)
- Overlays：[KV-aware](../../deploy/experiments/r6i26-long-context/kv-aware-active/)、[LB](../../deploy/experiments/r6i26-long-context/load-balanced-active/)
- LB runs：`artifacts/bench/r6i26-lb-long-r{1,2}/`
- KV runs：`artifacts/bench/r6i26-kv-long-r{1,2}/`
- Pooled comparison：[r6i26-long-comparison-r2](../../artifacts/bench/r6i26-long-comparison-r2/comparison.md)
- Memory observation implementation：`internal/workload/client/bench_metrics.go`
