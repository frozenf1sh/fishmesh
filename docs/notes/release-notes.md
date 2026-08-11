# Lite 发布、兼容性与回滚说明

## 发布边界

本说明覆盖 Lite Gateway 镜像及其 Kustomize 配置；R6E Standard mode（Gateway API、InferencePool、
EPP/llm-d 的产品安装面）明确后置，不在本发布流程中。

当前参考环境的已验证路径是 macOS arm64 构建机交叉构建 `linux/amd64` Gateway，再离线导入
GPU 节点 K3s containerd。Dockerfile 使用 `BUILDPLATFORM`、`TARGETOS` 和 `TARGETARCH`，因此可作为
多架构构建输入；但 Linux arm64 Gateway 运行、远程 registry multi-arch manifest 和 SBOM
attestation **尚未在真实环境验证或发布**。以下命令是发布前必须执行的流程，不是已有 release 的声明。

## 版本矩阵

| 组件 | 固定/当前版本 | 状态与边界 |
| --- | --- | --- |
| Go | `1.26` | 本地与 CI 编译、test/vet/build 门禁 |
| Gateway | `fishmesh-gateway:r6d2-r1` | Linux amd64 离线导入并在 GPU 节点运行；tag 不是不可变身份 |
| Gateway 构建目标 | `linux/amd64`、`linux/arm64` | Dockerfile 支持交叉编译；仅 amd64 有运行验收 |
| K3s | `v1.36.3+k3s1` | 参考集群已验证 |
| vLLM | `0.23.0` | Qwen2.5-0.5B-Instruct、KVEvents/replay 已验证 |
| GPU/CNI | 一块 time-sliced RTX 4060 Laptop GPU / Flannel | 非独立 GPU 故障域；Flannel 不保证 NetworkPolicy 执行 |
| llm-d Router adapter | `0.9.0` | 本地 adapter/contract 存在；R6E 产品部署后置 |

发布记录必须同时保存 Git commit、镜像 **manifest digest**、每个架构的 image digest、SBOM digest、
构建器版本和此矩阵。不要把可变 tag 当成回滚依据。

## 多架构构建与 SBOM

### 已验证的离线 amd64 路径

这条路径供当前 GPU 节点使用，保持现有 `imagePullPolicy: Never` 语义：

```bash
make image VERSION=<release-tag>
ssh ubuntu 'sudo /opt/kubellm/bin/k3s ctr -n k8s.io images ls | grep fishmesh-gateway'
```

`make image` 只构建/导入 amd64 OCI archive，不生成 remote manifest 或 SBOM。导入后记录
`k3s ctr images ls` 中的 digest；仅在确认节点实际拥有该 image 后才更新 Lite overlay 的 tag 和
`fishmesh.io/gateway-image-revision` 注解。

### 发布前的远程多架构流程（尚未验证）

在拥有可写 registry、已登录的 Buildx builder 和 `syft` 的环境中，先把变量替换为真实值：

```bash
export RELEASE_IMAGE=registry.example/fishmesh-gateway
export RELEASE_TAG=<semver-or-date-tag>

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag "${RELEASE_IMAGE}:${RELEASE_TAG}" \
  --provenance=true --sbom=true --push .

docker buildx imagetools inspect "${RELEASE_IMAGE}:${RELEASE_TAG}"
syft "${RELEASE_IMAGE}:${RELEASE_TAG}" -o spdx-json > "fishmesh-gateway-${RELEASE_TAG}.spdx.json"
sha256sum "fishmesh-gateway-${RELEASE_TAG}.spdx.json"
```

发布人必须确认输出含 amd64 与 arm64 两个 manifest，并将 `imagetools inspect` 的 manifest digest、
SPDX 文件 SHA-256 及 attestation 引用写入 release record。SBOM 应列出 Gateway 运行镜像及 Go 模块；
它不能替代对 vLLM、K3s 或节点预加载镜像的库存审计。若 `syft` 或 registry attestation 不可用，
该发布不具备完整 SBOM 交付条件，应标记为未发布而不是补造摘要。

## Lite 升级

升级前先做以下只读检查，保留输出到变更记录：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get deployment,pod,pdb
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get endpointslice
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get configmap fishmesh-gateway-config -o yaml
```

1. 按目标节点架构导入目标 Gateway 镜像，并记录 digest；当前 GPU 节点只能使用已验证的 amd64
   离线导入路径。
2. 在一个受评审的 release 变更中，同时更新 `deploy/lite-kv-aware/kustomization.yaml` 的 Gateway
   image、`fishmesh.io/gateway-image-revision` 和 release record；不可只改 tag 而漏掉 revision。
3. 先运行 `make ci` 与 `git diff --check`，再 `kubectl apply -k deploy/lite-kv-aware`。
4. 等待 `fishmesh-gateway` rollout；它的 PDB 为 `minAvailable: 1`，并使用
   `maxSurge: 1` / `maxUnavailable: 0`。不要并行重启两个 time-sliced vLLM 副本。
5. 检查 `/readyz`、一条 SSE 请求、`X-FishMesh-*` 决策头及 KV freshness。unknown/stale 必须保留
   load-balanced 降级，不能通过关闭 KV-aware 或把 unavailable 改写为零命中来“恢复”。
6. 观察 GPU 温度；持续超过 80°C 或 `gpu-watchdog` WARN/CRITICAL 时，停止升级/负载并等待低于 70°C。

Flannel 环境不得把 NetworkPolicy 当作本升级过程已生效的隔离控制；若改用可执行 NetworkPolicy 的
CNI，先单独验证 DNS、HTTP、ZMQ 5557 和 replay 5558。

## 回滚与失败处置

若 Gateway image rollout 或 request-path smoke 失败，先停止继续变更、保留 Pod events、Gateway 日志、
决策头和 KV 指标，再执行最小回滚：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout undo deployment/fishmesh-gateway
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deployment/fishmesh-gateway --timeout=5m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get pod -l app.kubernetes.io/name=fishmesh-gateway
```

若问题来自 KV-aware/KVEvents 链路而非镜像本身，恢复受控基线，而不是试图把无效 cache index 继续用于
KV-aware 路由：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/baseline/base
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deployment/fishmesh-gateway --timeout=5m
```

`rollout undo` 回退 Deployment revision；配置、镜像预加载状态和 vLLM cache 不会自动倒回。完成后重新
检查 ConfigMap、实际 image digest、`session-key` 路由模式和 `/readyz`。需要回退 vLLM 时，遵守其
`maxSurge: 0` / `maxUnavailable: 1` 约束，并将其视为 cache-cold：在 replay/valid 重新成立前，只允许
load-balanced 降级。
