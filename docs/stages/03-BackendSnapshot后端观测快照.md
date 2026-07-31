# 03｜Backend Snapshot 后端观测快照

## 目标

把动态 EndpointSlice 地址和每个 vLLM 副本的真实指标放进同一个结构化快照，为后续
bounded affinity scheduler 和慢速证据路径提供输入，同时明确 freshness 和采集错误，避免把“没有数据”当成健康的
零负载。

## 为什么先做 vLLM，不直接做 GPU

vLLM `/metrics` 可以通过 EndpointSlice 地址逐 Pod 读取，因此 backend ID 与 queue、running、
Prefix Cache、TTFT 能一一对应。当前 GPU exporter 是节点级指标，Kubernetes Events 是
namespace 级指标；在没有 Pod→Node/GPU 的明确映射前，把它们复制到每个 backend 会制造
错误证据。本阶段只实现正确的 vLLM 对齐，GPU/Kubernetes 作为后续聚合信号。

## 实现

- `internal/serving/routing.BackendObservation` 定义稳定输入契约；
- `internal/serving/observation.Service` 后台按 EndpointSlice 快照采集，替换已删除 backend，
  并用 `MaxAge` 将过期 `ok` 状态降级为 `degraded`；
- `PrometheusCollector` 读取每个 backend 的 `/metrics`，采集 queue、running、Prefix
  Cache 命中率、TTFT P95 和 vLLM GPU cache usage；
- Gateway `/metrics` 暴露 backend ID、status、freshness、queue、running gauges；
- 默认配置为 `none`，实验入口为 `deploy/experiments/backend-snapshot`，不会改变当前路由策略。

## 验证

K3s 实测中，两个 EndpointSlice backend 均返回 `backend_observation_status{status="ok"} 1`，
独立 backend ID，freshness 小于 1 秒，queue/running 来自对应 vLLM `/metrics`。
验证后已恢复 baseline、关闭 token 自动挂载并删除实验 RBAC。

## 当前边界与下一步

快照目前还没有参与调度决策，Prefix Affinity 仍保持确定性；后续阶段已补充 Pod/Node
身份映射和 EndpointSlice/API 断连门控。P0 复盘后，只有短 TTL 的 queue/running 会作为
spillover 输入；累计 TTFT、Prefix Cache 和 node GPU 数据进入慢速评估，不直接线性加权。
