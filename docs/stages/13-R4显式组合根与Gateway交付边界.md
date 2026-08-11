# 13｜R4 显式组合根与 Gateway 交付边界

状态：代码迁移和本地验证完成；环境变量、HTTP/SSE 协议、指标名称和调度语义保持不变。

## 这一阶段解决什么问题

R3 已把一次请求如何选择 backend 提取为 `requestpath`，但旧 `gateway.NewServer` 仍在内部读取一份
大配置并创建 discovery、observation、routing、circuit 和 transport。表面上 Gateway 是 delivery
层，实际上却是一个隐藏的组合根：阅读 HTTP handler 时也必须理解 Kubernetes、Prometheus 和调度
实现，其他入口也无法复用同一套能力。

R4 将进程装配移到 `cmd/fishmesh-gateway`，让依赖关系直接显示在代码中。Gateway 只接收已经创建
好的能力，不再决定使用哪一种 resolver、collector 或 strategy。

## 新增的两个边界

### Admission

`internal/serving/admission` 提供非阻塞的并发准入：

- 有容量时返回一个 permit；
- 容量已满时立即返回 typed error，不排队隐藏延迟；
- permit 释放是幂等的，重复释放不会破坏计数。

它不知道 HTTP、backend 或 routing，因此 standalone Gateway 和未来 integrated adapter 都能复用。

### Config

`internal/serving/config` 是进程配置入口。所有 `FISHMESH_*` 环境变量名和默认值集中在 protocol
文件中，加载后转换为各 owner domain 的小 Config，例如 `discovery.Config`、`routing.Config` 和
`transport.Config`。

这样 routing 阈值不会继续混进 Gateway 配置，配置错误也会在创建运行时之前暴露。

## 显式组合根

`cmd/fishmesh-gateway/composition.go` 按依赖方向完成四件事：

1. 创建 discovery，以及可选的 observation 和 identity；
2. 创建 routing、circuit、transport、admission 等原子能力；
3. 注入并创建 requestpath，最后创建 Gateway delivery；
4. 记录拥有后台任务或连接池的资源，并在退出时按依赖反序关闭。

EndpointSlice discovery 和 Kubernetes identity 共用一个显式配置的 HTTP client。关闭时先停止仍会
使用下游能力的 requestpath/observation/resolver，再关闭 transport；关闭函数本身也可重复调用。

## Gateway 现在负责什么

Gateway 文件按阅读角色拆分：

- `gateway.go`：配置、依赖和公开契约；
- `gateway_impl.go`：health、ready、metrics 和 `/v1/*` handler；
- `proxy_impl.go`：准入、取得 lease、流式转发、完成 lease；
- `metrics_impl.go`：HTTP 层指标投影；
- `protocol.go` / `metrics_protocol.go`：稳定的路径、header、metric 和 label 字面量。

主代理流程只保留四个步骤。它不创建 Kubernetes client，不解析 Prometheus 响应，也不实现选择
算法。response headers 发出后不会偷偷重试；client cancellation、upstream stream failure 和
downstream write failure仍会被分类为不同 outcome。

## 保持不变的兼容性边界

- 现有 `FISHMESH_*` 环境变量名和默认值；
- `/healthz`、`/readyz`、`/metrics` 和 `/v1/*` 路径；
- FishMesh response header、route reason 和 fallback 语义；
- Prometheus metric/label 名称；
- SSE 逐块转发，以及 response headers 后不重试；
- session-key、circuit 和 membership 行为。

R4 是职责搬迁，不借机修改调度算法或制造性能数字。

## 验证

- admission 覆盖容量耗尽和幂等释放；
- config 覆盖 EndpointSlice、非法值、session-key 映射和必需 Service；
- composition test 从静态配置创建完整 runtime，并代理真实 `httptest` upstream；
- Gateway 原有 HTTP/SSE、取消、circuit spillover 和 session-key 测试继续通过；
- import architecture test 阻止 Gateway 重新依赖 Kubernetes DTO 或 Prometheus parser；
- `make ci` 完整通过。

本阶段不依赖 GPU/K3s。GPU 节点暂停期间没有执行集群 smoke，也没有据此声称集群部署已验证。

## 下一步

进入 R5：建立一个可控 simulator，用标准 discovery、observation 和 upstream fault contract 覆盖
slow/error/removed backend、stale、overload 与 cancellation；随后用同一组 requestpath conformance
测试验证 standalone Gateway 和 EPP/llm-d adapter。R5 不把 simulator 逻辑放回 Gateway。
