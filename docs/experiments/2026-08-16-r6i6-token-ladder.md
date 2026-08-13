# R6I-6：Static TTFT 校准与 Token 阶梯实验

## 1. 结论

本轮完成 512/1024/2048/3072 prompt token 的 cold/controlled-warm 校准，并在并发
1/4/8/16 下比较 `token-cost` 与 calibrated-static。static 在低负载校验中的 estimator MAE 为
2.34–5.44 ms，但在 2048-token 长生成并发阶梯中的 MAE 上升到 27.57 ms、absolute error P95 为
71.54 ms；相对 token-cost 的整体 TTFT P95 变化为 +3.13%，bootstrap 95% CI 为
[-3.98%, +9.38%]。

因此本轮 **不通过 active/default promotion 门禁**。参考集群已恢复 `token-cost`；static profile 和
overlay 作为可复现实验交付物保留，R6I-7 只做 learned-shadow，不增加在线决策复杂度。

## 2. 环境与边界

| 项目 | 值 |
| --- | --- |
| 日期 | 2026-08-16 |
| 模型 | `qwen2.5-0.5b-instruct` |
| vLLM | `0.23.0` |
| GPU profile | RTX 4060 8 GiB，两个 time-sliced vLLM Pod |
| Gateway image | `fishmesh-gateway:r6i6-static-ttft-r4`（与正式提交 Go source 构建；manifest 与 r3 相同） |
| image manifest digest | `sha256:e7962f0c9ea7cabc200b9b2dc78d47901d53462a7241d6238227b1b3d688ac24` |
| profile version | `r6i6-rtx4060-vllm0.23.0-v1` |
| profile SHA-256 | `b94a70a5d957f3b79a53c760fe3508dae1b97fed21292b688b5ae1be24094174` |
| cache protocol | cold / controlled-warm，treatment 间独立 nonce/generation |

两个 vLLM Pod 共享一张 time-sliced GPU，不能视为独立硬件副本；每个 treatment 只有一轮有效正式结果，故本报告
用于校准和 promotion gate，不作为跨硬件或最终五轮性能结论。raw JSONL 保留在本地
`artifacts/bench/r6i6-*`，不跟踪进 Git。

最终 `r4` token-cost rollout 后，Gateway 与两个 vLLM Pod 都 Ready，真实请求返回 200，load observation
也为 `ok`；但新 Gateway 对两 backend 的 replay 分别停在 `sequence-gap-awaiting-replay` 和
`unrecoverable-sequence-gap`，请求因此正确返回 `kv-aware-load-fallback-v1`。为避免用 vLLM 重启扰动健康 GPU，
本轮没有强制清空 cache generation；该 rollout 不计作“KV replay 已恢复”的验收证据，后续需单独修复或在受控
cache generation 中复验。

## 3. 实际 Token 校准

首次 Render 得到 501/1002/2002/3002 token；据此调整生成 byte 起点后，正式模板收敛为：

| 目标 token | `prefix_bytes` | 实际 token | tolerance |
| ---: | ---: | ---: | ---: |
| 512 | 2866 | 512 | ±16 |
| 1024 | 5932 | 1025 | ±16 |
| 2048 | 12076 | 2048 | ±16 |
| 3072 | 18220 | 3072 | ±16 |

结果来自 Gateway 返回的 `X-FishMesh-Prompt-Tokens`，不是把字节数当作 token。两组校准各 40/40 正式
请求成功，所有 token evidence 完整且在容差内。

## 4. Cold 与 Controlled-warm Profile

| prompt token | cold TTFT P50 / P95 (ms) | controlled-warm TTFT P50 / P95 (ms) |
| ---: | ---: | ---: |
| 512 | 32.13 / 36.24 | 20.62 / 21.08 |
| 1024（实际 1025） | 54.72 / 56.14 | 23.13 / 24.53 |
| 2048 | 88.17 / 89.71 | 26.38 / 28.00 |
| 3072 | 135.67 / 136.20 | 30.10 / 30.44 |
| overall | 58.73 / 136.02 | 25.32 / 30.44 |

