# R6I-30：round-robin、load-aware 与 KV-aware 三臂路由实验计划

## 目的

本轮不重测已完成的 4/12 KiB 全矩阵，也不把 R6H 的 `76.7%` 当作待复现目标。它只回答：在相同镜像、
双层缓存隔离和长共享前缀负载下，KV locality 相对强负载感知普通路由是否仍有净收益；以及普通负载感知
相对无信号轮转的收益。

`round-robin` 是 Gateway 内请求级的“无调度信号”消融，不是 Kubernetes Service kube-proxy 的逐连接实现。
历史 direct-Service 数据保留为端到端网络路径参考，不能与本策略混为同一组。

## 三臂与固定条件

| Arm | 路由模式 / policy | 作用 |
| --- | --- | --- |
| RR | `round-robin` / `round-robin-v1` | 无负载、无 KV 信号的请求级基线 |
| LA | `load-aware` / `load-aware-v1` | 默认普通路径；观测不完整时发布 `load-balanced-v1` |
| KV | `kv-aware` / `kv-aware-v1` | KV locality + 等价 token 成本；KV 无效时发布实际 fallback policy |

每个 arm 固定同一 Gateway image digest、Qwen2.5-0.5B、vLLM 0.23.0、两个 time-sliced Pod、
`gpu-memory-utilization=0.40`、Gateway max in-flight `64`、connections `16`、keep-alive。admission
固定 `off`：R6I-25 的 active controller 没有 hard rejection，recovery 也没有下调 target，尚不足以作为
默认生产策略或本轮因子。

## 最小新增矩阵

计划见 [`r6i30-routing-triad.json`](../../configs/r6i30-routing-triad.json)。每 arm 每轮 96 请求，两个
replicate、三臂共 576 个正式请求。

| 场景 | 目的 | batch × size | 并发 / offered QPS |
| --- | --- | ---: | ---: |
| shared-3072 | 已多次出现 locality 信号的锚点 | 2 × 16 | 8 / 4 |
| mixed-3072 | 60/20/20 热共享、unique、其他共享分布 | 2 × 16 | 8 / 8 |
| random-3072 | 无 locality 时的 KV 固定成本基线 | 2 × 16 | 8 / 4 |

同一 replicate 的三臂复用 seed 与逻辑输入顺序；replicate 2 使用新 seed 并反向 arm 顺序：
`RR → LA → KV`，再 `KV → LA → RR`。每 arm 均滚动 vLLM 与 Gateway；KV arm 额外等待两个 backend
replay valid。这会隔离上一 arm 的 vLLM prefix cache、Gateway index/subscriber 和 admission state。

## 必须留存与判定

- 每 arm：`requests.jsonl`、report、Gateway metrics window、ConfigMap、Deployment、Pod UID、vLLM
  generation、image digest、seed、run nonce、cache generation；
- 直接抓取 vLLM `/metrics` 的 prefix-cache 指标。LA 的 `kv_status=not-requested` 不能写成 cache miss；
- 比较 `RR vs LA`、`LA vs KV`、`RR vs KV` 三对，均为 20,000 bootstrap samples、固定 seed；同时报告
  场景级 P50/P95/P99、成功率、QPS、Little's Law、policy/backend 分布、KV 状态和 cached tokens；
- 只有 `LA vs KV` pooled P95 CI 完全小于 0，且 shared/mixed 至少一项同方向、KV available >=99%、
  成功率不下降，才可写“当前 profile 下 KV 有净收益”。否则只保留局部 shared 信号。

任一 arm 出现非 Ready、replay 未 valid、成功率低于 99.5%、镜像/配置/Pod generation 不一致或 rollout
traffic 混入正式 JSONL，应停止该 replicate、标记无效并恢复
`deploy/experiments/r6i22-final/load-aware` + admission off；禁止与有效数据 pooling。

## 声明式 overlays

- [`round-robin-off`](../../deploy/experiments/r6i30-routing-triad/round-robin-off/)
- [`load-aware-off`](../../deploy/experiments/r6i30-routing-triad/load-aware-off/)
- [`kv-aware-off`](../../deploy/experiments/r6i30-routing-triad/kv-aware-off/)

测试 agent prompt 见 [`r6i30-routing-triad-test-agent-prompt.md`](../notes/r6i30-routing-triad-test-agent-prompt.md)。
