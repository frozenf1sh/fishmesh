# 阶段 33：R6H 成本式 KV-aware 路由

> 状态：本地契约与实现中；GPU 节点关闭期间不部署、不访问 Kubernetes、不做性能结论。

## 1. 用更简单的话说

现在的 KV-aware 会先问“谁已经有更多缓存”，只有缓存数量完全相同时才问“谁更忙”。所以一次冷请求在
平局中落到某个 Pod 后，该 Pod 获得缓存并很容易持续获选。R6H 的目标是改为先把三件事换成同一单位再
比较：还要算多少 token、前面排了多少工作、Gateway 当前已经交给该 Pod 多少请求。缓存仍有价值，但它
不再能无限压过已经很忙的后端。

这不做任意用户内容的主动预热，也不把用户请求复制到另一个 Pod。热门前缀的主动副本预热、远端/分层
KV 和 llm-d Standard mode 是后续独立决策，不混入本阶段。

## 2. 契约与不变量

1. `routing` 继续是纯函数域，只消费 `KVAwareInput`、`Load`、local in-flight 与 routing-owned
   `KVAwareConfig`；不 import tokenization、kvcache、HTTP、Kubernetes 或 Prometheus。
2. 每个候选的估算成本以“等价未缓存 token”表示：
   `uncached_tokens + queue × queue_penalty + running × running_penalty + local_inflight × inflight_penalty`。
   penalty 是显式、非负、版本化的 routing 配置，不从累计 TTFT、GPU 温度或任意权重推断。
3. `Load.Valid=false` 不等于 queue/running 为零：只省略这两个未知项；local in-flight 仍是 Gateway 已知
   的本地事实。unknown/stale KV match 仍由 requestpath 降级，不能进入 KV-aware 成本计算。
4. hard overload 仍先排除，所有候选都 hard-overload 时保持现有 load-balanced fallback；成本模型不能绕过
   可用性保护。总成本使用饱和整数计算，不能因大样本溢出而变成较小成本。
5. 成本相同才使用现有 session hint 作为稳定平局；本切片不承诺“零命中时轮流暖每个 Pod”。该能力需要
   有状态且受资源预算约束的 cold-tie 分发策略，待真实负载数据决定后另立切片。
6. 配置通过环境变量进入组合根；本切片只改代码默认值，不 rollout、不改集群模式。默认参数只是
   保守起点，必须在 GPU 恢复后用受控 profile 校准，不能据此声称性能收益。

## 3. 实施步骤

1. 在 `routing` 定义成本配置和值对象，先以同包 contract tests 锁定 cache/load 取舍、unknown、硬过载、
   平局与整数边界。
2. 将 KV-aware 策略从词典比较替换为饱和成本比较，并把 policy 标为 `kv-aware-v1`；保留 v1 的
   load-balanced degradation policy，避免把 KV unknown 变成 zero match。
3. 在 `config` 收口三个 token-equivalent penalty 环境键；组合根仍只注入已校验的 `routing.Config`。
4. GPU 恢复后才启用新镜像/配置，并按顺序验收：两 Pod KV valid → cold equal-cost → shared-prefix hit →
   人为制造 queue/running 压力 → stale/replay 降级 → session-key 恢复。每项保留 headers、metrics、
   JSONL 与 GPU watchdog 温度证据。

## 4. 非目标与后续判断

- 不复制任意 conversation/system prompt 到多个 Pod；当前两副本共享同一张 time-sliced GPU，复制 prefill
  只会增加竞争，不能形成独立故障域。
- 不把 prompt、Token IDs 或高基数 prefix identity 写入 metrics。
- 若真实数据表明 queue/running 指标不可靠，保留有效 KV 与 local in-flight，但不伪造外部 load；必要时
  回到 `kv-aware-load-fallback-v1` 或 session-key，而不是调大 cache 权重。

## 5. GPU 恢复后的验收门槛

先确认 2/2 Ready、GPU watchdog 未告警和 KV-aware replay valid。以默认并发 4、单一共享前缀和保留失败
attempt 的方式，比较现有 v1 与 v2 的 TTFT P50/P95、stream throughput、per-backend selected count、
cached-prefix histogram、queue/running、Gateway CPU/RSS 与温度峰值。若 v2 不能改善 busy-cache-owner 的
尾延迟且没有明确负载安全收益，不扩大参数面，记录结论并保留可回滚配置。

## 6. 本地验证与暂停点

已完成同包 contract tests：cache owner 在已知 queue penalty 足够大时会让位给较冷后端；unknown load
不会被伪造成 zero；local in-flight 仍参与；hard overload 与 KV unknown 的既有边界保持。环境变量映射和
负 penalty 拒绝也已覆盖。本地完整 Go 门禁通过。GPU 节点关闭期间没有执行 `kubectl`、port-forward、镜像
构建或任何 GPU workload；因此本阶段尚未完成真实验收，不更新部署 image/config，也不声称默认
`512/128/64` penalties 已校准。
