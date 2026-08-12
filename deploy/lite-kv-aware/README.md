# Lite KV-aware 安装与演示

这个 overlay 是 R6C 的单命令产品安装面：它只部署 Gateway 和 Qwen vLLM，Gateway 使用
`fishmesh-gateway:<tag>` 独立镜像；冻结的 analyst、simulator 和 loadgen 不在其中。基础
`deploy/baseline/base` 默认使用 `load-balanced`；`session-key` 由单独 overlay 提供，只有此 overlay 明确开启
`kv-aware`。

## 安装前提

- 已配置访问目标集群的 `kubectl`，GPU 节点可由 SSH 别名 `ubuntu` 访问；
- GPU 节点已离线导入 `docker.io/vllm/vllm-openai:v0.23.0`，并且 `qwen-models` PVC 有
  `Qwen2.5-0.5B-Instruct`；
- 当前 CNI 是 Flannel。`security/network-policy.requires-policy-cni.yaml` **没有**包含在
  overlay 内；切换至 Cilium/Calico 等可执行 NetworkPolicy 的 CNI 后，先验证 DNS、HTTP、
  ZMQ 5557 和 replay 5558，再单独启用它。

构建、离线导入 amd64 Gateway 镜像并安装：

```bash
make image VERSION=r6c-lite-r1
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/lite-kv-aware
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deployment/qwen-vllm --timeout=25m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deployment/fishmesh-gateway --timeout=5m
```

确认运行面没有冻结模块，且权限只扩大到 namespace 内 EndpointSlice 与 Pod 只读；Pod list
用于把 vLLM queue/running observation 关联到已发现的 backend，不允许读 Secrets 或集群范围资源：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get deployment,pdb
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm auth can-i list endpointslices \
  --as=system:serviceaccount:kubellm:fishmesh-gateway
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm auth can-i list pods \
  --as=system:serviceaccount:kubellm:fishmesh-gateway
```

两个回答都应为 `yes`；`get secrets` 和集群范围读取仍应为 `no`。Gateway 的 PDB `minAvailable: 1` 与
`maxSurge: 1/maxUnavailable: 0` 配合，允许滚动升级同时保护唯一的 Gateway 副本。

## 命中与降级对照

在一个终端建立端口转发：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  port-forward svc/fishmesh-gateway 8080:8080
```

另一个终端写入一段足以跨完整 KV block 的公共 system prompt，并让两个请求不带任何 session
key、仅 user message 不同：

```bash
SYSTEM_PROMPT='FishMesh demo policy: answer concisely, state assumptions, preserve streaming semantics, and never reveal hidden reasoning. This repeated system instruction deliberately exceeds several vLLM KV blocks so a following request can reuse its KV-aware prefix.'

curl -sS -N -D /tmp/fishmesh-first.headers http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  --data "{\"model\":\"qwen2.5-0.5b-instruct\",\"stream\":true,\"max_tokens\":32,\"messages\":[{\"role\":\"system\",\"content\":\"${SYSTEM_PROMPT}\"},{\"role\":\"user\",\"content\":\"first request\"}]}" >/tmp/fishmesh-first.sse

curl -sS -N -D /tmp/fishmesh-second.headers http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  --data "{\"model\":\"qwen2.5-0.5b-instruct\",\"stream\":true,\"max_tokens\":32,\"messages\":[{\"role\":\"system\",\"content\":\"${SYSTEM_PROMPT}\"},{\"role\":\"user\",\"content\":\"second request\"}]}" >/tmp/fishmesh-second.sse

grep -Ei 'x-fishmesh-(kv-status|route-reason|policy|cached-prefix-tokens|backend)' /tmp/fishmesh-*.headers
tail -n 1 /tmp/fishmesh-second.sse
```

成功命中要求第二个头文件同时出现：

```text
X-FishMesh-KV-Status: available
X-FishMesh-Policy: kv-aware-v1
X-FishMesh-Route-Reason: kv-aware
X-FishMesh-Cached-Prefix-Tokens: <non-zero>
```

Gateway 更新或 KV replay 尚未确认时，首个请求可以出现下面的**正确降级**，不能把它视为零命中：

```text
X-FishMesh-KV-Status: match-unavailable
X-FishMesh-Policy: kv-aware-load-fallback-v1
X-FishMesh-Route-Reason: kv-aware-signal-unavailable
X-FishMesh-Cached-Prefix-Tokens: 0
```

这仍是 SSE 透传；检查 body 的最后一行是 `data: [DONE]`。

## 指标与故障恢复演练

Gateway 以 `500ms` 周期、`2s` freshness 和 `400ms` 单次超时直接读取两个 vLLM 的
queue/running；queue 达到 16 或本 Gateway 对单 backend 的 local in-flight 达到 32 时，KV-aware
会先应用 HardOverload 门。Gateway `/metrics` 不带 prompt、routing key、token IDs、Pod UID 或 endpoint 标签。它暴露稳定
backend ID 下的 `fishmesh_gateway_kv_cache_instance_valid`、freshness、已提交 sequence、applied/
replay batches，以及 `fishmesh_gateway_kv_aware_{requests,degradations}_total`。查看方式：

```bash
curl -sS http://127.0.0.1:8080/metrics | grep -E \
  'fishmesh_gateway_(kv_cache|kv_aware_)|process_(cpu_seconds_total|resident_memory_bytes)'
```

### 滚动更新

用新 tag 更新 `kustomization.yaml` 的 Gateway image 和 `gateway-image-revision`，再声明式 apply；
不要通过 `kubectl set image` 绕过 Git 记录：

```bash
make image VERSION=r6c-lite-r2
# 在受审阅的 overlay 变更中将 Gateway image 和 gateway-image-revision 改为 r6c-lite-r2。
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/lite-kv-aware
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deployment/fishmesh-gateway --timeout=5m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get rs,pods -l app.kubernetes.io/name=fishmesh-gateway
```

运行中的 SSE 连接不会迁移到新 Pod；这符合 HTTP stream 的连接语义。新连接只在新 Pod
`/readyz` 成功后进入 Service。

### Pod 删除、事件流断开与恢复

先重复“命中与降级对照”，保存已命中的 header。然后删除**一个** vLLM Pod（PDB 只允许一次），
这会真实关闭该 Pod 的 ZMQ live/replay stream；Deployment 会建立替代 Pod：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get pods -l app.kubernetes.io/name=qwen-vllm
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm delete pod <one-qwen-vllm-pod>
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deployment/qwen-vllm --timeout=25m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm logs deployment/fishmesh-gateway --since=30m \
  | grep -E 'kv|replay|sequence|subscriber'
```

在替代 Pod 完成 replay 前再次请求，应记录 `match-unavailable` +
`kv-aware-load-fallback-v1`；等待一个 EndpointSlice refresh/replay 周期后重复请求，应恢复
`available`，或在真实零命中时保持 `available` 且 cached tokens 为 `0`。若出现 sequence gap、
ZMQ 无法建立或 concurrent apply 的非预期日志，不猜测原因：保留上述日志、metrics 和 Pod
描述，停止演练并按 R6B-6 诊断规则报告。
