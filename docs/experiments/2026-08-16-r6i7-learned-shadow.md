# R6I-7 Learned-shadow 真实集群实验

## 结论

本轮不允许 learned-active，也不改变默认 `token-cost`。learned-shadow 在已校准的 512/1024/2048
token 区间上有一定预测价值，但启动阶段覆盖不足、would-select 一致性跨轮不稳定；在 3072 档实际
prompt token 为 3078，超过 static profile 的 3072 上界，static 正确回退为 token-cost，因此不能把
这一档混入 static 精度结论。R6I-6 的并发 P95 promotion 也已经独立失败，所以没有 active 决策入口。

更具体地说：剔除 3072 out-of-range 档后，learned MAE 在两轮约比 static 低 10.4%，P95 absolute
error 两轮都不高于 static；但 would-select 与当前 static 实际选择的一致率从 79.5% 降至 66.3%，第二轮
低于预注册的 70% 门槛。模型可以作为 shadow 研究信号保留，不能承担生产选路。

## 环境与协议

- RTX 4060 8 GiB，两个 time-sliced vLLM 0.23.0 Pod；Qwen2.5-0.5B-Instruct，`max-model-len=4096`；
- Gateway `static-ttft + prediction=shadow`，Gateway image
  `fishmesh-gateway:r6i7-learned-shadow-r1@sha256:490462aa2e930e2a4a113288b119be1adc5be8fd44fc0e91b44877eff5e200bf`；
- 每轮 4 个场景：512 same-prefix、1024 different-prefix、2048 mixed-prefix、3072 different-prefix；
  每个场景 2 批 × 20 请求，并发 8，共 160 个正式请求；两轮独立 run nonce，并在第二轮前重启 Gateway
  以清空内存模型；vLLM generation 保持为 `r6i7-6fcb895786`；
- 两轮均 160/160 成功、160/160 `KVStatus=available`，prompt token 缺失/越界均为 0；两个 backend
  都达到远高于 32 的可用 shadow 样本数；实验结束后已恢复 token-cost + prediction off。

原始产物：

- [第 1 轮 report](../../artifacts/bench/r6i7-shadow-r1/report.md) 与 [requests JSONL](../../artifacts/bench/r6i7-shadow-r1/requests.jsonl)；
- [第 2 轮 report](../../artifacts/bench/r6i7-shadow-r2/report.md) 与 [requests JSONL](../../artifacts/bench/r6i7-shadow-r2/requests.jsonl)；
- [跨轮 compare](../../artifacts/bench/r6i7-shadow-comparison/comparison.md) 与 [机器报告](../../artifacts/bench/r6i7-shadow-comparison/comparison.json)。

## 总体结果

下表是完整 160 请求的 shadow 汇总；其中 3072 档 static 为 out-of-range，不纳入下一表的公平精度比较。

| Run | 成功 | KV available | shadow 可用 | shadow would-select 一致 | static 样本 | TTFT P50/P95 |
|---|---:|---:|---:|---:|---:|---:|
| r1 | 160/160 | 160/160 | 128 | 91/128 = 71.1% | 117 | 219.42 / 936.36 ms |
| r2 | 160/160 | 160/160 | 126 | 79/126 = 62.7% | 116 | 195.50 / 769.45 ms |

两个 backend 的 shadow 可用记录分别为：r1 `endpoint-0058c3e5bd6d=62`、
`endpoint-71e4a227c489=66`；r2 为 `66`、`60`。路由实际请求分布 r1 为 `78/82`，r2 为 `84/76`，
说明样本并没有集中在单一 Pod。

## 公平精度比较

由于 3072 目标在本轮生成的实际 token 是 3078，static profile 对该档返回 `out-of-range` 并安全回退。
因此以下只比较 static 有校准意义的 512/1024/2048 档；每个数字来自同一请求的实际 TTFT 与对应
估计值的绝对误差。

