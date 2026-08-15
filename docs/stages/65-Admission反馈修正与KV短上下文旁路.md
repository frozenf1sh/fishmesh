# 阶段 65：Admission 反馈修正与 KV 短上下文旁路

## 1. 交付

- `admission` 将 soft target rejection 与 hard limit rejection 拆成 typed error；Gateway 保留总拒绝计数，
  同时暴露 `admission_soft_rejections_total` 和 `admission_hard_rejections_total`。
- Admission tuner 新增两类拒绝增量：只有 hard rejection 才触发 target 下调；soft target rejection 只进入
  `soft-target-pressure`，不会把控制器自己的拒绝继续反馈成更低 target。高 in-flight 但没有 hard rejection
  时保持 `saturated`，避免长连接场景出现不必要的 target cliff。
- KV-aware 增加可选 `FISHMESH_KV_AWARE_SHORT_PROMPT_TOKENS`。阈值为 0 时关闭；启用后，精确 tokenization
  得到的 prompt token 数不超过阈值时跳过 per-request KV lookup，使用相同的 load-aware fallback。
- 新增 `short-context-bypassed` KV status、`kv-aware-short-context-fallback` reason/policy 和独立
  `kv_aware_bypasses_total` 指标；它不会被误计为 KV signal failure/degradation。
- 新增 `deploy/experiments/r6i22-final/kv-aware-short-bypass` 实验 overlay，初始阈值为 2048 token；默认
  `load-balanced` 和默认 KV-aware 配置均不改变。

## 2. 验证

- admission contract test 覆盖 hard-limit、soft-target、soft rejection 不自反馈降级和 active hard rejection。
- requestpath contract test 确认短上下文不调用 `kvcache.Lookup`，仍返回 load-aware fallback、明确 reason/policy
  和 `short-context-bypassed` 状态；默认阈值 0 的现有 KV query 路径保持不变。
- Gateway metrics test 确认 soft/hard rejection 与短上下文 bypass 均有独立指标，bypass 不进入 degradation counter。
- `kv-aware-short-bypass` Kustomize overlay 已渲染，配置明确为 `2048` 且触发 Gateway runtime config rollout。

## 3. 真实验收与当前边界

- 新镜像 `r6i24-admission-kv-bypass-r1`（digest
  `sha256:903c10b37dbc359946bff8dc592d3d6c96841e7f86afd4e35b607fda25c05553`）已在 GPU 集群滚动验证。
- `kv-aware-short-bypass` 以 2048 token 阈值运行短请求 smoke：HTTP `200`，响应明确为
  `short-context-bypassed` / `kv-aware-short-context-load-fallback-v1`，bypass counter 增加，degradation
  counter 未增加；验证后已恢复 baseline。
- 修正版 active 长连接 drain `r6i24-drain-active-r1` 为 `128/128` 成功、soft/hard rejection 均为 0，
  TTFT P95 `120.41 ms`，Gateway accepted/completed `1.564 QPS`，平均 in-flight `20.155`，Little's Law
  W `12888.69 ms`；控制器执行 4 次调整，最终 target=64，没有再出现旧版本 target=16 的自反馈拒绝。
- 实验结束后已恢复 `load-balanced / initial target=128 / tuning=off`；两个 vLLM Pod 仍为 Ready、重启数为 0，
  Gateway `/readyz` 正常。

短上下文阈值仍需在 512/1024/2048/3072 token 阶梯上进行真实 A/B；tokenization 仍是确定阈值所需的开销，
本阶段只跳过 per-request KV lookup，不声称完全消除 KV 相关成本。

`session-key` 继续是 frozen compatibility mode，不进入维护路径和收益矩阵。
