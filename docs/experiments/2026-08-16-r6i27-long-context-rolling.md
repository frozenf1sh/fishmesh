# R6I-27：双层滚动重启与随机输入长上下文复验

## 结论先行

本轮专门复验“已有 vLLM KV cache 或 Gateway KV index 干扰”这一风险。每个正式 arm 都执行了
vLLM 与 Gateway 的滚动重启；vLLM 使用 `maxSurge=0/maxUnavailable=1`，过程中最多保持 2 个 Pod，
避免同一张 time-sliced GPU 同时运行第三个 vLLM Pod。Gateway 使用每个 arm 唯一的 runtime annotation，
因此本地 hash/index、subscriber 和 admission runtime state 也被清空。

- 两个 replicate、四个 arm，共 `384/384` 请求成功；每个 arm 96 请求。
- 纯 load-balanced pooled TTFT P95 为 `189.81 ms`，KV-aware 为 `191.35 ms`，变化 `+0.81%`，
  bootstrap 95% CI 为 `[-15.45%, +38.22%]`，跨过 0。
- 在“vLLM cache + Gateway KV index 均按 arm 重启”的条件下，仍不能证明 KV-aware 在低并发长上下文下有整体收益；
  本轮结果与 R6I-26 的总体方向一致，但差异已经缩小到统计不显著范围。
- KV arm `192/192` 请求为 `available`，cached-prefix samples `192/192`，累计 cached prefix tokens
  `316,608`；LB 的 `not-requested` 不解释为 vLLM cache miss，因为纯 LB 不执行 KV lookup。

## 实验设计

| 项目 | 值 |
| --- | ---: |
| 长度 | 3072 / 3584 target prompt tokens |
| 前缀模式 | shared / mixed / random |
| 场景 | 6 |
| 每场景 | 2 batch × 8 requests = 16 |
| 每 arm | 96 requests |
| replicate | 2；seed `20260817` / `20260818` |
| 客户端并发 | 4 |
| offered QPS | 1 / 2 / 4 |
| Gateway | active admission，target 16，边界 8–64；max in-flight 64；connections 16 |
| vLLM | Qwen2.5-0.5B，vLLM 0.23.0，GPU utilization 0.40，max-model-len 4096 |
| cache mode | steady-warm；每个 arm 独立 run nonce 与 vLLM generation |

同一 replicate 的 LB/KV 使用相同 `workload_seed`，因此输入序列完全一致；不同 replicate 使用不同 seed。
`randomize_request_order=true` 同时打乱场景执行顺序和场景内逻辑输入顺序，JSONL 的 `input_sequence`
记录了逻辑输入编号，不记录 prompt 内容。

## 缓存隔离证据

四个正式 arm 的 vLLM ReplicaSet generation 分别为：

| Arm | vLLM generation | Gateway runtime annotation |
| --- | --- | --- |
| r1-LB | `qwen-vllm-76b7478669` | `r6i27-r1-lb` |
| r1-KV | `qwen-vllm-c9f54899` | `r6i27-r1-kv` |
| r2-KV | `qwen-vllm-786b6564cb` | `r6i27-r2-kv` |
| r2-LB | `qwen-vllm-5bc998f9f8` | `r6i27-r2-lb` |

每个 arm 都先等待 vLLM 两 Pod Ready；KV arm 额外等待两个 backend 的 KV replay
`valid_backends=2`。因此本轮排除了：

1. 上一个 arm 在 GPU 上留下的可复用 prefix cache；
2. 上一个 arm 在 Gateway 进程内留下的 KV hash/index；
3. 上一个 arm 留下的 subscriber、local in-flight 和 dynamic admission 状态。

纯 LB arm 没有 KV lookup header，这是设计语义，不是缓存 miss 证据；要测 LB 的真实 vLLM prefix hit，
仍需直接采集 vLLM `/metrics`。

## Pooled 结果

| 指标 | 纯 load-balanced | KV-aware | 变化 |
| --- | ---: | ---: | ---: |
| 请求成功 | 192/192 | 192/192 | 0 |
| TTFT P50 | 143.59 ms | 147.10 ms | +2.44% |
| TTFT P95 | 189.81 ms | 191.35 ms | +0.81% |
| TTFT P99 | 534.55 ms | 578.97 ms | +8.31% |
| run median P95 | 176.74 ms | 190.69 ms | +7.90% |
| accepted QPS | 1.720 | 1.715 | -0.29% |
| average in-flight | 0.319 | 0.331 | +3.76% |
| Little's Law W | 185.76 ms | 192.72 ms | +3.75% |
| admission rejection QPS | 0 | 0 | — |

## 分场景结果

下表是两轮 run-level P50/P95 的平均值；每个场景每 arm 32 请求，只用于分层观察。

| 场景 | LB P50/P95 ms | KV P50/P95 ms | P95 变化 | KV cached tokens |
| --- | ---: | ---: | ---: | ---: |
| shared-3072 | 38.57 / 144.21 | 50.71 / 87.69 | -39.20% | 91,392 |
| mixed-3072 | 57.55 / 255.48 | 73.54 / 254.71 | -0.30% | 54,720 |
| random-3072 | 149.03 / 159.33 | 159.23 / 162.08 | +1.73% | 0 |
| shared-3584 | 40.68 / 55.94 | 51.39 / 83.52 | +49.30% | 106,560 |
| mixed-3584 | 56.01 / 351.72 | 60.54 / 380.81 | +8.27% | 63,936 |
| random-3584 | 168.25 / 184.24 | 181.02 / 188.70 | +2.42% | 0 |

3072 shared 仍有明显尾延迟收益，但 3584 shared 反向变差；低并发下收益对长度、调度和尾部样本敏感，
不能提升为全局 promotion。

## 内存与 admission 边界

本轮仍只采集 Gateway process RSS/Go heap，不把它误写成 hash-only bytes。两轮平均的峰值起点差异约为：

- KV-aware 相对 LB：RSS peak-start 约 `+0.59 MiB`、Go heap peak-start 约 `+0.85 MiB`；
- KV-aware 的 absolute peak 相对 LB 约 `+21.49 MiB RSS`、`+18.33 MiB Go heap`。

每个 arm 的 Gateway metrics window 都有效，accepted/completed QPS 一致，rejection 为 0。当前 benchmark
报告没有保存 admission target/reason 的完整时间序列，因此本轮只确认无拒绝，不能宣称动态 target 的完整收敛轨迹。

## 产物

- 计划：[r6i27-long-context-rolling.json](../../configs/r6i27-long-context-rolling.json)
- 自动流程：[run-r6i27-long-context-rolling.sh](../../scripts/run-r6i27-long-context-rolling.sh)
- Overlays：[LB](../../deploy/experiments/r6i27-long-context-rolling/load-balanced-active/)、[KV](../../deploy/experiments/r6i27-long-context-rolling/kv-aware-active/)
- 有效产物：`artifacts/bench/r6i27-long-context-rolling-v2/`
- 对比：[comparison.md](../../artifacts/bench/r6i27-long-context-rolling-v2/comparison.md)

首轮尝试因 LB 不提供 prompt-token header 而触发了过严的 token evidence 门禁；该目录
`artifacts/bench/r6i27-long-context-rolling/` 未纳入本报告。随后计划改为显式
`skip_prompt_token_evidence=true`，不影响 route/KV/延迟/metrics evidence。

实验结束后已恢复 `load-balanced`、admission `off`、max in-flight `128`、connections `32`、
vLLM GPU utilization `0.35`、max-model-len `4096`。
