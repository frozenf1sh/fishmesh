# 02｜EndpointSlice 动态发现

## 目标

移除实验中手工维护 Pod IP 的依赖，让 Gateway 从 Kubernetes EndpointSlice 获取动态 Ready
后端，同时保留默认 Service baseline 和安全回退。

## 实现

- `internal/serving/discovery` 提供 Static 与 EndpointSlice 两种 Resolver；
- namespace-scoped `get/list/watch endpointslices`；
- 仅选择 Ready 的 IPv4/IPv6 地址和有效端口；
- 根据地址和端口生成稳定 `endpoint-*` ID；
- watch 断开后重新 list，快照由锁保护；
- Gateway 动态解析 backend URL，并为动态 backend 建立 in-flight counter；
- 实验 overlay 使用最小 RBAC，默认 baseline 不挂载 ServiceAccount token。

## 验证

真实 K3s 中发现两个 vLLM Ready 地址，`/v1/models` 返回：

- `X-FishMesh-Routing-Mode: session-key`；
- `X-FishMesh-Backend-ID: endpoint-*`；
- `X-FishMesh-Upstream` 指向某个实际 Pod 地址。

验证后已恢复 Service + keep-alive baseline，并删除实验 RBAC。

## 边界

EndpointSlice 只告诉我们“地址可用”。后续 Backend Snapshot 已对齐 vLLM queue/running；
Prefix Cache/TTFT 用于慢速评估，node GPU 信号不伪装成 per-backend 指标。
