# R6I-31：低并发逐轮增长对话至 32K 的 KV-aware 验证

## 结论

找到了 KV-aware 的明确优势区间：三个低并发、持续多轮对话用户，实际上下文从 `6,837` 增长到
`31,479` tokens，且各用户的上一轮回复严格进入下一轮真实 prompt。在两个独立、反向顺序的 vLLM/Gateway
冷启动 replicate 中，KV-aware 相对 load-aware 的 pooled TTFT P95 下降 **55.42%**，bootstrap 95% CI 为
**[-67.28%, -31.52%]**，不跨 0；四个 arm 均为 21/21 成功。

这不是“KV 对所有负载都更好”的结论。它说明：当长会话的前缀能在同一 vLLM Pod 内复用、而普通负载感知
为了均衡 queue/running 会把同一用户的连续请求分散到不同 Pod 时，KV locality 的 prefill 节省压过了
load-aware 的均衡收益。短/随机/高并发矩阵仍应默认 load-aware，并保持既有 R6I-30 结论。

## 受控条件

- Qwen2.5-0.5B-Instruct、vLLM 0.23.0、两个 time-sliced vLLM Pod；`--max-model-len=32768`、
  `--gpu-memory-utilization=0.40`、chunked prefill、`max-num-seqs=3`；
- admission `off`，Gateway max in-flight `16`、每 host connection `8`；
- 每 arm 为 3 个用户 × 7 轮 × 每轮 3 请求 = 21 请求，轮内 concurrency 3、轮间 1s；
- 16 KiB 共享 system prefix 加每轮约 18.4 KiB 用户增量；有效 prompt token 阶梯为
  `6,837 → 10,944 → 15,051 → 19,158 → 23,265 → 27,372 → 31,479`；
- arm 顺序为 `LA → KV` 和 `KV → LA`，每个 arm 使用唯一 Deployment generation/Pod-template annotation，
  只有两个新 vLLM Pod 和新 Gateway 均 Ready 后开始；KV arm 还要求两个 backend replay valid。因此上一 arm
  的 vLLM prefix cache、Gateway KV index 和 in-flight 不会混入。

实验配置为 [`r6i31-conversation-ladder-28k.json`](../../configs/r6i31-conversation-ladder-28k.json)。文件名保留
最初的预算命名，实际最后一轮为 31,479 tokens，仍在 32K 上限内。

## 正式结果

| Arm | 成功 | TTFT P50 | TTFT P95 | 最终轮 P95 |
| --- | ---: | ---: | ---: | ---: |
| load-aware r1 | 21/21 | 1295.15 ms | 4471.19 ms | 4471.19 ms |
| KV-aware r1 | 21/21 | 1168.09 ms | 1804.62 ms | 1453.27 ms |
| KV-aware r2 | 21/21 | 1150.52 ms | 1771.68 ms | 1463.17 ms |
| load-aware r2 | 21/21 | 1235.49 ms | 3973.91 ms | 2360.70 ms |

Pooling 后 baseline/treatment P95 为 `3973.91 / 1771.68 ms`；完成 QPS 为 `1.303 / 1.941`，没有 rejection。
KV 两轮均为 `21/21 kv_status=available`。首轮的每用户 cached prefix token 从第 1 轮的 `6,832` 递增至
末轮的 `27,376`，末轮只需 prefill 约 `4,103` 未缓存 tokens；这是收益的直接机制证据，而不是仅凭 TTFT
相关性推断。

完整可复核产物在
[`artifacts/bench/r6i31-conversation-ladder-28k-v3/`](../../artifacts/bench/r6i31-conversation-ladder-28k-v3/)，其中
`compare/comparison.md` 为 pooled 统计，四个 `runs/*/bench/requests.jsonl` 保留逐请求 route、backend、KV
状态和 cached token。此前 v2 运行发现 rollout 状态可能在 controller 尚未观测新 generation 时误用旧 Ready
状态；该批产物已明确作废，v3 的 generation gate 修复后重跑并作为唯一正式结果。

## 产品与表述边界

可以写："FishMesh 在双 vLLM Pod、低并发持续会话、6.8K–31.5K token 上下文的真实 prefix-cache 复用场景中，
KV-aware 路由将 TTFT P95 降低 55.4%（双 replicate bootstrap CI [-67.3%, -31.5%]），并通过 per-request cached
prefix evidence 验证机制。"

不能写："KV-aware 通用降低 55.4%"、"支持 100K context"，或把它与 Kubernetes Service 的连接级负载均衡
等同。当前模型的 native 上限是 32K；此实验也只验证了 3 个并发增长会话和这台 GPU/模型/profile。

## 后续

1. 以此为唯一长会话 promotion profile，补 3–5 个独立 seed（不必重跑已完成短/随机矩阵）。
2. 增加一个会话数 6/12、仍低总并发的梯度，找出 KV locality 被 load pressure/eviction 反转的边界。
3. 默认策略保持 load-aware；仅在 `kv_status=available`、长上下文且预计复用充分时进入 KV-aware，信号失效
   必须降级到 load-aware，再到本地 load-balanced。
