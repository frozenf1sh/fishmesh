# R6I-25：KV-aware + LB 降级与纯 load-balance 大规模性能对照

## 结论先行

本轮真实 GPU 集群实验覆盖 4,160 个请求：路由矩阵两侧各 1,536 个请求，动态 admission 专项两侧各 544 个请求；正式数据全部成功，未观察到 admission rejection、KV degradation 或 upstream error。

- 路由矩阵的 pooled TTFT P95：纯 `load-balanced` 为 `962.17 ms`，`kv-aware + load-balanced fallback` 为 `1057.30 ms`，描述性变化 `+9.89%`，bootstrap 95% CI `[-13.43%, +28.39%]`，因此整体没有统计上稳定的 P95 收益。
- 路由矩阵的 pooled TTFT P50：`77.60 -> 77.25 ms`，变化 `-0.45%`；Gateway accepted/completed QPS：`5.243 -> 5.422`，变化约 `+3.43%`；Little’s Law `W`：`1812.01 -> 1615.09 ms`，变化约 `-10.87%`。
- KV-aware 的收益集中在长上下文且有 locality 的场景：`long-shared-8` TTFT P95 下降 `17.57%`，`long-mixed-16` 下降 `23.29%`，`xlong-random-24` 下降 `26.50%`；512-token 短上下文旁路相对纯 LB 反而增加约 `4–5%` P95，这是主动牺牲 KV locality 换取旁路成本的结果。
- 动态 admission 在两侧都从 target `32` 逐步升到 `128`，每轮 12 次动作、无拒绝。高水位阶段只 hold；由于没有 hard rejection，recovery 阶段没有下调，反而继续按 `underutilized` 增长。这是当前控制器的真实行为，不应表述为已经实现了双向稳定收敛。
- 运行时 CPU/GPU hard gate 本轮仍不可评估：`FISHMESH_RUNTIME_*_HARD_LIMIT` 全为 `0`，Prometheus 没有 vLLM Pod 维度 runtime samples；报告没有虚构 GPU/CPU 性能归因。

## 实验边界

| 项目 | 值 |
| --- | --- |
| namespace | `kubellm` |
| Gateway image | `fishmesh-gateway:r6i24-admission-kv-bypass-r1@sha256:903c10b37dbc359946bff8dc592d3d6c96841e7f86afd4e35b607fda25c05553` |
| Model / vLLM | `qwen2.5-0.5b-instruct` / `vLLM 0.23.0` |
| GPU | RTX 4060 8 GiB，2 个 time-sliced vLLM Pod |
| Admission | active；initial `32`，min/max `16/128`，step `8`，interval `2s`，cooldown `5s`，watermark `0.25/0.80` |
| Backend observation | Prometheus，interval `500ms`，max age `2s`，EndpointSlice discovery |
| KV short bypass | KV arm 固定 `576` actual prompt-token 上界候选 |
| Runtime hard gate | 全部关闭；无 Pod CPU/GPU runtime sample |
| Matrix workload | 8 场景 × 3 batch × 32 请求 = 768 请求/轮；两侧各 2 轮 |
| Admission workload | low/medium/high/recovery，共 544 请求/轮；两侧各 1 轮 |
| Cache isolation | 每轮独立 run nonce；steady-warm routing，cold admission |

### Generation 约束

Gateway 重启会丢失 KV replay subscriber 状态，导致旧 generation 进入 `unrecoverable-sequence-gap`。因此不能把 LB overlay 热切换到 KV overlay 后继续声称同一 generation 的 A/B。正式数据采用两个独立 valid phase：纯 LB 使用 generation `qwen-vllm-79758748b8`，KV-aware 使用 `qwen-vllm-657848b977` 与 `qwen-vllm-5677f67964`；每个 KV phase 在新 vLLM generation 建立后确认两 backend `valid=1 / ready`。这保持了 model、vLLM、GPU、Gateway image、workload 和控制参数一致，但仍是跨 generation 的 profile 对照，因果归因必须保守。

此前在旧 generation 上完成的 `r6i25-lb-routing-r1/r2` 只作为诊断/预热证据，未纳入以下正式 pooled compare。

## 路由矩阵正式结果

以下数据 pooled across 两轮，每个场景共 192 个请求。

