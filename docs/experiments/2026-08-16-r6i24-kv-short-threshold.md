# R6I-24：KV 短上下文阈值校准与固定策略

## 结论

当前阈值不应做成全局通用常量，也不应在每个请求到达时在线自适应。推荐采用：

> **按 model + hardware + vLLM profile 实验确定，运行期固定；shadow 只负责推荐重校准，不直接改写线上阈值。**

在当前参考 profile（Qwen2.5-0.5B-Instruct、vLLM 0.23.0、RTX 4060 8 GiB 双 time-sliced Pod）上，
`576` 是 512-token 短上下文的候选固定阈值：两轮共 80/80 请求进入
`short-context-bypassed`，而 1024/2048/3072 档均保留 KV-aware。它只应作为 profile overlay 候选，默认
仍保持 `FISHMESH_KV_AWARE_SHORT_PROMPT_TOKENS=0`，直到更多独立轮次通过正式性能门禁。

## 实验边界

| 项目 | 值 |
| --- | --- |
| Gateway image | `fishmesh-gateway:r6i24-admission-kv-bypass-r1@sha256:903c10b37dbc359946bff8dc592d3d6c96841e7f86afd4e35b607fda25c05553` |
| 模型 / vLLM | `qwen2.5-0.5b-instruct` / `0.23.0` |
| GPU | RTX 4060 8 GiB，双 time-sliced vLLM Pod |
| vLLM generation | `qwen-vllm-55bfbdd889`（所有有效 treatment 共用） |
| workload | `configs/r6i24-kv-short-threshold-sweep.json`，4 档 × 2 批 × 20 请求，共 160 请求/轮 |
| cache mode | `steady-warm`，每轮独立 run nonce；有效轮次均 160/160 成功 |

首次 baseline 因旧 generation 的 `sequence-gap-awaiting-replay` 无 KV validity，另一次阈值实验因 Gateway
rollout 后未重建 port-forward；两者均排除，不进入下表。

## 实际 token 与覆盖边界

阈值比较的是精确 tokenization 后的实际 token 数，不是场景名义值：

| 名义档位 | 实际 token 观测 | 名义阈值的结果 |
| ---: | ---: | --- |
| 512 | P50/P95 `513/513` | `512` 只覆盖部分请求，`576` 覆盖全部 |
| 1024 | `1030/1030` | `1024` 不覆盖该档 |
| 2048 | `2049/2053` | `2048` 只覆盖部分请求 |
| 3072 | `3078/3078` | `3072` 不覆盖该档 |

因此阈值必须是“校准上界 + guard band”，不能直接复用业务名义 token 档位。

## 512/1024/2048/3072 threshold sweep

以下为单轮阶梯结果；`bypass` 是四个场景共 160 请求中实际进入短上下文旁路的数量。

| 阈值 | bypass | TTFT P50/P95 (ms) | Gateway accepted/completed QPS | 说明 |
| ---: | ---: | ---: | ---: | --- |
| 0 | 0/160 | `224.66/853.99` | `11.623/11.623` | KV-aware baseline |
| 512 | 10/160 | `189.32/960.08` | `11.356/11.356` | 名义边界过窄 |
| 1024 | 40/160 | `158.80/842.17` | `12.597/12.597` | 只旁路 512 档 |
| 2048 | 90/160 | `140.61/930.17` | `11.912/11.912` | 512、1024 全旁路，2048 仍部分旁路 |
| 3072 | 120/160 | `184.21/794.18` | `12.069/12.069` | 2048 全旁路，3072 保留 KV |

单轮整体数字只能用于发现交叉点，不能作为 promotion 证据；单轮 bootstrap CI 均跨 0。

## 固定候选 576 的 paired repeat

对阈值 0 与 576 各执行两轮，均使用相同 vLLM generation：

| 指标 | threshold 0 | threshold 576 | 变化 |
| --- | ---: | ---: | ---: |
| 请求成功 | 320/320 | 320/320 | 无变化 |
| 整体 TTFT P95 | 940.87 ms | 839.14 ms | -10.81% |
| bootstrap 95% CI | — | — | `[-26.61%, +22.40%]` |
| accepted/completed QPS | 11.552/11.552 | 12.118/12.118 | 描述性上升 |
| Little's Law W | 550.98 ms | 521.26 ms | 描述性下降 |
| admission rejection | 0 | 0 | 无变化 |

按场景汇总两轮 P95：

| 场景 | threshold 0 平均 P95 | threshold 576 平均 P95 | 变化 | 旁路 |
| --- | ---: | ---: | ---: | ---: |
| 512 | 324.25 ms | 151.35 ms | -53.32% | 80/80 |
| 1024 | 353.04 ms | 377.50 ms | +6.93% | 0/80 |
| 2048 | 685.48 ms | 684.23 ms | -0.18% | 0/80 |
| 3072 | 1084.07 ms | 1092.06 ms | +0.74% | 0/80 |

512 档的收益与牺牲 locality 同时存在：threshold 0 有 80/80 个 available cache sample，576 有 0/80
个 cache sample；旁路不是“免费优化”。目前证据支持只旁路极短请求，不支持把 1024 或更长请求统一旁路。

## 决策与后续门禁

1. **策略**：profile-scoped fixed threshold。配置随 model/hardware/vLLM profile 版本化，运行期不因瞬时
   QPS、cache hit 或单次 TTFT 变化而改变。
2. **当前候选**：本 profile 使用 `576`，对应 overlay 为
   `deploy/experiments/r6i22-final/kv-aware-short-bypass-576`；它不是默认配置。
3. **默认状态**：实验结束恢复 `load-balanced`，`FISHMESH_KV_AWARE_SHORT_PROMPT_TOKENS` 缺省/关闭。
4. **shadow 角色**：只汇总 token 分布、bypass 比例、KV lookup/选择 P95、cache locality 和失败率，输出
   “建议阈值”；配置发布仍需人工/变更流程确认。
5. **重校准触发**：model、tokenizer、vLLM 版本、GPU profile、KV protocol、Gateway image 或 cache
   generation 行为变化时重新做 token calibration + paired A/B；不要把 576 外推到其他 profile。

## 产物

- 计划：[r6i24-kv-short-threshold-sweep.json](../../configs/r6i24-kv-short-threshold-sweep.json)
- 双轮 baseline/treatment compare：[0 vs 576](../../artifacts/bench/r6i24-threshold-0-vs-576-r2/comparison.md)
- 阈值阶梯 compare：[512](../../artifacts/bench/r6i24-threshold-compare-512-r1/comparison.md)、[1024](../../artifacts/bench/r6i24-threshold-compare-1024-r1/comparison.md)、[2048](../../artifacts/bench/r6i24-threshold-compare-2048-r1/comparison.md)、[3072](../../artifacts/bench/r6i24-threshold-compare-3072-r1/comparison.md)
- 固定候选单轮 compare：[576](../../artifacts/bench/r6i24-threshold-compare-576-r1/comparison.md)
