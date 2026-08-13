# 阶段 43：R6I-3 静态 TTFT 路由接入

## 1. 范围

本阶段把 R6I-2 的纯 estimator 接入 standalone requestpath/routing，但默认部署仍保持 `token-cost`。
配置只有在显式选择 `static-ttft`、提供 profile 文件且 profile 标记 calibrated 时才能启动 active static。

## 2. 请求链路

```text
tokenize → KV lookup → load/local snapshot → HardOverload filter
→ requestpath static estimate projection → routing minimum TTFT → lease
```

- `prediction` 不 import routing，也不持有 routing 状态；
- requestpath 把 prompt/cached/load 数值投影成 `routing.LatencyEstimate`；
- routing 只比较稳定 duration，平局按 backend ID；
- circuit 与 HardOverload 始终先于 static estimate；
- load unknown 产生 degraded/local-fallback estimate，仍可比较；
- 任一候选 estimate 缺失、无效或 uncalibrated 时，整次选择回到 token-cost，使用
  `kv-aware-static-fallback` reason；不会把部分 estimate 与 token cost 混排。

完整 static 选择的 policy/reason 为：

```text
kv-aware-ttft-static-v1 / kv-aware-static-ttft
```

## 3. 配置

- `FISHMESH_KV_AWARE_ESTIMATOR_MODE=token-cost|static-ttft`；
- `FISHMESH_KV_AWARE_STATIC_PROFILE_FILE=<json path>`；
- profile 文件上限 1 MiB、拒绝未知 JSON 字段；
- active static 要求 calibrated=true，且 profile model 与 tokenizer model 一致。

基础和当前 Lite manifest 继续使用 `token-cost`，因此本阶段没有 rollout 或 GPU 行为变化。

## 4. 验证

- static estimate 可以在证据充分时覆盖 token-cost；
- degraded load fallback 可参与 static 比较；
- 部分/未校准 estimate 原子回退 token-cost；
- requestpath 正确投影 cached prefix、外部 load 去重与 local fallback；
- profile JSON、model、calibrated 和缺失文件在启动期校验。

## 5. 下一阶段

R6I-4 为选择结果增加低基数 estimator 指标与请求级 JSONL provenance，使 static/fallback、估算值和实际
TTFT 可以在实验报告中配对，不暴露 prompt 或 prefix identity。
