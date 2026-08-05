# FishMesh 工程验证方案

> 状态：2026-08-11 收缩。实验只服务于真实 KV 主能力的门禁、验收和性能回归，不再作为独立
> 产品路线。历史报告保留，但不会继续扩展相同矩阵。

## 1. 验证优先级

不同问题使用不同环境，不能因为 simulator 方便就回避真实集群：

| 优先级 | 环境 | 允许回答的问题 |
| --- | --- | --- |
| 1 | 真实 K3s + vLLM | KVEvents、Render token、prefix match、eviction、Pod restart、SSE、rollout、资源和性能 |
| 2 | Go unit/race/contract test | 纯策略、容量、freshness、并发、取消和状态不变量 |
| 3 | 已有 simulator | HTTP/SSE error、held stream、取消、circuit、admission、确定性 endpoint fault |

R6 不为证明产品可行而增加模拟 KVEvents、模拟 tokenizer 或新的长时间无 GPU soak。真实信号闭环
后，可以增加最小 fixture 防止已经确认的行为回归。

## 2. 准入条件

运行验证前必须写明：

1. **工程决策**：结果决定哪个实现、默认值、资源预算或是否继续；
2. **baseline/treatment**：至少一个当前行为和一个候选行为；
3. **预设判定**：运行前确定正确性或性能阈值；
4. **后续动作**：通过、失败和结果不确定分别触发什么；
5. **最小环境**：真实 engine contract 不得用 mock，纯状态机不占用 GPU；
6. **停止条件**：证据足够做决定后不继续扩大矩阵。

不能指向工程动作的实验不进入当前里程碑。为了增加图表、技术名词或代码量的探索进入个人
笔记，不进入仓库主线。

## 3. 当前只回答四个问题

### Q1：真实 KV locality 是否可获得

- vLLM 0.23.0 能否同时启用 prefix caching、KVEvents publisher 和 replay；
- 两个 Pod 的 stored/removed 事件能否被稳定区分；
- Render API 是否返回与模型实际模板一致的 Token IDs；
- 不同 session、相同 system prompt 是否产生非零公共 block prefix；
- eviction、Pod restart、断流和 replay 后索引是否正确。

结果决定是否进入 R6B。任一核心不变量无法满足时，先评估版本 pin/upgrade；不能用 session key
或累计 hit rate 假装 exact 成功。

### Q2：联合策略是否比单一信号更安全

固定请求和 endpoint 状态，对比：

- load-only；
- cache-only（风险对照，不作为生产候选）；
- exact-cache-load；
- optional session hint 对 tie-break 的影响。

重点验证严重过载时是否拒绝追逐 cache、信号 stale 时是否明确降级、相同输入是否确定性选择。

### Q3：Lite mode 的轻量成本是否成立

测量：

- bounded body capture 和 Render latency；
- KV index lookup p50/p95/p99；
- event ingest lag 和 replay recovery；
- Gateway CPU/RSS 与 index entry 数量；
- direct Service、load-only、exact 下的 SSE token throughput；
- cache-cold TTFT overhead。

结果决定默认 index 容量、Gateway resources、是否需要 renderer Service，以及 Lite 的适用规模。

### Q4：Lite 与 Standard 的适用边界是什么

只在 Lite MVP 完成后对比 FishMesh exact 和 llm-d precise：

- 安装对象、常驻进程、权限和资源；
- prefix-heavy 与 cache-cold workload；
- event stale、EPP/Gateway 失败和 rollout；
- 能力差异：TLS、多租户、流控、多池、HA 与运维复杂度。

目的不是证明 FishMesh 全面击败 llm-d，而是形成可信的选择指南。

## 4. R6A 真实信号门禁（已完成）

### 4.1 固定环境

- Kubeconfig：`~/.kube/fishmesh.yaml`；
- Namespace：`kubellm`；
- vLLM：固定 0.23.0 镜像/digest；
- 模型：当前 Qwen2.5-0.5B-Instruct；
- endpoint：两个 time-sliced vLLM Pod；
- prefix caching：开启；
- KVEvents：为每 Pod 配置唯一 topic、publisher 和 replay endpoint。

time-slicing 不代表独立 GPU，因此本阶段只判断 signal correctness、单卡相对开销和恢复语义。

### 4.2 最小请求集

准备三类 OpenAI Chat 请求：

1. A/B 使用不同 session 和不同 user message，但共享足够长且字节完全相同的 system prompt；
2. C 使用完全不同 system prompt；
3. D 与 A 内容相同但使用隔离 cache salt（若当前 API 支持并能传递）。