| Run | 请求子集 | learned 样本 | static 样本 | learned MAE | static MAE | learned abs P95 | static abs P95 | paired learned-static |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| r1 | 120 | 88 | 117 | 117.23 ms | 130.78 ms | 254.18 ms | 428.86 ms | -19.26 ms |
| r2 | 120 | 86 | 116 | 91.84 ms | 102.57 ms | 282.51 ms | 287.01 ms | -34.49 ms |

`paired learned-static` 为 learned 绝对误差减 static 绝对误差，负数表示两者都有值的重叠请求上
learned 较小；它不能抵消第二轮一致率跌破门槛，也不能证明未观测 backend 的反事实 TTFT。

按场景的请求级证据如下：

| Run / 场景 | 请求 | shadow 可用 | 一致 | static 有效 | TTFT P50/P95 |
|---|---:|---:|---:|---:|---:|
| r1 / 512 same | 40 | 8 | 4 | 40 | 54.70 / 469.04 ms |
| r1 / 1024 different | 40 | 40 | 33 | 40 | 190.89 / 352.71 ms |
| r1 / 2048 mixed | 40 | 40 | 33 | 37 | 202.09 / 665.33 ms |
| r1 / 3072 different | 40 | 40 | 21 | 0（out-of-range） | 543.37 / 1178.60 ms |
| r2 / 512 same | 40 | 6 | 3 | 40 | 52.22 / 207.97 ms |
| r2 / 1024 different | 40 | 40 | 26 | 40 | 200.94 / 354.45 ms |
| r2 / 2048 mixed | 40 | 40 | 28 | 36 | 145.15 / 666.45 ms |
| r2 / 3072 different | 40 | 40 | 22 | 0（out-of-range） | 531.04 / 1043.00 ms |

512 档前若干请求处于 `insufficient-data` 是安全行为：模型没有足够样本时不输出预测，不伪造零误差。
这也说明当前实现需要单独的 calibration warmup 或持久化已验证 profile，不能在生产请求流中期待模型立即可用。

## 门禁判定

| 门禁 | 结果 | 说明 |
|---|---|---|
| 双 vLLM/Gateway/backend Ready | 通过 | 两轮压测前均为两个 vLLM Ready、两个 KV instance valid、Gateway Ready |
| 成功率 ≥98% | 通过 | 320/320 成功 |
| KV available ≥95% | 通过 | 320/320 |
| 每 backend 每轮 ≥32 shadow 样本 | 通过 | 每轮 60–66 条 shadow 可用记录/Pod |
| learned MAE 至少低 10%、P95 不高于 static | 通过（仅校准子集） | 两轮 MAE 约低 10.4%，P95 均不高于 static |
| would-select 一致率 ≥70%，且两轮稳定 | 不通过 | r1 71.1%，r2 62.7% |
| active/default promotion | 不适用/不允许 | R6I-6 并发 P95 已为 +3.13%，CI [-3.98%, +9.38%]，先验 promotion 已失败 |

## 最终运行状态

实验完成后应用了 `deploy/experiments/r6i7-token-cost-recovery`：

- `FISHMESH_KV_AWARE_ESTIMATOR_MODE=token-cost`；
- `FISHMESH_PREDICTION_MODE=off`；
- Gateway 1/1 Ready，两个 vLLM 2/2 Ready，GPU node Ready；
- 两个 KV instance 仍为 `ready/valid=1`；
- recovery 第一次预校验发现 `off` 未加引号会被 YAML 解析成布尔值，已修正为字符串并重新通过 server dry-run；
  这次配置修复不影响实验数据。

结论是：保留 learned-shadow 代码和实验产物用于后续研究，但当前产品默认继续使用 token-cost；下一次若继续，
应先把 3072 actual token 档校准到 profile 上界内，再增加有独立 calibration warmup 的跨轮实验。除非一致率
在独立轮次稳定达到门槛且 R6I-6 性能门重新通过，否则不再把动态模型接入 active routing。
