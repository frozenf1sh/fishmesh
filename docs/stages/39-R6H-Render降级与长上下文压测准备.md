# 阶段 39：R6H Render 降级与长上下文压测准备

## 1. 范围

本阶段处理高并发下的 503 根因，并为下一轮更接近真实业务的压测准备受控 profile。目标不是继续
无上限增加连接数，而是在有限连接下测试更长 prompt 对 TTFT、尾延迟、KV locality 和 GPU 利用率的影响。

## 2. 根因与修复

此前 KV-aware 请求在选路前需要调用 vLLM Render。Render 使用 Kubernetes Service FQDN；在冷连接突发时，
客户端受 `ndots:5` 影响反复尝试 search suffix，偶发超过 5 秒 tokenization timeout。这个错误被 Gateway
表现为 503 `request path unavailable`，所以 Prometheus 看不到 admission rejection 或 upstream error。

本阶段的修复包括：

- Render 失败按 typed error 区分：临时不可用、超时、异常响应允许本次请求降级到 load-balanced；请求体形状错误和调用方取消仍然硬失败；
- Render 使用独立的 16 连接有界池，不与数据面连接池混用；
- DNS 结果缓存 30 秒，并对并发首次解析做 singleflight；
- 对完整集群 FQDN 强制绝对 DNS 查询，避免 `ndots:5` 触发无效 search suffix 尝试；
- 建连失败会失效当前缓存地址，下一次请求重新解析。

## 3. 长上下文 profile

`configs/long-context-balanced.json` 使用 4 KiB 和 12 KiB prompt 前缀、多个批次和同前缀/不同前缀/混合前缀。
Gateway profile 使用 64 个 in-flight 上限和 16 个 upstream 数据连接。当前 vLLM `max-model-len` 仍是 4096；
这里的 12 KiB 是生成器的 prompt 字节长度，不是 12K token，因此没有未经测量就扩大模型 KV cache。

同时提供 `deploy/experiments/long-context-load-balanced`，它与 KV-aware profile 使用相同连接数、in-flight
上限和压测计划，只改变路由策略，供下一轮 A/B 使用。

## 4. 真实集群验证

参考集群部署 `long-context-kv-aware` 后完成 288/288 请求，0 失败：

| 指标 | 结果 |
| --- | ---: |
| TTFT P50 / P95 | 85.17 / 479.83 ms |
| 总耗时 P50 / P95 | 234.25 / 843.63 ms |
| KV available | 288 / 288 |
| Gateway admission rejection | 0 |
| Gateway upstream error | 0 |
| GPU 峰值 | 100%，约 7.26 / 7.84 GiB，59°C |

同一计划中，12 KiB 同前缀 TTFT P50 为 47.59 ms，12 KiB 不同前缀为 338.25 ms，12 KiB 混合前缀为 111.60 ms。
这说明长上下文本身会放大缓存命中差异，但当前机器的显存余量只有约 574 MiB，不能据此直接把
`max-model-len` 调到更高。

## 5. 真实 Mixed 修正与反向顺序 A/B

上一轮名义上的 mixed 场景每场只有 48 个请求，而旧生成器按 `request % 100` 编排比例，因此实际请求全部
落在 `shared-0` 热前缀上，不能代表真正的混合流量。本轮将生成器修正为按整个场景的请求总数计算比例，
并将两个 mixed 场景改为 2 批 × 50 请求。从原始 `requests.jsonl` 核对每场 100 个请求的实际分布：60 个
`shared-0` 热点前缀、20 个 `unique-*` 独立前缀、20 个其他共享前缀，其中 `shared-1/2/3` 分别为 7/6/7。

2026-08-16 使用修正后的同一份计划完成两轮反向顺序 A/B：第一轮为 `load-balanced → KV-aware`，第二轮为
`KV-aware → load-balanced`。四个正式 run 共 1568 请求，全部成功；报告和机器可读汇总位于
`artifacts/bench/long-context-mixed-comparison-r6h-r2/`。

| 指标 | load-balanced | KV-aware | KV-aware 相对变化 |
| --- | ---: | ---: | ---: |
| TTFT P50 | 48.40 ms | 50.65 ms | +4.6% |
| TTFT P95 | 431.42 ms | 100.32 ms | -76.7% |
| 总耗时 P50 | 300.35 ms | 215.39 ms | -28.3% |
| 总耗时 P95 | 967.00 ms | 435.53 ms | -55.0% |

真实 mixed 流量下，KV-aware 的收益主要体现在尾延迟，而不是所有请求的首包都更快：mixed-4k 的 TTFT P95
从 187.03 ms 降至 70.49 ms（-62.3%），mixed-12k 从 678.71 ms 降至 106.37 ms（-84.3%），但两者
P50 分别变化 +8.2% 和 +1.6%。两种模式均无 admission rejection 或 upstream error；KV-aware 的 784 个
请求全部 `available`。GPU SM 和 memory-controller 峰值均为 100%，显存为 7262/8188 MiB，温度峰值 65°C。

切换对照 overlay 时曾发现 `service` 不是合法 discovery mode，导致一个新 Gateway Pod 启动失败；该错误在
正式请求前修正为 `endpointslice`，四次正式 run 均在健康 rollout 后执行，失败 Pod 不计入报告。

## 6. 下一阶段边界

真实 mixed A/B 已完成。下一轮按并发 1/4/8/16 做阶梯压测，并增加更长 prompt 的分档对照。每一档同时观察
成功率、503 分类、TTFT P50/P95/P99、GPU 利用率/显存、vLLM queue/running、KV available/degradation 和
Gateway RSS；只在无错误且显存余量稳定时继续提高上下文或并发。

正式报告完成后的集群恢复过程中，GPU 节点经历了一次短暂 NotReady，vLLM 0.23 重新估算 CUDA graph 后在
`gpu-memory-utilization=0.35` 下报告没有可用 KV cache。为给下一轮压测保留足够启动余量，两个长上下文实验
overlay 已统一调整为 `0.40`；这只影响后续运行时配置，不改变本阶段已完成的 A/B 数据。
