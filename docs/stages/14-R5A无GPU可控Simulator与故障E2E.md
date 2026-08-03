# 14｜R5A 无 GPU 可控 Simulator 与故障 E2E

状态：代码、进程级 E2E 和本地验证完成；没有使用 GPU/K3s，也没有产生性能结论。

## 这一阶段解决什么问题

过去的故障测试主要依赖函数替身，真实 K3s 又依赖暂时不可用的 GPU 节点。两者之间缺少一个
稳定层：能够走真实 HTTP/SSE、连接取消、EndpointSlice TLS 和 Prometheus 解析，但不执行模型
推理。没有这一层，slow、stream failure、discovery stale 等状态只能零散验证，也很难进入 CI。

R5A 新增独立的 controlled backend simulator。它不是新的模型服务器，更不是性能基准；它只
提供确定性的协议输入和故障，用来验证 FishMesh 请求路径的不变量。

## Simulator 提供什么

`internal/simulator` 只依赖 Go 标准库，提供：

- `/v1/chat/completions`：OpenAI-compatible SSE，可设置首事件延迟、事件间隔和事件数量；
- `/v1/models` 与 `/healthz`：最小兼容接口；
- `/metrics`：可控的 vLLM queue/running Prometheus 指标；
- `PUT /control/behavior`：为后续请求设置 HTTP status、stream abort、held stream 和观测值；
- `GET /control/state`：查看请求、active、断开、强制错误和流中断计数。

行为更新使用 mutex 原子替换；请求计数使用 atomic。每个已经开始的请求持有自己的行为副本，
所以控制面更新不会让半条 SSE 流突然改变语义。`hold` 会发送 SSE comment heartbeat，使 HTTP/1
代理能够及时发现 downstream 已断开，而不会伪造额外 token。

`cmd/fishmesh-simulator` 只负责 flag、监听、信号和优雅关闭。Docker 镜像也包含该二进制。控制
API 没有认证，因为它只允许用于本地和 CI；禁止把它暴露到生产网络。

## 自动 E2E 覆盖什么

测试使用真实 `httptest` HTTP/TLS server 和真正的 Gateway 组合根，不直接调用 Gateway 私有函数：

1. 首 SSE 事件延迟会经过 Gateway 保留；
2. 一个 held stream 占满 admission 后，第二个请求立即得到 429；
3. client cancellation 会释放 simulator/Gateway in-flight，且不会打开 backend circuit；
4. response headers 后的 upstream stream abort 不会重试，并会打开 circuit；
5. circuit 打开后下一请求使用带 `circuit-fallback` reason 的 Service fallback；
6. TLS EndpointSlice fixture 移除 backend 后，旧 backend 不再收到请求；
7. Kubernetes API 失联超过 freshness 界限后，readiness 返回 503，请求使用
   `discovery-fallback`；
8. observation Prometheus collector 能把 simulator queue/running 解析为有效 sample。

这些是正确性和故障生命周期测试，不测 token 吞吐量，也不模拟真实 KV cache 或 GPU 执行。

## 一个真实发现

最初的 `hold` 只发送 response headers 然后等待取消。Go HTTP server 会把尚无 body 的响应视为
零长度响应，导致 Gateway 正常结束并释放 admission，而 simulator handler 仍在等待。这不是
admission bug，而是测试协议不真实。

最终 `hold` 先发送一个合法 SSE 事件，再周期发送 comment heartbeat。heartbeat 不算 token，
但能让代理在连接断开后的下一次写入发现错误，从而取消 upstream、完成 lease 并释放 permit。
这个过程说明 simulator 的价值在于暴露真实协议生命周期，而不是制造更多 mock。

## 代码边界

- simulator 不 import Serving、Gateway 或 Kubernetes DTO；架构门禁会自动检查；
- Gateway 只在测试中 import simulator，生产依赖图没有反向依赖；
- composition 保留显式注入的 Kubernetes HTTP client，未注入时才使用默认 client；
- 所有生产文件小于 300 行，函数小于 40 行；关键生命周期使用中文注释。

## 验证

- simulator contract tests；
- Gateway slow/error/overload/cancellation fault E2E；
- EndpointSlice removal/stale TLS E2E；
- observation compatibility test；
- `make ci` 完整通过。

本阶段没有连接 `~/.kube/fishmesh.yaml`，也没有启动或修改 GPU 节点。

## 下一步

执行 R5B EPP/llm-d integration spike：只调研并记录当前上游协议、插件边界、版本约束和失败
模式，先形成 ADR，再选择 integrated runtime path。不要在 spike 中复制 Gateway proxy，也不要
在没有 integrated adapter 时提前制造一套形式化接口。Simulator 的长时间 churn/soak 可随后
作为独立可靠性提交。