Profile 使用上述 P50 作为 0%/100% cached prefill 网格，并只允许在实测边界内插值：prompt
512–3072、queue 0、running 0–8、local delta 0–11。未观测到 queue，因此将 `max_queue_depth=0`，任何
queue > 0 都 typed fallback 到 token-cost，而不是杜撰 queue 系数或向外推。

## 5. 低负载 Static 校验

| cache 状态 | 请求 | TTFT P50 / P95 (ms) | estimator MAE / abs P95 (ms) | 相对 token-cost P95 |
| --- | ---: | ---: | ---: | ---: |
| cold | 40/40 | 55.88 / 136.63 | 2.34 / 4.37 | +0.45% |
| controlled-warm | 40/40 | 25.18 / 31.87 | 5.44 / 8.69 | +4.72% |

cold P95 bootstrap CI 为 [-2.94%, +1.91%]；warm 为 [-13.57%, +16.81%]。低负载模型可解释且误差小，
但性能置信区间都跨过 0，不能声称确定提升。34-token smoke 低于校准下界，正确返回
`kv-aware-static-fallback`。

## 6. 2048-token 并发阶梯

为避免模型提前输出 EOS，阶梯计划固定 `max_tokens=256`、`ignore_eos=true`，并使用
controlled-warm。最终公平对照均包含 local-delta 和原子 selection/reservation 修复。

| 并发 | token-cost TTFT P50 / P95 (ms) | static TTFT P50 / P95 (ms) | P95 变化 |
| ---: | ---: | ---: | ---: |
| 1 | 27.66 / 29.86 | 27.02 / 27.84 | -6.75% |
| 4 | 47.65 / 55.68 | 47.17 / 53.93 | -3.14% |
| 8 | 73.25 / 75.53 | 72.33 / 79.92 | +5.81% |
| 16 | 118.41 / 129.15 | 100.11 / 133.57 | +3.42% |
| overall | 75.32 / 128.62 | 77.35 / 132.64 | +3.13% |

两组均为 120/120 请求成功。static 的 duration P50/P95 为 1786.81/3377.00 ms，token-cost 为
1836.90/3262.52 ms。static 在 c16 的 64 个请求中有 47 次使用 static、17 次因超出 load bounds 回退
token-cost；本轮没有 queue，HardOverload 计数为 0。整体 P95 变化的 bootstrap 95% CI 为
[-3.98%, +9.38%]，没有稳定优势。

一次 rollout 后立即启动的 token-cost 尝试虽然 120 个请求成功，但有 2 条 prompt-token evidence 缺失；
该 run 按预注册验收规则判无效并排除，稳定后重跑的 r3 才作为上表 baseline。这个失败样本没有被平均值掩盖。

## 7. 实验驱动的设计修正

并发阶梯暴露了两个比调权重更重要的问题：

1. vLLM `running` 是采样值，Gateway 刚接收但尚未出现在样本中的请求仍会形成盲区。现在定义
   `local_delta=max(local_inflight-sampled_running, 0)`，外部 load 有效时只补这个差量；load 未知时才使用
   完整 local in-flight，避免重复计费。
2. 并发请求原先可能在 reservation 前同时读取相同 local in-flight。现在 tokenization/KV lookup 保持并发，
   最终 snapshot → select → counter reservation 在短临界区原子提交，避免 batch thundering herd。

这些修正改变的是事实一致性，不依赖本轮拟合出的系数。static profile 另外增加 load bounds，超出校准包络时
返回 `out-of-range` 并整次回退 token-cost。

## 8. Promotion 决策

- 保持 `token-cost` 为当前和默认 estimator；
- 保留 immutable static profile、显式 overlay、profile SHA 和 image digest，便于面试复现；
- 不采用在线动态权重。下一步若继续，只在 R6I-7 用相同逐请求证据做 learned-shadow，并要求误差稳定优于
  static 后才讨论 active；
- 后续长前缀扩展优先在容量允许时加入 4096/8192/12288 token 与 25/50/75% cache ratio，而不是增加组件。
