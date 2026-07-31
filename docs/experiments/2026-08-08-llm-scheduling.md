# FishMesh 探索性实验报告（2026-08-08）

> 证据等级：探索性。该批次用于修正开发方向，不是最终性能 benchmark。它使用 vLLM
> `0.11.0` 和单张 RTX 4060 time-slicing 的两个进程；新版重复实验遵循
> [`docs/experiments/plan.md`](plan.md)。

## 1. 实验目的

本轮实验验证四个问题：

1. TCP/HTTP Keepalive 的收益是否已经足够大；
2. Prefix Affinity 是否只在热点前缀下有效；
3. 简单的 Gateway In-flight Load-aware 策略是否能改善混合负载尾延迟；
4. 静态 Pod endpoint、GPU 故障和后端饱和时，路由策略如何降级。

实验运行在 K3s 双节点集群：OrbStack 中的 ARM64 控制平面和 Ubuntu RTX 4060 节点，vLLM 使用两个 Qwen2.5-0.5B-Instruct 副本，Gateway/Loadgen 使用同一个本地镜像版本。所有有效请求均消费完整 SSE 流后才计为成功。

## 2. 条件和工作负载

| 工作负载 | 请求数 | 并发 | prefix groups | 热点比例 | 最大输出 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 原始矩阵 | 200 | 4 | 8 | 0% | 32 token |
| Hot prefix | 200 | 4 | 4 | 75% group 0 | 32 token |
| Mixed skew | 200 | 8 | 8 | 50% group 0 | 32 token |
| Saturation probe | 100 | 8 | 8 | 0% | 32 token |

Hot prefix 分布由 Loadgen 的确定性 `--hot-prefix-ratio` 生成，不使用不可复现的随机数。Direct endpoint 和静态 endpoint 仅用于实验，不是正式发现机制。

## 3. 结果

### 3.1 已有连接矩阵

| 模式 | TTFT P50 | TTFT P95 | Warm P50 | 成功率 |
| --- | ---: | ---: | ---: | ---: |
| Service + no keep-alive | 84.32 ms | 124.36 ms | 83.25 ms | 200/200 |
| Service + keep-alive | 48.11 ms | 93.08 ms | 47.42 ms | 200/200 |
| Prefix-hash + keep-alive | 51.92 ms | 79.83 ms | 51.34 ms | 200/200 |

结论：相比 no keep-alive，Generic Keepalive 将 P50 降低约 43%，是当前最确定的收益。Prefix-hash 的 P95 低约 14%，但 P50 和 Warm P50 略差，不能据此宣称 KV Cache 命中提升。

### 3.2 Hot prefix（75% 请求共享 group 0）

| 模式 | TTFT P50 | TTFT P95 | TTFT P99 | 成功率 |
| --- | ---: | ---: | ---: | ---: |
| Service + keep-alive | 50.53 ms | 88.41 ms | 249.99 ms | 200/200 |
| Prefix-hash + keep-alive（rerun） | **42.68 ms** | **62.64 ms** | **83.43 ms** | 200/200 |
| In-flight load-aware | 52.51 ms | 65.94 ms | 89.01 ms | 200/200 |

第一次 hot-prefix prefix-hash attempt 为 196/200 成功，P50/P95/P99 分别为
43.96/69.11/95.28ms；上表是随后相同参数的 200/200 rerun。两次数据都必须保留，不能只因
rerun 更好就将其当作唯一结果。它们提示 affinity 在该合成热点分布中可能有价值，但需要按
新实验方案进行多轮随机化复验。

### 3.3 Mixed skew（50% 请求共享 group 0）

| 模式 | TTFT P50 | TTFT P95 | TTFT P99 | 成功率 |
| --- | ---: | ---: | ---: | ---: |
| Service + keep-alive | 48.44 ms | 104.16 ms | 281.96 ms | 200/200 |
| Prefix-hash + keep-alive | **46.10 ms** | 105.17 ms | **584.41 ms** | 200/200 |
| In-flight load-aware | 49.78 ms | **94.70 ms** | **140.17 ms** | 200/200 |

纯 Prefix-hash 在混合负载下没有改善 P95，P99 反而显著变差；In-flight load-aware 的 P95/P99 更稳定，但 P50 略差。说明生产策略不能只做 `hash(prefix) -> pod`，必须把局部性和当前负载共同纳入决策。

### 3.4 饱和与故障

对单个 endpoint 施加直接压力后进行 Gateway probe：

| 模式 | TTFT P50 | TTFT P95 | 成功率 | 观察 |
| --- | ---: | ---: | ---: | --- |
| Service probe | 56.45 ms | 233.74 ms | 100/100 | Service 仍能完成请求，但尾延迟升高 |
| In-flight load-aware probe | 58.18 ms | 220.47 ms | 94/100 | 没有外部 GPU/队列信号，仍可能选中饱和 endpoint |

这组不是严格的 A/B 性能结论，因为两次压力窗口和 endpoint 状态不完全相同；它的工程价值是证明“Gateway 自己的 in-flight 计数不等于 vLLM/GPU 负载观测”，真正的 load-aware 必须接入 vLLM metrics、队列和 GPU 信号。

删除一个 vLLM Pod 后，静态 endpoint 列表仍保留旧 IP，50 个请求中有 2 个返回 502。随后新 Pod 暴露出 Ubuntu 主机缺少 amd64 NVML 用户态库的问题；补装 `nvidia-utils-610`、`libnvidia-compute-610`，重新生成 CDI 并重启 device plugin 后恢复。这个事件同时证明：

- 固定 Pod IP 不能作为正式路由发现机制；
- endpoint 路由必须有 Ready 状态、健康检查、超时和 Service fallback；
- GPU 节点的驱动、DKMS、NVML、CDI 和 device plugin 是一个完整运维单元。

## 4. 结果边界

本轮数据足以做方向决策，但不能声称已经证明 vLLM Prefix Cache 命中率提升，原因是：

- 当前 Gateway 的 prefix hash 只是实验 `prefix_group/routing_key`，不是 vLLM 内部 token-block cache key；
- 本轮 artifact 没有对每个请求关联 vLLM Prefix Cache hit；
- 每个条件的重复次数和模型规模仍有限；
- Prefix-hash 使用的是 Pod IP 快照，故障恢复实验已证明其工程风险。
- 两个 vLLM replica 共享同一张 time-sliced GPU，不是独立容量或故障域；
- Service keep-alive 产生连接级 endpoint 粘性，可能包含偶然 locality；
- 部分表格来自单次或选定 rerun，尚无跨轮区间。

后续应优先补采 vLLM `/metrics` 中的 Prefix Cache、队列、running requests 和 TTFT 指标，而不是继续堆叠更多 hash 变体。

## 5. 最终实验结论

1. **Generic Keepalive 是默认基线和必须保留的优化。**
2. **Prefix Affinity 是条件策略，不是所有负载下的默认策略。** 热点前缀明显受益，混合/热点不均时可能制造 tail latency。
3. **简单 In-flight Load-aware 有研究价值，但还不是 GPU-aware Scheduler。** 它在混合负载中改善了尾延迟，却无法感知外部压力和 vLLM 队列。
4. **下一步应验证 bounded affinity：硬 eligibility、局部性优先和过载 spillover，而不是直接实现多指标加权 score。**
5. **GPU、累计 TTFT/Prefix Cache 和网络指标先作为慢速证据，不宣称 per-backend 快路径信号。**