| 场景 | LB TTFT P50/P95 ms | KV+fallback TTFT P50/P95 ms | P95 Δ | LB E2E P95 ms | KV E2E P95 ms | KV 路径 | KV cached sample |
| --- | ---: | ---: | ---: | ---: | ---: | --- | ---: |
| short-shared-4 | 49.25/80.57 | 52.27/84.43 | +4.79% | 1469.03 | 1493.60 | short bypass 192 | 0/192 |
| short-shared-16 | 54.37/82.97 | 61.23/87.42 | +5.36% | 1543.85 | 1574.99 | short bypass 192 | 0/192 |
| short-random-4 | 78.32/107.12 | 83.93/111.46 | +4.05% | 1569.17 | 1574.53 | short bypass 192 | 0/192 |
| medium-mixed-8 | 70.06/142.92 | 66.26/143.85 | +0.65% | 2025.63 | 1940.16 | KV-aware 192 | 192/192 |
| medium-random-16 | 133.11/248.84 | 150.72/359.47 | +44.46% | 2199.33 | 2286.51 | KV-aware 192 | 192/192 |
| long-shared-8 | 63.12/95.60 | 49.89/78.81 | -17.57% | 1650.54 | 810.59 | KV-aware 192 | 192/192 |
| long-mixed-16 | 79.81/730.08 | 77.88/560.03 | -23.29% | 2842.35 | 2686.29 | KV-aware 192 | 192/192 |
| xlong-random-24 | 880.52/2232.99 | 932.19/1641.23 | -26.50% | 5065.45 | 4578.55 | KV-aware 192 | 192/192 |

### Pooled capacity and Little’s Law

| 指标 | 纯 load-balance | KV-aware + LB fallback | 变化 |
| --- | ---: | ---: | ---: |
| 请求成功 | 1536/1536 | 1536/1536 | 0 |
| TTFT P50 | 77.60 ms | 77.25 ms | -0.45% |
| TTFT P95 | 962.17 ms | 1057.30 ms | +9.89% |
| TTFT P99 | 1709.56 ms | 1562.83 ms | -8.59% |
| accepted QPS | 5.243 | 5.422 | +3.43% |
| completed QPS | 5.243 | 5.422 | +3.43% |
| admission rejection QPS | 0 | 0 | — |
| average in-flight | 9.500 | 8.757 | -7.82% |
| Little’s Law W | 1812.01 ms | 1615.09 ms | -10.87% |

Pooled TTFT P95 的 KV/LB bootstrap CI 为 `[-13.43%, +28.39%]`，跨过 0；因此本轮不能给出“KV-aware 整体降低 P95”的结论，只能给出分场景收益。

## KV-aware、降级与动态负载行为

正式 KV 路由矩阵两轮合计：

- `960/1536` 请求为 `kv-aware / available`；
- `576/1536` 请求为 `kv-aware-short-context-fallback / short-context-bypassed`，正好覆盖三个 512-token 场景；
- KV degradation 为 `0`，所有 valid phase 的 backend state 都从 `ready` 开始；
- cached prefix samples 为 `960/1536`，累计 cached prefix tokens 为 `819,952`；
- short bypass 场景没有 cached sample，这是预期的 locality trade-off，不是 KV lookup 失败。

后端路由呈现了预期的 load-aware + locality 行为：短上下文旁路和 random 场景在两个 backend 间近似均衡；`long-shared-8` 两轮均把全部 192 个请求保留在同一 backend，以保留 prefix locality；mixed 场景按可用 cache 与当前负载在两个 backend 间分配，而不是固定 hash。

Gateway metrics 原始采样中，稳定窗口两 backend 均为 `observation_status=ok`，没有 `unavailable`；每轮开始时出现少量 `degraded` 采样（LB 两轮 `9/491`、`5/490`；KV 两轮 `10/475`、`19/474`），对应 Gateway/vLLM observation 建立期。观测到的两 backend 聚合最大 queue/running 为：LB `9/16`，KV `6/16`；没有 backend runtime hard gate 样本。

## 动态 admission 专项

### 结果

动态专项各 544 个请求，单轮、独立 generation，结果主要用于行为观察而非多轮 promotion：

