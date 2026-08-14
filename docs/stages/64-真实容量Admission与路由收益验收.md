# 阶段 64：真实容量、Admission 与路由收益验收

## 1. 交付

- 修正 benchmark 观测边界：load-balanced 不提供 prompt-token header 时可显式记录
  `prompt_token_missing` 而不伪造 token 证据；正式 token-sensitive plan 仍拒绝缺失证据。
- 修正 Gateway metrics sampler 的内部取消误报，并把“首个采样点尚无 completed counter”解释为零；
  malformed family 和 counter reset 仍会使窗口失效。
- 将路由消融的 warmup 从 `controlled-warm` 调整为适用于 load-balanced 的 `steady-warm`，并保留
  `available`、真实零命中和 unavailable 的区分。
- 新增 `r6i22-final` 实验 overlay，使用 Gateway 镜像
  `sha256:2baa1a168b1a2951b43ae88b4c3ecf7cd170af730cf9e4d18f6bce24a322316d`，完成 A0/A1/A2、B1/B2
  和长连接对照的真实 K3s/GPU 验收。

## 2. 真实结果

### A. 容量与动态 admission

| Run | 配置 | 请求 | 成功/失败 | Gateway 结论 |
| --- | --- | ---: | ---: | --- |
| `r6i22-a0-r3` | off / target 128 | 384 | 384/0 | 全局 accepted/completed 2.124 QPS；拒绝 0；offered 32 QPS 段最高 accepted 3.762 QPS |
| `r6i22-a1-r2` | off / target 32 | 544 | 544/0 | accepted/completed 4.053 QPS；平均 in-flight 15.165；拒绝 0 |
| `r6i22-a2-shadow-r1` | shadow / target 32 | 544 | 544/0 | accepted/completed 4.040 QPS；实际 target 不变；高负载期间 suggested target 曾为 24 |
| `r6i22-a2-active-r1` | active / target 32→128 | 544 | 544/0 | accepted/completed 4.077 QPS；平均 in-flight 15.057；12 次 target action；拒绝 0 |

A1 与 active 的单轮成对比较显示 TTFT P95 从 547.46 ms 降至 467.72 ms，变化为 `-14.57%`，
bootstrap 95% CI 为 `[-22.54%, -5.51%]`；completed QPS 仅从 4.053 增至 4.077，约 `+0.6%`。
因此本轮支持“尾延迟改善信号”，不支持“容量/QPS 明显提升”的结论；shadow 也有尾延迟改善，
所以不能把该单轮差异全部归因于 active 控制器。

长连接 drain 使用完全相同的 128 请求、32 并发、1024 max tokens 计划：

- target 128 / tuning off：`128/128` 成功，Gateway admission rejection 为 `0`，TTFT P95 `121.72 ms`；
- active：`112/128` 成功，`16` 个新请求被 `admission-capacity` 以 429 拒绝，TTFT P95 `126.39 ms`；
  被接纳的 112 个 SSE 流全部完成，没有观察到已有连接被撤销或中途断流。

这证明 target 下调只影响新 admission、不会撤销已有 permit；同时也暴露出当前 active 策略在长流
持续高占用时会把 target 从 32 降到 16，产生保护性背压。active 不应以当前参数直接替换默认 off。

### B. KV-aware 路由

`r6i22-b1-r2`（load-balanced）与 `r6i22-b2-r1`（KV-aware）均为 `288/288` 成功、拒绝 0。KV-aware
本身的 correctness/locality 证据成立：三种场景均 `available`；shared 平均约 1995 cached tokens，
mixed 平均约 1533 cached tokens，random 为真实的 0-token cache miss，而不是 unavailable。

短 2048-token profile 的单轮 paired compare 为：TTFT P95 `555.31 → 588.57 ms`，变化 `+5.99%`，
bootstrap 95% CI `[-24.20%, +29.45%]`；Gateway completed QPS `3.660 → 4.293`，平均 in-flight
`13.122 → 12.142`，Little's Law W `3585.47 → 2828.15 ms`。后面三项有改善，但实验只有单轮且
存在执行顺序/缓存状态影响，不能据此宣称短上下文 KV-aware 的稳定 TTFT 收益。KV-aware 应继续保留，
但收益结论限定为 cache locality/correctness；长上下文尾延迟收益仍需沿用已有 R6H-3 矩阵重复验证。

### C. Runtime hard gate

当前 Prometheus 只抓取 `fishmesh-gateway`，没有 vLLM Pod 维度的 CPU、内存或 DCGM/GPU runtime
sample。因而 B3 的 runtime freshness/hard-gate 收益在本集群不可判定；这不是“runtime gate 无效”，
而是观测前置条件尚未满足。未把节点级或缺失 Pod 归属的 GPU 数据冒充成 runtime 证据。

## 3. 最终结论与边界

- Little's Law 所需的 admitted、completed、rejected、in-flight 以及有效窗口已经被真实观测；
  它现在能支撑容量比较，但不会自动产生动态并发最优值。
- dynamic admission 已验证连接安全语义，并在短容量阶梯中显示尾延迟改善信号；当前 active 长流背压代价
  已实测，默认继续使用 `off`，后续需针对长流/短流分层控制或重新校准 target policy。
- KV-aware 的 available/zero-miss/degradation 语义和 prefix locality 已验证；短 profile 没有统计显著的
  TTFT P95 收益，不把 QPS/W 的单轮变化当成因果性能收益。
- runtime 感知升级暂不进入收益结论，先补齐 `$namespace+$pod` 作用域的容器/GPU exporter 与 Prometheus
  scrape，再重跑 B3。
- `session-key` 仍是 frozen compatibility mode，不进入维护和收益矩阵。

## 4. 恢复与验证

实验结束后已应用 `deploy/experiments/r6i22-final/load-balanced`：Gateway 为新镜像、
`load-balanced`、tuning `off`、initial target `128`；两个 vLLM Pod 均 Ready 且无本轮重启。
原始 JSONL、report 和 compare 产物保留在 `artifacts/bench/`，包括：

- `r6i22-a1-a2-compare/`
- `r6i22-b1-b2-compare/`
- `r6i22-drain-compare/`
- `r6i22-a0-r3/`、`r6i22-a1-r2/`、`r6i22-a2-shadow-r1/`、`r6i22-a2-active-r1/`
- `r6i22-b1-r2/`、`r6i22-b2-r1/`、`r6i22-drain-baseline-r1/`、`r6i22-drain-active-r1/`

本阶段仍需在补齐 Pod runtime metrics 后单独完成 B3；在此之前不把 runtime gate、active admission
或短上下文 KV-aware 宣称为已完成的普适性能升级。
