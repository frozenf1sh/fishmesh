# 阶段 37：Serving 默认配置收口

## 目标

把 standalone Serving 的产品默认值收敛到 `internal/serving/config.DefaultConfig()`，避免
domain 包各自维护一份默认阈值，或在构造函数收到零值时静默改变运行行为。

## 变更

- 新增完整的 `config.DefaultConfig()`，统一声明进程、Gateway、discovery、identity、observation、
  Prometheus、三种 routing、circuit、admission、transport、requestpath、tokenization、KV cache
  和 prediction 的默认配置；环境变量解析器只做显式覆盖。
- 移除 routing、tokenization、KV cache、circuit、prediction 的生产 `DefaultConfig`/策略默认工厂；
  session-key、KV-aware 和 adapter 的数值由组合根提供，测试夹具自行声明测试值。
- observation 与 EndpointSlice 不再对零 interval/max-age/refresh interval 静默补产品默认值；
  Prometheus metrics path 和 transport idle connection timeout 也变成显式配置并进入启动校验。
- llm-d 插件 factory 不再为缺失参数填充默认 JSON 参数；集成配置必须显式写出 header、freshness、
  TTL、容量和 spillover 阈值。nil clock/http client 等运行时依赖兜底仍保留，不属于产品默认配置。

## 验证

- `config.DefaultConfig().Validate()` 通过，并新增完整性测试；
- standalone Serving、Gateway 和 llm-d 相关单元测试通过；
- `go test -race ./...` 通过；
- `go vet ./...`、`go build ./...`、`make manifest` 和 `git diff --check` 通过。

## 边界

本阶段不改变三种 routing 的算法语义、KV 状态协议或降级语义；只改变默认值的所有权和缺省配置
的解析路径。独立 hack/实验程序的命令行默认值仍由其自身 CLI 合约管理，不会伪装成 standalone
Serving 的产品配置。
