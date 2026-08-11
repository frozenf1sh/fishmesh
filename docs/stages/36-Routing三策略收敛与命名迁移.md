# 阶段 36：Routing 三策略收敛与命名迁移

## 目标

把 routing 子包收敛为三个有明确用户价值、可直接配置的模式，并让实现、请求协议、环境变量、
部署 overlay、llm-d 参数和文档使用同一套名称：

| 模式 | 语义 | 策略标识 |
| --- | --- | --- |
| `load-balanced` | 按 Gateway 视角的在途负载做普通负载均衡 | `load-balanced-v1` |
| `session-key` | 使用客户端传入的 key 建立有界粘性，压力或熔断时允许溢出 | `session-key-v1` |
| `kv-aware` | 使用真实 KV locality，并把已知 queue/running/in-flight 折算为等价成本 | `kv-aware-v1` |

## 变更

- routing 实现文件、构造函数、配置类型、reason/policy 常量统一为三种模式的命名；
- 删除纯 prefix hash 选路和独立 Service 选路实现；Service 仅作为 discovery、circuit 或策略失败时的
  最终 fallback，不再作为用户可选 routing mode；
- `session-key` 使用 `X-FishMesh-Session-Key`，KV 请求状态使用 `X-FishMesh-KV-Status`；
- session-key 与 KV-aware 的环境变量、Prometheus 指标、llm-d EPP 参数、Gateway 配置 annotation 和
  实验 overlay 全部完成迁移；
- 默认模式改为 `load-balanced`，Lite KV-aware overlay 和 session-key 实验 overlay 保持显式启用；
- 删除旧的 prefix endpoint 兼容入口，避免已经移除的策略概念继续成为配置契约。

## 验证

本阶段修改后应通过仓库门禁：

- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `make manifest`

并额外检查 `rg` 不再命中已删除策略的实现名、旧部署目录、旧 header/env/metric 名称；工作区不提交
用户自有的未跟踪材料。

## 边界

本阶段只改变 routing 模式收敛和命名，不改变 KV index、Render、KVEvents、SSE 生命周期或硬过载语义。
KV 信号未知/过期时，`kv-aware` 仍显式降级为 `load-balanced`，不能把未知状态解释为零 token 命中。
