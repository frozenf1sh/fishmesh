# 阶段 31：R6F KVEvents 与逐请求观测契约

## 1. 契约与不变量

R6D/R6D2 需要可归因的性能证据，但已有 `kv_cache_freshness_seconds` 仅表示 replay heartbeat
确认后的状态年龄，不能代替单个 KV event 的延迟。本阶段只增加观测，不改变 KV index、routing 或
unknown/stale 的降级语义。

1. **KV event 延迟**定义为 vLLM event batch 的 publisher timestamp 到 Gateway 已同步 `Apply`、并
   提交 sequence 的耗时，名称为 publisher-to-apply lag；它不是 ZMQ 网络 RTT，也会受两端时钟偏差影响。
2. 只有 event batch 有有效 publisher timestamp 且 `Apply` 成功时才记录延迟。没有 timestamp、重复
   sequence、decode/apply 失败、gap 或 replay 失败均不生成零值样本。
3. 成功 event 的来源固定为 `live` 或 `replay`；Prometheus 仅使用稳定 backend ID 和这个两值来源，
   不得使用 Pod UID、Pod IP、topic、prompt、token IDs、sequence 或错误文本作为 label。
4. `available` 的 exact 选择才记录一次最终 backend 的 `cached_prefix_tokens` histogram；其值为零时
   是真实零命中，必须保留。`match-unavailable`、tokenization/lookup failed 和未请求 exact 不记录
   cached-prefix 零样本。
5. header 仍是单请求证据，histogram 是无敏感 label 的聚合证据；两者均不能改变 route decision。
6. `LastEventLag` 继续保留为最新已成功 apply event 的状态快照，供故障诊断；它不能被当作 histogram
   的替代或由 scrape 周期重复观测。

## 2. 交付面

- `kvcache.EventObservation` 是成功 apply 后交给组合根的只读值对象，带 stable backend、来源和
  publisher-to-apply 时长；kvcache 不 import Prometheus/Gateway。
- Gateway `Metrics` 将 callback 投影为 `fishmesh_gateway_kv_event_publish_to_apply_seconds` histogram，
  并将可用 exact 选择投影为 `fishmesh_gateway_exact_cached_prefix_tokens` histogram。
- cmd 组合根是唯一把 kvcache callback 接到 Gateway metrics 的位置；requestpath/routing 保持不认识
  Prometheus。
- Grafana 展示 live/replay P95 与 cached-prefix P50/P95；告警继续依据 validity/freshness/降级，不把
  event lag 误解为 freshness。

## 3. 验收

1. 同包 contract tests 覆盖带 timestamp 成功 apply、无 timestamp、失败 apply、重复 sequence、真实
   available zero miss 与 unavailable 不计样本；
2. Gateway metrics tests 断言 histogram 标签有界，且不泄露 prompt/UID/topic/sequence；
3. 在实际 exact Lite overlay 上以两条 SSE 请求进行低负载验证，检查 Prometheus histogram 和
   dashboard 查询；不运行长时或并行 GPU 压测；
4. 结束后恢复 `bounded-affinity` 基线，执行 `go test -race ./...`、`go vet ./...`、`go build ./...`、
   `make manifest` 与 `git diff --check`。

R6F 完成后，独立的 Go 对话/压测 CLI（R6G）才消费这些稳定的请求头与聚合指标；R6E 不在本阶段范围内。

## 4. 真实集群验收（2026-08-13）

Gateway `r6f-r1` 通过现有 macOS arm64 → Linux amd64 OCI archive 的离线导入路径部署到 GPU 节点；
exact overlay 已有的 KVEvents/replay 配置保持不变。启动日志确认 `routing_mode=exact-cache-load`。最终
`r6f-r2` 仅包含 backend label 回收并作为恢复后的 bounded-affinity Gateway 镜像部署。
以同一个约 3.8KiB system prompt、不同 user message、无 session key 发送两条短 SSE 请求：

| 请求 | Exact status / cached prefix tokens | 结论 |
| --- | --- | --- |
| 首请求 | `available` / `0` | 有效 KV 信号下的真实零命中，记录为 cached-prefix histogram 的零样本。 |
| 第二请求 | `available` / `768` | 同 backend 复用真实完整前缀，记录为非零 cached-prefix 样本。 |

Prometheus 原始 `fishmesh_gateway_exact_cached_prefix_tokens_count/sum` 为 `2/768`。新的 live
`publish_to_apply` histogram 记录 4 个 batch、sum `0.002878708s`；5 分钟 P95 查询约为 `0.00096s`。
两个实例在启动 replay 中也生成独立的 `source="replay"` 样本；这些 event 原始 publisher timestamp
早于当前 Gateway，lag 很大是历史重放年龄，不可与 live delivery 性能混合或据此声明网络延迟。

Grafana 配置新增 live/replay P95 和 cached-prefix P50/P95 两个面板；重新 provision 后 Grafana API
确认 dashboard 含 8 个 panels。GPU 验收前后温度为 40°C/41°C，未出现 `gpu-watchdog` WARN/CRITICAL。
本次仅两条短请求，不是 benchmark；验收结束后恢复 `deploy/experiments/r6d-bounded-affinity`。
