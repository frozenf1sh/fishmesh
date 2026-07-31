# FishMesh 可复现实验方案

> 本文件取代“每个策略跑一次并比较单个 P95”的方式。历史报告保留，但只作为探索性证据。

## 1. 要回答的问题

实验只验证四个独立假设：

1. **Transport**：固定路由策略时，HTTP keep-alive 的净效应是多少？
2. **Locality**：固定负载时，共享 prefix/session 的 affinity 在什么复用比例和 prompt 长度下
   才产生稳定收益？
3. **Overload**：热点倾斜时，bounded affinity 是否比 pure affinity 降低失败率和 P99，且
   保留大部分 locality 收益？
4. **Failure**：Pod、telemetry 或 Kubernetes API 异常时，策略能否在规定时间内停止选择
   无效 backend 并安全回退？

GPU-aware、eBPF 网络优化、LLM Agent 和 disaggregated serving 不属于本轮假设。

## 2. 对照策略

每个 workload 至少比较：

- `service-keepalive`：Kubernetes Service + connection reuse；
- `least-loaded`：EndpointSlice + local/vLLM load，不使用 affinity；
- `pure-affinity`：仅作为风险对照，不作为候选生产策略；
- `bounded-affinity`：preferred backend 未越过 spillover threshold 时保持亲和；
- `open-source-router`：llm-d EPP 或 vLLM Router，同版本 vLLM 和同一 workload。

`service-no-keepalive` 只用于 transport 假设，不参加后续 scheduler 排名。

## 3. 工作负载矩阵

### 3.1 基础维度

| 维度 | 建议取值 |
| --- | --- |
| prefix 热点比例 | 0%、25%、50%、75%、95% |
| prefix groups | 1、4、16 |
| prompt 长度 | 约 256、1K、4K tokens；模型允许时增加 16K |
| output 长度 | 16、128 tokens |
| concurrency | 1、4、8、16；超过硬件容量的档位标记 saturation |
| request count | 每轮 warmup 50 + measurement 至少 500 |
| repetitions | 每个 treatment 至少 7 轮 |

不能用 byte 数冒充 token 数。Loadgen 后续应记录 tokenizer/version 和最终 prompt token count；
完成前继续记录 byte 数，但报告必须显式标注近似值。

### 3.2 两类运行环境

1. **Real GPU correctness profile**：当前 RTX 4060 time-slicing，用来验证真实 vLLM、SSE、
   EndpointSlice、cache 行为和故障恢复，不宣称独立 GPU 扩展性。
2. **Controlled simulator profile**：多个可控 backend，注入 queue、cache、延迟和故障，用来
   验证策略不变量、阈值和恢复时间。

最终简历性能数字必须在至少两个独立物理 GPU/backend 上复验；可以使用短时云 GPU，不要求
长期保留集群。

## 4. 运行规程

每个 experiment block：

1. 验证节点、Pod、EndpointSlice、模型和 `/metrics` readiness；
2. 记录 Git SHA、镜像 digest、vLLM version/args、GPU driver、内核、节点和 policy config；
3. 为不同 treatment 生成随机执行顺序并保存 seed；
4. 使用独立 prefix namespace，避免跨 treatment 缓存污染；
5. 执行固定 warmup，不纳入 measurement；
6. 运行 measurement，完整消费 SSE；
7. 保存所有成功、失败、超时和 retry attempt，不覆盖文件；
8. 记录 vLLM counter 的运行前后 delta；
9. 一个 block 内出现 NodeNotReady、Pod restart 或环境变更时，整轮标记 invalid，保留但不
   混入性能统计；
10. 分析脚本只读取 immutable artifact，不从文档手工抄数字。

## 5. Artifact 契约

JSONL 顺序：

```text
run_metadata
request × N
summary
```

`run_metadata` 至少包含：

- immutable run ID；
- Git SHA 和 image digest；
- Gateway/vLLM/version；
- cluster profile；
- model、请求数、并发、prefix 分布、prompt/output 大小；
- treatment、policy version、threshold；
- random seed 和 treatment order；
- warmup/measurement 标记。

仓库保留 failed attempt 和 rerun。报告若选择某轮，必须给出预先定义的排除原因，不能因为
结果更好而选择 rerun。

## 6. 指标

### 6.1 主要指标

- success/error/timeout rate；
- TTFT P50/P95/P99；
- E2E latency；
- TPOT/ITL；
- request throughput 和 output token throughput；
- backend distribution、spillover ratio、fallback ratio；
- cache hit tokens/query tokens 的窗口 delta；
- fault detection、traffic removal 和 recovery time。

### 6.2 诊断指标

- per-backend waiting/running；
- local in-flight 和 error EWMA；
- KV cache usage；
- EndpointSlice freshness/resourceVersion；
- node-level GPU utilization、memory、temperature、XID；
- Pod restart 和 NodeReady transitions。

节点 GPU 指标不得写成 backend A/B 各自的利用率。

## 7. 统计和结论门槛

- 报告每轮分布和跨轮中位数，不只报告表现最好的一轮；
- 对主要差异给出 bootstrap 95% interval 或等价不确定性区间；
- 同时报告绝对差值和相对变化；
- 性能改善不能以更高失败率为代价；
- 若 P50 改善但 P99/失败率恶化，结论必须描述 trade-off；
- saturation/failure treatment 不与正常稳态 treatment 混合；
- 单 GPU profile 的结论限定为该硬件和版本。

Bounded affinity 的 MVP 验收条件：

1. hot workload 下相对 least-loaded 保留可重复的 locality 收益；
2. skew/saturation 下相对 pure affinity 显著降低 P99 或失败率；
3. stale/no-endpoint 场景在配置 TTL 内停止直接 endpoint selection；
4. 恢复后无需重启 Gateway 即回到正常 selection；
5. 所有决策能由 reason/metrics/artifact 解释。

## 8. 历史 2026-08-08 数据处理

历史结果不删除，也不重新包装为严格实验：

- connection matrix 的三个 attempt 全部保留；
- hot prefix 第一次 `196/200` 和 rerun `200/200` 同时保留；
- mixed、saturation 和 endpoint failure 从仍存活的 Job 恢复原始日志；
- 恢复 artifact 标记 `partial-historical-recovery`；
- 历史表格只支持后续方向选择，不进入最终 benchmark headline。
