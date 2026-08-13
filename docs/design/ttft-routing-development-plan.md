# FishMesh R6I：可校准 TTFT 路由与可信实验多阶段开发方案

> 状态：R6I-0–R6I-6 已完成；static 通过低负载精度校验，但未通过并发 promotion gate，当前保持
> `token-cost`。设计决策见
> [`ADR-003`](decisions/003-calibrated-ttft-routing.md)。本计划优先交付安全、可解释的 Lite 调度器，
> 再用隔离实验校准和证明，不把在线学习或大规模组件提前放进主请求路径。

## 1. 最终交付目标

R6I 完成时，FishMesh 应交付：

1. 真实 KV residency、快速 load observation 和 Gateway 本地事实组成的不可变候选快照；
2. 明确运行的 HardOverload 安全门；
3. 以毫秒为单位、硬件/profile 版本化的 calibrated-static TTFT estimator；
4. 不改变实际选择的 learned-shadow estimator，以及误差/置信度证据；
5. cold/controlled-warm/steady-warm 隔离的 token 阶梯压测工具和机器可读 provenance；
6. 一张回答“缓存多长、队列差多少时应放弃 locality”的切换边界图；
7. load-balanced 或 static KV-aware 的一键回滚面。

## 2. 阶段总览

| 阶段 | 主题 | 主要交付 | 是否需要 GPU |
| --- | --- | --- | --- |
| R6I-0 | 决策与契约 | ADR-003、本计划、停止条件 | 否 |
| R6I-1 | Load 与 HardOverload | 快速观测配置、运行时 HardOverload、去重成本语义 | 否；末尾 smoke 才需要 |
| R6I-2 | Static estimator 契约 | 毫秒估算值、profile/schema、纯函数 contract tests | 否 |
| R6I-3 | Calibrated-static 接入 | requestpath 投影、routing 选择、typed confidence/fallback | 否；末尾 smoke 才需要 |
| R6I-4 | 决策证据 | 低基数 metrics、JSONL 数值 provenance、compare 命令 | 否 |
| R6I-5 | 实验隔离 | cache salt/run nonce、cold/warm 协议、五轮统计 | 否；正式运行需要 |
| R6I-6 | Profile 与阶梯实验 | 512–3072 token 校准、并发/locality/load 阶梯 | 是 |
| R6I-7 | Shadow 校准 | learned-shadow MAE/P95 error/agreement | 是 |
| R6I-8 | Active 决策门 | 是否允许 learned-active 的独立 ADR | 取决于证据 |

阶段编号表达依赖，不要求在一个提交或一次 GPU 在线窗口全部完成。每个阶段必须在本地门禁通过后才进入下一个；
GPU 不稳定不会阻止纯代码阶段，但禁止用 simulator 伪造真实性能验收。

截至 2026-08-16，R6I-6 已完成：低负载 static estimator MAE 为 2.34–5.44 ms；2048-token 并发阶梯
MAE 上升到 27.57 ms，整体 TTFT P95 相对 token-cost 为 +3.13%，置信区间跨 0。static 不进入默认/active，
完整证据见 [`2026-08-16-r6i6-token-ladder.md`](../experiments/2026-08-16-r6i6-token-ladder.md)。

## 3. R6I-0：决策与契约

### 目标

固定优化目标、依赖方向、隐私边界、实验口径和停止条件，避免实现中临时增加动态权重。

### 交付

- ADR-003；
- 本阶段计划；
- `docs/stages/` 入口和项目状态更新；
- 明确当前 12 KiB 约为 2K token，不是 12K token；
- 明确最新 A/B 未启用 queue/running 且没有 KV generation 隔离。

### 通过条件

- 静态正式交付、动态影子、active 后置三者边界无歧义；
- 不新增外部服务或反向依赖；
- 后续每个类型都有明确 owner。

## 4. R6I-1：Load observation 与 HardOverload

### 目标

让 standalone Lite 真实获得调度时间尺度上的 queue/running，并把 HardOverload 从测试输入接入运行时。

### 代码变更

