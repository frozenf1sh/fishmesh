# 阶段 18：R6A 真实 KV 信号闭环

> 日期：2026-08-11
> 类型：真实集群数据源门禁
> 结论：通过，可以进入 R6B 生产能力域
> 产品行为：尚未接入 Gateway 请求路径
> 集群状态：实验后恢复 `deploy/inference` 基础清单

## 1. 本阶段要回答什么

阶段 17 选择“Lite Gateway + 真实 KV cache”以后，最危险的做法是先写大量接口，再假设 vLLM
事件一定可靠。本阶段只回答七个工程问题：

1. vLLM 0.23.0 是否真的提供 Render token、实时 KVEvents 和 replay；
2. 上游 `llm-d-kv-cache` 能否从真实事件重建逐 Pod block locality；
3. 不同会话共享 system prompt 时能否得到非零公共 prefix；
4. `BlockRemoved` 是否会真实删除索引命中；
5. Pod 重建后旧实例 locality 能否按生命周期清理；
6. subscriber 断开时能否先失效，再通过 replay 恢复；
7. 这条链路的延迟和内存是否允许继续做轻量 Gateway。

本阶段不修改 Gateway，不实现路由公式，不扩展 simulator，也不把实验探针放入产品镜像。

## 2. 实现了什么

### 2.1 声明式 vLLM overlay

新增 `deploy/experiments/exact-kv-signal`，在基础 vLLM Deployment 上增加：

```text
--kv-events-config={
  enable_kv_cache_events: true,
  publisher: zmq,
  endpoint: tcp://*:5557,
  replay_endpoint: tcp://*:5558,
  buffer_steps: 4096,
  hwm: 10000,
  max_queue_size: 10000,
  topic: kv@<PodIP>:8000@qwen2.5-0.5b-instruct
}
```

Pod IP 来自 Downward API。实时和 replay 端口只在实验 Pod 中声明，没有增加公网入口或共享
Service。replay、publisher queue 和探针索引都有显式上限。

### 2.2 最小真实探针

新增 `hack/r6a-kv-signal`，职责刻意受限：

- 调用 `/v1/chat/completions/render` 获取与模型 chat template 一致的 Token IDs；
- 复用 `llm-d-kv-cache` v0.9.0 的 vLLM adapter、token processor、KV index 和 prefix scorer；
- 订阅每个 Pod 的实时 PUB，并从 replay ROUTER 补偿断流；
- 暴露本地 `/state`、`/generate`、`/score`、`/stream`、`/clear` 控制端点；
- 记录序列、event 类型、replay、freshness、Render/lookup latency 和 Go heap。

控制面只监听 GPU 节点 `127.0.0.1:19090`。Linux amd64 二进制由本地交叉编译后临时复制到
GPU 节点 `/tmp`，没有创建长期 Deployment，也没有进入 FishMesh release image。

### 2.3 依赖版本

项目把直接使用的 `llm-d-kv-cache` 从 `v0.9.0-rc.1` 升到正式版 `v0.9.0`。llm-d Router 仍固定
为 `v0.9.0`，本阶段没有顺便升级 Standard mode runtime。

## 3. 一个关键的上游缺口

只调用 `kvevents.SubscriberManager` 不足以形成生产 exact 信号。固定的上游版本存在三个边界：

1. 接收并携带 sequence，但不检查 sequence gap；
2. 没有调用 vLLM replay endpoint；
3. Pod reconciler 示例在 Pod 删除时只移除 subscriber，不清理该 Pod 已写入的 index entry。

这不意味着应该自研 vLLM parser/indexer。探针继续复用上游协议解析和索引，只在 transport/lifecycle
边界增加以下语义：

- 周期 replay 请求同时作为补偿和 liveness heartbeat；
- subscriber 被暂停时立即 `valid=false`；
- 从最后连续序列的下一位 replay，完整补回后恢复 valid；
- buffer 已覆盖缺口时清理该 Pod 并保持 invalid，禁止使用部分索引；
- Pod 删除通知按所属实例清理 index。

尤其不能用“最后一条业务事件距今多久”判断 stale：空闲且健康的 vLLM 不会持续发送 KV event。
replay END 响应才是当前方案可用的主动链路健康证据。

## 4. 真实集群步骤和结果

固定环境：K3s `v1.36.3+k3s1`、vLLM `0.23.0`、Qwen2.5-0.5B-Instruct、两个 time-sliced
vLLM Pod、单块 RTX 4060。该环境用于信号正确性，不代表两个物理 GPU failure domain。

### 4.1 Render 与初始状态

两个新 Pod 均 Ready，实时端口和 replay 端口可达。没有请求时，两条流也能通过 replay END
心跳变为 valid；这证明 idle 不会被误报为 stale。

第一次 B 请求只调用 Render/score，不生成：

```text
prompt tokens     149
Pod .137 match      0
Pod .138 match      0
Render latency   6.202 ms
lookup latency   0.129 ms
```

补充最小复测中，`BlockStored` 自带 publisher timestamp 到 GPU 节点探针接收的延迟为 `0.678 ms`。
空索引探针 RSS 为 `34044 KiB`，Go `HeapAlloc` 为约 `2.63 MiB`；RSS 包含 Go runtime、上游库和
网络连接，不能只用 HeapAlloc 代替进程资源预算。

