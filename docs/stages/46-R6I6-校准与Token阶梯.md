# 阶段 46：R6I-6 校准与 Token 阶梯

## 1. 范围

本阶段把 R6I-2–R6I-5 的 static estimator、逐请求证据和缓存隔离协议放到真实 RTX 4060 双 vLLM
参考集群中，完成 512–3072 prompt token 校准、低负载验证和并发 1/4/8/16 阶梯。目标是做 promotion
决策，不以“必须优于 baseline”为前提。

## 2. 实现

- 新增 `configs/token-load-ladder.json`，固定 2048 prompt token、256 output token、`ignore_eos=true`；
- static profile 增加 `local_delta_ms` 与明确的 load bounds，超出实测包络时返回 typed `out-of-range`；
- load 有效时用 `max(local_inflight-running, 0)` 补偿观测滞后，未知时才使用完整 local fallback；
- tokenization/KV lookup 保持并行，最终 snapshot、选择和 lease reservation 在短临界区原子提交；
- 交付 token-cost 校准 overlay、immutable static profile ConfigMap、SHA/digest provenance 和独立实验报告。

## 3. 证据

校准模板实际得到 512/1025/2048/3072 token。低负载 static cold/warm estimator MAE 分别为
2.34/5.44 ms；2048-token 并发阶梯中 static 与 token-cost 均为 120/120 成功，但 static 整体 TTFT
P95 为 132.64 ms，对照为 128.62 ms（+3.13%，bootstrap 95% CI [-3.98%, +9.38%]），estimator
MAE 上升至 27.57 ms、absolute error P95 为 71.54 ms。

完整口径、分档表和无效 run 处理见
[`docs/experiments/2026-08-16-r6i6-token-ladder.md`](../experiments/2026-08-16-r6i6-token-ladder.md)。

## 4. 决策与运行状态

static 没有通过并发负载误差与性能门禁，不升级为默认或 active。参考集群已恢复
`r6i6-calibration-token-cost`，GPU Node Ready、vLLM 2/2、Gateway 1/1；static overlay 仅作为显式、可回滚的
研究交付物。最终 Gateway rollout 的真实请求为 200，但既有 vLLM generation 的 replay 出现 sequence gap，
请求安全降级为 `kv-aware-load-fallback-v1`；本阶段没有以重启健康 vLLM 的方式掩盖该验收缺口。queue 和
HardOverload 在本轮没有真实触发，因此不声称已覆盖这些压力区间或最终 rollout 的 KV replay 恢复。

## 5. 下一阶段

R6I-7 仅做 learned-shadow：复用现有 JSONL 证据比较 static/learned 的 MAE、P95 absolute error 和
would-select agreement。若不能稳定优于 static，研究到此收口，不增加在线权重更新器或外部服务。
