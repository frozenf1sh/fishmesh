# 阶段 47：R6I-7 Learned-shadow 实现与门禁

## 1. 范围

本阶段把已有的本地非负 ridge 预测器推进到可审计的 learned-shadow：它只收集已选 backend
的首个 SSE 事件，不改变本次请求的实际选择，也不向正式路由引入随机探索或在线 active 权重。

## 2. 实现

- prediction 模型按 backend 保留有界样本，过期样本会被清理，未知 load、无首事件和取消请求不形成伪样本；
- 模型不再每次请求重拟合，而是在达到固定 `RefitEvery` 完成样本数后重拟合；
- ridge 系数保持非负，并按特征量纲设置上界，避免小样本异常把 TTFT 估计放大；
- Gateway 增加固定低基数 shadow response headers：status、model、would-select、selected/would-select
  TTFT、样本数；`fishmesh-client` 将这些字段原样收入无 prompt 的 JSONL；
- `fishmesh-client compare` 同时汇总 static estimator 与 learned-shadow 的 MAE、P95 absolute error、
  would-select 与当前实际选择的一致率，以及同一请求上的 paired error 差值；
- 新增 `deploy/experiments/r6i7-learned-shadow`，继承 static profile，只增加
  `FISHMESH_PREDICTION_MODE=shadow`，可通过恢复 `r6i6-calibration` 立即回退。

## 3. 预注册实验与门禁

使用 RTX 4060 8 GiB、两个 time-sliced vLLM 0.23.0、Qwen2.5-0.5B、4096 max model length；每个独立
run 使用 512/1024/2048/3072 actual prompt tokens，same/different/mixed prefix，4 个场景各 2 批、
每批 20 个请求、并发 8，共 160 个正式请求。两轮使用不同 run nonce，保留 plan、每请求 JSONL 和 report。

进入质量比较前必须满足：

1. 两个 vLLM Pod、Gateway 和两个 EndpointSlice backend Ready；正式请求成功率至少 98%；
2. KV status 为 `available` 的请求至少占成功请求 95%，prompt token 缺失/越界为 0；
3. 每个 backend 每个独立 run 至少 32 个可用 learned-shadow 样本；
4. learned MAE 至少比 static MAE 低 10%，learned absolute-error P95 不高于 static，且两项在两轮都成立；
5. learned would-select 与当前实际 static 选择的一致率至少 70%。

任一前置条件或门禁失败，都只能得出 shadow 研究结论，不能升级为 active。即使本阶段质量门禁通过，
R6I-6 已经显示 static 在并发 TTFT P95 未通过 promotion，因此仍需单独的 active ADR 和性能门禁，
本阶段不直接打开 active。

## 4. 验证

本阶段代码门禁：`go test ./...`、`go vet ./...`、`go build ./...`、R6I-7 Kustomize server dry-run
和 `git diff --check`。真实实验结果写入
`docs/experiments/2026-08-16-r6i7-learned-shadow.md`，并根据门禁结果更新当前运行状态。

## 5. 回滚

shadow 只影响样本和低基数观测；恢复 `deploy/experiments/r6i6-calibration` 即可关闭 prediction 并回到
token-cost。任何 KV replay、GPU readiness 或成功率异常都优先恢复 token-cost，不用压测数据掩盖运行时故障。