### 4.2 跨会话公共 system prompt

A/B 使用相同的长 system prompt，但 user message 不同，也没有使用 FishMesh session key。A 定向在
Pod `.137` 完成后，B 的结果是：

```text
prompt tokens       149
Pod .137 match         8 blocks / 128 tokens
Pod .138 match         0 blocks /   0 tokens
Render latency      5.046 ms
lookup latency      0.139 ms
observed event      BlockStored
```

因此命中来自真实 token block，而不是客户端键、累计 hit rate 或连接亲和。

### 4.3 断流、失效与 replay

暂停 `.137` 订阅后，状态立即变为：

```text
valid=false, invalid_reason=subscriber-disabled
```

断流期间向该 Pod 发送另一组 123-token prompt，随后恢复订阅。探针从 sequence 1 replay：

```text
replay_batches      1
last_sequence       1
matched prefix      7 blocks / 112 tokens
valid               true
```

这验证了“先明确失效，再恢复 exact”，而不是断流期间继续使用旧索引。

### 4.4 真实 eviction

vLLM 报告 `num_gpu_blocks=4783`、block size 16。连续发送 36 个约 2.4k-token 的独立提示制造缓存
压力后，探针观察到：

```text
BlockRemoved        3105（压力步骤结束时）
旧公共前缀          128 tokens -> 0 tokens
HeapAlloc           约 2.31 MiB -> 11.67 MiB
HeapSys             约 23.7 MiB（压力步骤结束时）
索引容量            100000 keys，8 Pod entries/key
```

后续重建前累计观察到 3111 个 removed。旧公共前缀归零证明 index 确实消费了 engine eviction，
不是只增加不删除的近似表。

### 4.5 Pod 重建

重建前，用专用 prompt 再次确认旧 Pod：

```text
Pod IP              10.42.1.137
Pod UID             3607133e-0308-429c-be63-88453aaf3ce2
matched prefix      5 blocks / 80 tokens
```

删除 Pod 并把 lifecycle 删除通知交给探针后：

```text
旧 backend match    0
valid               false
invalid_reason      pod-lifecycle-cleared
```

Deployment 创建的新实例为：

```text
Pod IP              10.42.1.139
Pod UID             acc7ad9b-c692-4a6f-ace4-f4bd03b58434
first sequence      0
first event         BlockStored
```

新实例没有继承旧 locality。R6B 必须让 Kubernetes lifecycle owner 自动完成这条 UID→topic/index
清理事务；不能只用可能被复用的 Pod IP 当永久身份。

## 5. Go / No-go 结论

ADR-002 的 R6A 数据源门禁通过，可以进入 R6B，依据是：

- 两个 vLLM Pod 都能发布、停止发布并响应 replay；
- Render Token IDs 与上游 KV index 能形成逐请求、逐 Pod match；
- 不同会话共享 system prompt 得到 128-token 真实公共前缀；
- removed event 后旧 match 归零；
- Pod UID 变化后旧归属可清除，新 Pod 从独立 sequence 0 开始；
- subscriber 暂停时 exact invalid，恢复后 replay 可补偿；
- 单次 Render 约 5–6 ms，lookup 约 0.08–0.14 ms，当前规模的内存仍很小且有界。
- 一次最小事件复测的 publisher→consumer lag 为 0.678 ms，空索引探针 RSS 约 33.2 MiB。

这只是数据链路通过，不代表 exact routing 已经交付。当前 Gateway 仍运行 bounded affinity，默认
vLLM 清单也没有长期打开 KVEvents。

## 6. R6B 必须继承的约束

1. `tokenization` 单独拥有 Render timeout、请求/响应上限、取消和 typed error；
2. `kvcache` 单独拥有 replay heartbeat、sequence、freshness、容量和 Pod UID 生命周期；
3. 上游 parser/indexer 继续放在 adapter 内，不把其类型泄漏给 routing；
4. initial replay 如果从 sequence 0 已无法覆盖，信号保持 invalid，不能用部分 buffer 冒充完整状态；
5. idle event stream 不等于 stale，freshness 以主动 replay 心跳为依据；
6. unknown/stale match 不等于零命中，requestpath 必须显式降级到 load-aware；
7. topic 的 Pod IP 只用于兼容上游索引键，状态 owner 同时持有 Pod UID 并负责映射和回收；
8. 第一批生产提交不实现策略和 Gateway，先按规范完成 tokenization 契约和值对象。

## 7. 集群收尾

实验完成后重新应用 `deploy/inference`。两个基础 vLLM 副本重新 Ready，实验 `5557/5558` 端口和
`--kv-events-config` 不再存在于当前运行 Pod；声明式 overlay 保留在仓库，用于以后回归。

## 8. 下一阶段

R6B 不一次性实现整条快路径。第一个独立切片是 **R6B-1 tokenization domain**：

1. 写 `tokenization` 契约、纯值对象、配置和 typed error；
2. 先写正常、超时、取消、响应上限、model mismatch 和不支持 route 的 contract tests；
3. 实现薄 vLLM Render adapter；
4. 不接 Gateway、不读取 Kubernetes、不维护 KV index；
5. 完整 CI、阶段文档、提交并推送后，再进入 kvcache domain。