| 指标 | 纯 load-balance | KV-aware | 变化 |
| --- | ---: | ---: | ---: |
| 请求成功 | 544/544 | 544/544 | 0 |
| TTFT P50 | 124.13 ms | 112.90 ms | -9.05% |
| TTFT P95 | 471.97 ms | 381.95 ms | -19.07% |
| TTFT P95 bootstrap CI | — | `[-29.14%, -8.02%]` | 描述性单轮结果 |
| accepted/completed QPS | 4.139/4.139 | 4.088/4.088 | -1.24% |
| average in-flight | 14.882 | 15.212 | +2.22% |
| Little’s Law W | 3595.51 ms | 3721.24 ms | +3.50% |
| rejection QPS | 0 | 0 | — |

分 step 的 TTFT P95：

| step | LB P95 ms | KV P95 ms | 变化 |
| --- | ---: | ---: | ---: |
| low | 120.83 | 118.55 | -1.88% |
| medium | 147.33 | 128.70 | -12.65% |
| high | 532.23 | 408.97 | -23.07% |
| recovery | 115.87 | 103.84 | -10.38% |

### target/action 时间序列

两组都观察到相同的控制路径：

1. `step-low`：target `32 -> 80`，6 次 action；
2. `step-medium`：继续到 `96`，累计 8 次 action；
3. `step-high`：高水位时 `hold`，不继续扩大；
4. `step-recovery`：没有 hard rejection，target 没有回落，继续被 `underutilized` 推到 `128`，累计 12 次 action。

原始 metrics 中两组全程 `soft/hard/total rejection=0`，reason 主要是 `underutilized`、`underutilized-cooldown`、`hold`，没有 `overloaded`。这与当前实现一致：只有 hard rejection 才会主动降低 target；高 in-flight 只 hold，soft rejection 不反向降低 target，以避免长连接被 target cliff 伤害。因此，“动态 admission 已能升档和限速”，但“空闲恢复时自动降低并释放并发窗口”尚未被本轮实现/验证。

## 运行时指标与统计边界

本轮没有把 Pod CPU、RSS、GPU utilization、GPU memory、temperature 当作性能收益数据，因为当前 Prometheus 只 scrape Gateway，且 live config 中：

```text
FISHMESH_RUNTIME_CPU_HARD_LIMIT_CORES=0
FISHMESH_RUNTIME_MEMORY_HARD_LIMIT_BYTES=0
FISHMESH_RUNTIME_GPU_UTILIZATION_HARD_LIMIT_PERCENT=0
FISHMESH_RUNTIME_GPU_MEMORY_HARD_LIMIT_BYTES=0
FISHMESH_RUNTIME_GPU_TEMPERATURE_HARD_LIMIT_CELSIUS=0
```

所以本报告能确认的是 Gateway observation、admission、routing、KV 状态和请求级延迟；不能确认 runtime hard gate 对 GPU/CPU 的收益或安全保护效果。

## 产物

- 计划：[r6i25-large-scale-routing.json](../../configs/r6i25-large-scale-routing.json)、[r6i25-dynamic-admission.json](../../configs/r6i25-dynamic-admission.json)
- Overlay：[kv-aware-active](../../deploy/experiments/r6i25-large-scale/kv-aware-active/)、[load-balanced-active](../../deploy/experiments/r6i25-large-scale/load-balanced-active/)
- 路由 JSONL/metrics：`artifacts/bench/r6i25-final-{lb,kv}-routing-r{1,2}/`
- 路由 compare：[comparison.md](../../artifacts/bench/r6i25-final-routing-comparison/comparison.md)、[comparison.json](../../artifacts/bench/r6i25-final-routing-comparison/comparison.json)
- admission JSONL/metrics：`artifacts/bench/r6i25-final-{lb,kv}-admission-r1/`
- admission compare：[comparison.md](../../artifacts/bench/r6i25-final-admission-comparison/comparison.md)、[comparison.json](../../artifacts/bench/r6i25-final-admission-comparison/comparison.json)

## 后续建议

1. 保持 `576` 为 profile-scoped fixed candidate，不把它提升为全局默认；更换 model、tokenizer、GPU、vLLM 或 KV protocol 时重新校准。
2. 将 dynamic admission 的“只在 hard reject 下减小 target”作为下一轮设计评审点；若要真正双向动态并发，需要独立的 idle/recovery hysteresis，并继续保证已有 SSE 不被撤销。
3. 增加 Pod/GPU runtime exporter 和 Prometheus scrape，再重跑 runtime hard gate 与资源归因实验。
4. 增加 KV event loss / stale subscriber / backend unavailable fault injection，真正验证 `kv-aware -> load-balanced` 降级，而不仅是 valid steady-state。
