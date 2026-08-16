# R6I-25：大规模 KV-aware 与纯 load-balance 性能实验

本阶段完成了真实 GPU 集群上的大规模对照实验与动态行为取证。

## 交付

- 新增 8 场景路由矩阵：短/中/长/超长、shared/random/mixed、4/8/16/24 QPS，KV arm 默认启用 576-token short-context bypass。
- 两侧都启用 active admission、EndpointSlice discovery、Prometheus backend observation 和相同的 16–128 target bounds。
- 新增 low/medium/high/recovery admission 专项，保存 target/action/reason 原始时间序列。
- 生成 JSONL request-level evidence、Gateway metrics raw samples、compare JSON/Markdown 与中文多维度报告。

## 结论

- 路由矩阵两侧各 1536 请求全部成功；整体 TTFT P95 变化 `+9.89%`，CI 跨 0；收益集中在长 shared/mixed/xlong 场景，512-token bypass 相对纯 LB 有约 4–5% P95 代价。
- Little’s Law W 从 `1812.01 ms` 降到 `1615.09 ms`，但不能单独替代 TTFT P95 的 promotion 结论。
- KV 路径为 `960 available + 576 short-context-bypassed`，KV degradation/rejection 均为 0；valid phase 均确认 backend `ready`。
- Dynamic admission 在两侧均 `32 -> 128`、12 actions、无 rejection；本轮没有 hard rejection，因此没有触发 target decrease，recovery 继续增长而非回落。
- vLLM Pod runtime metrics 尚未具备，runtime hard gate 仍不可评估。

详细数据见 [`2026-08-16-r6i25-large-scale-kv-vs-lb.md`](../experiments/2026-08-16-r6i25-large-scale-kv-vs-lb.md)。实验结束后已恢复 `load-balanced + admission off` 基线。