1. `config` 增加：
   - `FISHMESH_BACKEND_OBSERVATION_REQUEST_TIMEOUT`；
   - `FISHMESH_KV_AWARE_HARD_QUEUE_DEPTH`；
   - `FISHMESH_KV_AWARE_HARD_LOCAL_INFLIGHT`。
2. `requestpath.Config` 拥有 HardOverload snapshot 发布门槛；`routing.Load` 继续只消费已发布事实。
3. `buildSnapshot` 在同一时刻读取 local in-flight，并结合新鲜 queue 计算 `HardOverload`。
4. KV 成本去重：load valid 时使用 queue/running，load invalid 时才使用完整 local in-flight。
5. `lite-kv-aware` 和长上下文实验 overlay 显式启用 Prometheus observation，初始使用
   `500ms/2s/400ms`。

### Contract tests

- queue 等于门槛时 hard overload；
- queue unknown 不触发 queue 门，但 local 门仍有效；
- 所有候选 hard overload 时 typed fallback；
- load valid 时 local in-flight 不重复进入成本；
- load invalid 时 local in-flight 仍保护单 Gateway；
- 负数门槛和非法 timeout 拒绝；
- 环境变量完整映射。

### 真实 smoke

只验证：2/2 vLLM Ready、load samples valid、queue/running 随受控请求变化、HardOverload reason 可触发并恢复。
不在该阶段声称 TTFT 改善。

## 5. R6I-2：Calibrated-static estimator 契约

### 目标

建立纯、可版本化、以毫秒为单位的估算契约，不立即绑定真实系数。

### 值对象

```text
PromptWork {
  prompt_tokens
  cached_prefix_tokens
  uncached_tokens
}

LoadWork {
  queue_depth
  running_requests
  local_delta
  valid
  observed_at
}

LatencyEstimate {
  ttft
  valid
  confidence
  estimator_version
  reason
}
```

### Profile

profile 是有界、单调的 prompt/cache 二维分段表，加上 load 的毫秒系数和适用 identity：

```text
model
hardware_profile
vllm_version
max_model_len
prefill_breakpoints
queue_ms
running_ms
local_delta_ms
version
```

配置不保存 prompt 内容，不允许负系数、重复 breakpoint、非单调 prefill 或空版本。

### 通过条件

- 纯函数 tests 覆盖插值、边界、溢出、未知 load、版本不匹配和保守 fallback；
- profile 未校准时不能标记 `calibrated=true`；
- routing 不 import HTTP/Prometheus/prediction。

## 6. R6I-3：Static estimator 请求路径接入

### 目标

让实际 KV-aware 选择使用 `EstimatedTTFT`，同时保留 R6H token cost 作为可回滚兼容路径，直到真实校准完成。

### 请求顺序

```text
tokenize
→ KV lookup
→ load/local snapshot
→ HardOverload filter
→ static estimate per candidate
→ minimum estimated TTFT
→ hysteresis/tie-break
→ lease
```

### 降级

- estimate 全部有效：`kv-aware-ttft-static-v1`；
- profile 不匹配或部分无效：回到 R6H token cost，并记录 estimator degradation；
- KV unknown/stale：既有 load-balanced fallback；
- load unknown：static estimator 使用 local fallback 并降低 confidence；
- discovery/circuit 失败：既有 Service fallback/503 契约。

### 通过条件

- 相同 snapshot 产生确定性选择；
- estimator 不得绕过 circuit/HardOverload；
- profile 故障不改变 KV unknown 语义；
- Standard llm-d 未提供相同 estimate 前继续使用共享兼容策略，不伪造 Lite 数据。

## 7. R6I-4：决策观测与产物

### 目标

让每次实验可以解释选路，但不泄露请求或制造高基数 Prometheus 标签。

### 交付

- HTTP/JSONL allowlist 数值：prompt/cached/uncached、estimate、load age、confidence；
- Prometheus histogram/counter：estimator status、estimate、absolute error、HardOverload decision；
- `fishmesh-client compare`：从多轮 report 计算 pooled percentile、run median、bootstrap CI；
- benchmark metadata：Git/image/Pod/vLLM/config/seed/cache generation；
- 评审后的 compact report 跟踪进 Git，raw JSONL 继续留在 `artifacts/`。

