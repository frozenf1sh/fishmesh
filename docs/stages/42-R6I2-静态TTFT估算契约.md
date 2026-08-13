# 阶段 42：R6I-2 静态 TTFT 估算契约

## 1. 范围

本阶段建立 calibrated-static estimator 的纯值契约，不接入实际 routing，不增加网络 I/O、样本状态或
集群配置。实现位于 `prediction` domain，与现有 learned-shadow tracker 并列；requestpath 尚未消费其结果。

## 2. Profile

Profile 以 `prompt tokens × cached-prefix ratio` 的二维网格保存 prefill duration，并包含：

- model、hardware profile、vLLM version；
- 最小 prompt token 与 `max-model-len`；
- profile version 与 `calibrated` 标志；
- queue、running、local fallback 的毫秒系数和 safety margin。

构造期拒绝空版本、身份缺失、断裂的 token/ratio 范围、负系数、非矩形网格，以及“prompt 增长但
prefill 下降”或“cache ratio 增长但 prefill 上升”的非单调数据。

## 3. Estimate

估算使用二维线性插值：

```text
TTFT = interpolated prefill + load wait + safety margin
```

外部 load 完整时使用 queue/running；未知时只使用 local in-flight fallback，并把 confidence 降为
`degraded`。未校准 profile 可以产生实验 estimate，但只能标记 `uncalibrated`，不能伪装成 calibrated。
身份不匹配、prompt 越界、非法输入和 duration overflow 都返回 typed invalid estimate。

## 4. 验证

- prompt/cache 二维插值；
- cache 全命中边界；
- load unknown 的 local fallback；
- uncalibrated 置信度；
- model/hardware/vLLM/range mismatch；
- 非单调网格和 overflow 拒绝。

本阶段只运行本地门禁，不访问 GPU 集群。

## 5. 下一阶段

R6I-3 在 routing snapshot 中增加稳定 latency estimate，由 requestpath 投影 static estimator 结果；只有
全部候选 estimate 有效且 calibrated 时才使用 `kv-aware-ttft-static-v1`，否则回到现有 token-cost contract。
