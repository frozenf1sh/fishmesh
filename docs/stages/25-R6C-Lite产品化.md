# 阶段 25：R6C Lite 产品化

> 日期：2026-08-12
> 类型：Lite 安装面、资源边界与真实集群验收
> 状态：已完成

## 1. 本阶段契约与不变量

1. `deploy/lite-kv-aware` 是一条 `kubectl apply -k` 可安装的产品 overlay；它只组合 Gateway、vLLM 和运行所需 Kubernetes 资源，冻结的 analyst、simulator、loadgen 不进入镜像或 overlay，源码不在本阶段物理删除；
2. 默认基础配置仍为 `session-key`。Lite overlay 显式启用 `kv-aware`，unknown/stale 继续走 `kv-aware-load-fallback-v1`，不能伪装为 zero miss；
3. 默认 Dockerfile 只产生 `fishmesh-gateway`，离线 amd64 导入脚本使用固定 `fishmesh-gateway:<tag>` 与 `imagePullPolicy: Never`；
4. Gateway 是 non-root、只读根文件系统、drop all capabilities，具有 liveness/readiness、CPU/内存 requests/limits、rolling update 和 PDB；其专用 SA 只可对当前 namespace EndpointSlice 执行 `get/list/watch`，不得读取 Pods 或 Secrets；
5. 指标不得包含 routing key、prompt、token IDs、Pod UID 或 ZMQ endpoint。只投影有界 backend ID、KV-aware 状态、typed reason、KV freshness、已提交 sequence、applied/replay batches 与进程 CPU/RSS；
6. Gateway logger 在进程入口以 `ctrllog.SetLogger(logr.Discard())` 初始化 llm-d 的 controller-runtime 依赖。它只抑制已知的第三方空 logger stack，不改变 KV event、sequence 或 routing 行为；
7. Pod 删除期间已知信号或可用实例的真实 zero miss 必须由不同的 headers/metrics 表达；SSE 仍原样结束于 `[DONE]`。

## 2. 实现

- 新增 `deploy/lite-kv-aware`：explicit KV-aware 配置、EndpointSlice reader Role/RoleBinding、Gateway PDB、vLLM KVEvents/replay 端口和 `POD_IP` topic；当前 Flannel 不执行 NetworkPolicy，因此策略以 `security/network-policy.requires-policy-cni.yaml` 保留但不纳入 overlay；
- 基础 ServiceAccount 不再创建冻结 loadgen SA；当前集群遗留 analyst Deployment、Service、ConfigMap、SA/RBAC 与旧 loadgen SA 已清理；
- Dockerfile 移除 EPP、analyst、loadgen 和 simulator 二进制，`make image` 改走独立 `scripts/build-and-load-gateway-image.sh`；
- requestpath 将 kvcache `StateSnapshot` 投影为不含身份/请求内容的 `KVCacheState`；Gateway metrics 增加 instance validity/freshness/sequence/batches、KV-aware request/degradation counters 和 process collector。同包 contract test 锁定敏感标签不外泄；
- `deploy/lite-kv-aware/README.md` 提供镜像导入、安装、命中/降级对照、指标、声明式滚动升级、Pod 删除和恢复 runbook。

## 3. 真实集群验收

Gateway 新镜像 `fishmesh-gateway:r6c-lite-r1` 已离线导入，OCI manifest 为
`sha256:3a4b46fcf5f0ecb15398f7c6cb77c318b86e7185e6c4c586c7f3e279cd8029e3`。应用 overlay 后，Gateway
与两个 vLLM 副本均经滚动更新 Ready；Gateway PDB `minAvailable: 1`，vLLM PDB `minAvailable: 1`。

RBAC 实测：`list endpointslices = yes`，`list pods = no`，`get secrets = no`。

| 场景 | 关键响应/指标 | 结论 |
| --- | --- | --- |
| 新 Gateway 首请求 | `match-unavailable`、`kv-aware-load-fallback-v1`、`kv-aware-signal-unavailable`、cached `0` | replay 未确认时显式 load-balanced 降级。 |
| 同 system prompt、不同 user message、无 session key 的第二请求 | `available`、`kv-aware-v1`、`kv-aware`、cached `80`、`data: [DONE]` | 真实 Render → KV lookup → KV-aware routing → SSE 命中闭环。 |
| 删除实际命中的 `10.42.1.170` vLLM Pod | 被删除 backend 立即从 KV metrics 消失；剩余 `Valid=1` backend 返回 `available`、cached `0`、`data: [DONE]` | ZMQ stream 随 Pod 消失，真实 zero miss 仍是 KV-aware，不误报为 unknown。 |
| 替代 Pod Ready/replay 的首请求 | `match-unavailable`、`kv-aware-load-fallback-v1`、cached `0`，同时两个 instance metrics 均 `Valid=1` | EndpointSlice/replay 发布过渡仍按明确降级处理。 |
| 稳定后的下一请求 | `available`、`kv-aware-v1`、cached `80`、`data: [DONE]`；两个 instance 均 `Valid=1, reason=ready` | subscriber/replay 与 KV-aware cache routing 已恢复。 |

修复后 Gateway logs 在上述 live event 与恢复窗口内未再出现 `log.SetLogger(...) was never called`。本阶段遇到此类已知、可由一行初始化消除的依赖噪声，直接修复并记录；真正不确定的架构/行为问题才暂停。

## 4. 验证

```text
go test -race ./...  PASS
go vet ./...         PASS
go build ./...       PASS
make manifest        PASS
```

完整 `go test -race ./...` 的输出尾部随本提交报告，不能以单一“通过”替代。

## 5. 下一阶段

R6D 在同一环境有限对比 Service、FishMesh load-balanced、FishMesh KV-aware 与 llm-d precise；不扩大 workload 矩阵，也不把 Flannel 上未执行的 NetworkPolicy 宣称为已生效。