## 8. R6I-5：缓存隔离与 workload

### 三种 cache 状态

| 状态 | 建立方式 | 回答问题 |
| --- | --- | --- |
| cold | 新 cache salt 或新 vLLM generation | 智能路由纯开销 |
| controlled-warm | 指定 backend 预热后冻结 generation | locality/load 切换边界 |
| steady-warm | 固定业务分布自然运行 | 尾延迟和长期稳定性 |

每个 run 使用独立 nonce；paired treatment 使用相同 workload seed，但不能复用上一 treatment 的 cache namespace。
所有场景顺序由 seed 决定并写入 plan。

## 9. R6I-6：校准与长上下文阶梯

### 容量前置

当前 `max-model-len=4096` 只运行 512/1024/2048/3072 token。每档先进行 Render/token count 和单请求
显存验证，再进行双 backend 并发。GPU memory 或 startup capacity 不满足时立即停止，不通过缩小证据口径伪造通过。

扩展到 4096/8192/12288 token 前必须：

1. 单 Pod 启动和 `max concurrency >= 1`；
2. 双 Pod 2/2 Ready 且显存留有稳定余量；
3. 温度/watchdog 无告警；
4. 请求记录的 `prompt_tokens` 达到目标范围；
5. 不把 byte 长度作为 token 证据。

### 最小矩阵

```text
prompt tokens: 512 / 1024 / 2048 / 3072
cache ratio:   0 / 25 / 50 / 75 / 100%
concurrency:   1 / 4 / 8 / 16
treatment:     load-balanced / KV-only / KV+load / static-TTFT
```

先用 `max_tokens=1` 校准 TTFT，再用固定 32/128 output token 做产品 profile。达到统计门槛即停止，不扩展模型和
更多路由模式。

### 核心切换边界

controlled-warm 中固定一个 cache owner，扫描 owner queue 0/1/2/4/8 和 cached prefix，记录 scheduler
切换点、实际 TTFT 与估算误差。最终主图必须直接回答：

> 预计节省多少 prefill 时，值得接受多长 queue？

## 10. R6I-7：Learned-shadow

### 目标

使用受控 workload 为每 backend 收集平衡样本，验证在线模型是否比 calibrated-static 更准确，而不是立即改变流量。

### 报告

- 每 backend 样本覆盖；
- MAE、P50/P95 absolute error；
- actual 与 would-select agreement；
- 按 prompt/cache/load bucket 的误差；
- drift、过期和 fallback 次数；
- static 与 learned 在同一请求上的 paired error。

若 learned 没有稳定优于 static，保留 shadow 作为研究结论，不扩大参数和生产复杂度。

## 11. R6I-8：Active 决策门

本阶段不是默认路线。只有 R6I-7 通过预注册门槛才写新 ADR，选择 `off|shadow|active` 交付面、canary、系数
clamp、更新频率和回滚。未通过时，R6I 在 calibrated-static 完成交付，不视为失败。

## 12. GPU 不稳定停止条件

任何真实集群阶段开始前先只读检查：

```text
GPU Node Ready
qwen-vllm Deployment 2/2 Available
EndpointSlice 2 个 Ready/serving backend
Gateway 1/1 Ready
无持续 CrashLoopBackOff/Unknown GPU Pod
```

出现以下任一情况立即停止，不继续 rollout、预热或压测，并等待用户协助：

- GPU Node `NotReady`；
- vLLM 长时间不是 2/2；
- device plugin unhealthy；
- GPU Pod `Unknown`/CrashLoopBackOff 持续；
- watchdog、温度、显存或 CUDA 初始化异常；
- Gateway 只能通过 Service fallback 工作，无法确认两 backend membership/KV replay。

## 13. 每阶段共同门禁

```bash
gofmt -w <changed-go-files>
go test -race ./...
go vet ./...
go build ./...
make manifest
git diff --check
```

完成阶段还必须更新：

- `docs/stages/<stage>.md`；
- `docs/stages/README.md`；
- `docs/notes/project-status.md`；
- 用户可见协议改变时更新 README/README_CN；
- 声称性能前保留机器可读原始 evidence 和 compact tracked report。
