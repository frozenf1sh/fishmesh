# Connection matrix

这组清单用于回答“keep-alive 是否已经足够替代 prefix-aware routing”。

Gateway 配置与 loadgen 配置分别控制两条连接：

- Gateway 的 `FISHMESH_UPSTREAM_KEEPALIVE` 控制 Gateway → vLLM；
- Job 的 `--keep-alive` 控制 loadgen → Gateway。

`gateway-session-key-keepalive.yaml` 使用当前 Ready Pod IP 快照，仅用于本轮实验；Pod
重建后必须刷新 Endpoint IP。所有 Job 使用相同的 8 个 prefix group、4096-byte prefix、
并发 4 和 32-token 上限。Loadgen 还支持 `--hot-prefix-ratio`：例如 75 表示 75% 请求
进入 group 0，用于可复现的热点和混合负载实验。输出应保存为 JSONL，按 `prefix_group`
分析首个请求和后续请求。

`load-balanced` 是当前的实验原型：它只根据 Gateway 自身的 endpoint in-flight 数选择
后端，不等同于 GPU-aware 调度。正式实现应替换静态 endpoint 为 EndpointSlice，并接入
vLLM queue/running、Prefix Cache、TTFT 和 GPU 指标。