先让 A 在指定 Pod 完成，再查询 B/C/D 的逐 Pod matched prefix。必须记录 prompt tokens、完整 block
数量、matched blocks/tokens、Pod UID、event timestamp 和选择 reason。

### 4.3 故障步骤

- 触发足够请求产生 block stored；
- 产生缓存压力或等待自然 eviction，观察 removed；
- 删除一个 vLLM Pod，确认旧 UID 索引清理；
- 新 Pod Ready 后确认不会继承旧 locality；
- 中断 subscriber，超过 freshness 后确认 exact invalid；
- 恢复连接并使用 replay，确认恢复或明确 full resync 边界。

### 4.4 通过条件

- A/B 在实际拥有 cache 的 Pod 上得到非零公共 prefix；
- C 不产生虚假公共 prefix；
- salt 隔离不发生跨域匹配；
- removed/restart 后不继续报告旧 block；
- stale 状态可观测并触发 load-aware degradation；
- lookup/ingest 状态有明确容量和回收方式；
- 结果可通过脚本和声明式配置复现。

2026-08-11 门禁已通过：跨会话公共 system prompt 在实际缓存 Pod 命中 8 blocks/128 tokens，
断流先 invalid 后由 replay 恢复，真实缓存压力产生 3105 个 removed 并清除旧命中，Pod UID 重建
后旧 locality 归零。cache salt 隔离没有在本轮文本 MVP 中单独施压，保留为 R6B adapter contract
test；它不影响 ADR-002 的文本 exact 数据源 Go/no-go。完整证据见
[`阶段 18`](../stages/18-R6A真实KV信号闭环.md)。

## 5. R6D 有限性能矩阵

R6A 不跑大矩阵。R6D 只比较四个 treatment：

- `service`：Kubernetes Service 直连；
- `fishmesh-load-only`；
- `fishmesh-exact`；
- `llmd-precise`。

只保留三类 workload：

| Workload | 目的 |
| --- | --- |
| cache-cold | 衡量智能路由纯开销 |
| shared-system-prefix | 衡量跨 session 真实复用收益 |
| shared-prefix + skew/overload | 验证 cache 与负载 trade-off |

每类使用短、中、长三个 prompt 档位和正常/饱和两个 concurrency 档位即可。达到置信区间和工程
决策所需样本后停止，不自动扩展 prefix groups、模型、输出长度和所有 Gateway provider。

## 6. 指标

### 正确性

- prompt tokens、matched prefix blocks/tokens；
- match source、valid/freshness/degradation reason；
- event stored/removed/replay/gap/reconnect；
- Pod UID/index entry 清理；
- selected/served endpoint；
- fallback、circuit、cancel 和 error outcome。

### 性能与资源

- Render latency；
- index lookup 和 routing decision p50/p95/p99；
- TTFT、E2E、TPOT/ITL；
- output token throughput；
- Gateway CPU/RSS、GC 和 request body bytes；
- index entries、admission/eviction 和 per-Pod memory；
- event ingest lag 和 stale duration；
- success/error/timeout rate。

节点级 GPU 指标只用于说明环境，不得写成各 time-sliced Pod 独立 GPU 利用率。

## 7. Artifact 与复现契约

每次运行保存：

```text
run_metadata
event/checkpoint records
request records
summary
```

`run_metadata` 至少包含：

- Git SHA、FishMesh image digest；
- vLLM/llm-d/Gateway 版本和 digest；
- vLLM args、KVEvents/replay 配置和 block size；
- cluster、node、GPU、model、tokenizer/chat template；
- treatment、policy/config、index bounds；
- request seed、warmup、measurement 和故障时间线。

Git 只保存代码、schema、脚本、声明式配置和评审后的结论。raw JSONL、压缩日志、节点转储和
集群快照仍保留在仓库外，不因为本次方向调整改变提交规范。

## 8. 统计与结论规则

- behavior/conformance 可以按明确不变量判定，不要求统计区间；
- performance 每个 treatment 至少多轮重复，报告分布和跨轮中位数；
- 报告绝对差值与相对变化，不只报告最好的一轮；
- 性能改善不能以更高失败率、错误 cache 声明或静默降级为代价；
- saturation 与正常稳态分开；
- 当前单卡结果明确限定为 correctness/profile；
- 简历使用跨 GPU 扩展或吞吐结论前，必须在至少两个独立物理 GPU/backend 上复验。

## 9. 历史数据处理

2026-08-08 及此前 keep-alive、prefix-hash、bounded-affinity 数据不删除，但它们只证明当时的
transport/行为结论。旧 `prefix_group/routing_key` 不是 vLLM token-block locality，不得在新 README、
简历或面试中重新解释为 exact cache-aware 结果。
