# FishMesh 项目当前状态

最后验证时间：2026-08-09。仓库为 `frozenf1sh/fishmesh`（private），当前 `main` 分支
包含可信 serving baseline、bounded-affinity-v1、request-path reliability 和工程优先的项目
章程。

request-path reliability 已完成，当前主线是无 GPU simulator E2E 和标准网关集成。实验只用于
工程决策、验收和回归防护；不会作为独立路线扩展。方向基准见
`docs/design/project-charter.md`。

## 当前运行拓扑

| 节点 | 角色 | 当前运行内容 |
| --- | --- | --- |
| `fishmesh-control-plane`（`100.111.18.52`） | OrbStack 内的 ARM64 Ubuntu VM | 官方 K3s server、CoreDNS、local-path provisioner、metrics-server |
| `fishmesh-gpu`（`100.98.49.106`） | 带 RTX 4060 的 x86_64 Ubuntu 笔记本 | K3s agent、NVIDIA device plugin、两个 vLLM 副本、amd64 FishMesh Gateway |

OrbStack 内置的 Kubernetes 集群不是这个多节点 control plane。官方 K3s server 运行在
已有的 OrbStack Ubuntu VM 内。GPU 笔记本上旧的 standalone `k3s.service` 数据被保留，
但服务 disabled；当前只有 `k3s-fishmesh-agent` enabled 且 active。

当前推理状态：

- `qwen-vllm` 使用 digest-pinned vLLM 0.23.0，两个副本提供本地缓存的
  Qwen2.5-0.5B-Instruct；
- RTX 4060 通过 NVIDIA device plugin 0.19.2 暴露两个 time-sliced resource；它们不是
  两块独立 GPU，也不提供显存或故障隔离；
- `qwen-vllm-baseline` 是普通 ClusterIP Service，负责随机的连接级 endpoint 选择；
  `qwen-vllm` 仍然是 headless Service，EndpointSlice 实验直接读取它的 Ready 地址；
- `fishmesh-gateway` 是一个 Go streaming proxy，运行在 GPU 节点。它不申请 GPU，
  但因为第一个镜像是 amd64 而 control plane 是 ARM64，所以被放在该节点；
- Gateway 当前运行 `fishmesh:0.3.0-p1` 的 bounded-affinity overlay；默认阈值为 local
  in-flight delta 2、queue-depth delta 1，EndpointSlice 不可用、无 Ready backend 或过期时
  回退 Kubernetes Service；
- `fishmesh-analyst` 是同镜像中的只读慢速控制面，当前以 `observability` overlay 运行在
  GPU 节点，不在请求路径中，仅通过 namespace-scoped Role 读取 Events/Pods；
- 模型持久化于 GPU 笔记本的 `/var/lib/fishmesh/models`，通过 retained local PV/PVC
  暴露，不会在每次 Pod 重启时重新下载。

## 从 Mac 体验推理

先在一个终端保持以下命令运行：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

另开终端查看模型：

```bash
curl http://127.0.0.1:8080/v1/models
```

发送不需要 API key 的流式 Chat Completion：

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [{"role": "user", "content": "用一句话介绍 FishMesh。"}],
    "stream": true,
    "max_tokens": 64,
    "temperature": 0.2
  }'
```

这是内部实验 endpoint，不是对公网暴露的服务。按 `Ctrl-C` 停止 port-forward 不会改变
集群状态。

## 暂停与恢复

如果只想释放 RTX 4060 显存，同时保留 K3s control plane 和 agent：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  scale deployment/qwen-vllm --replicas=0
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  scale deployment/fishmesh-gateway --replicas=0
```

恢复时，`qwen-vllm` 使用 `--replicas=2`，Gateway 使用 `--replicas=1`。模型文件仍在
磁盘上，但 vLLM 的内存权重、CUDA graphs 和 KV cache 会丢失；下一次启动需要重新加载
并 warm up。

如果临时关闭 Ubuntu GPU 笔记本，Mac 上的 control plane 仍然存在，但
`fishmesh-gpu` 会变成 `NotReady`，推理没有可用 endpoint。Ubuntu 下次启动时，已启用的
`k3s-fishmesh-agent` 会自动启动并重新加入集群；Deployment 会重新创建 Gateway/vLLM
Pod。不会丢失模型数据，但会出现服务中断和 cold-start 延迟。关闭 OrbStack control-plane
VM 的影响更大：Kubernetes API 和 reconciliation 也会停止，应按完整集群关闭处理。

真正受推理影响的是 Ubuntu GPU 笔记本。两个副本当前即使 GPU 利用率接近 0，也会占用约
6874 / 8188 MiB 显存；time-slicing 不提供 VRAM isolation。Mac 只运行轻量 control plane
和 Tailscale 路径，正常 CPU/RAM 影响很小。Ubuntu 不接收请求时，vLLM 仍会保留显存，
直到你把副本缩容为 0。

## 已完成

- Tailscale 上的 K3s server/agent、NVIDIA toolkit/device plugin 和 CUDA smoke test；
- 使用本地 model PV/PVC 的离线 vLLM 部署，以及两个稳定副本；
- Go Gateway、streaming proxy、Prometheus metrics、确定性的 Loadgen、JSONL 输出、
  单元测试、镜像导入脚本、Kustomize 清单、GitHub Actions 和本地 `act` 入口；
- 第一轮 random-Service baseline：200/200 成功，TTFT P50 77.26 ms、P95 122.01 ms、
  P99 312.67 ms；原始输出已归档在本地 `artifacts/`。
- Evidence-based Diagnoser 骨架：`Incident -> 四个领域工具 -> rules-v1 -> Diagnosis/Evidence/
  Recommendation`，本地和 K3s demo API 均验证返回 `prefix_locality_degraded`。
- N1 真实观测适配器：vLLM/GPU Prometheus、Kubernetes Events/Pod 状态和显式
  `insufficient_observability` 缺口诊断已完成单元测试；observability overlay 已在 K3s
  部署验证，vLLM 与 Kubernetes signals 返回 `ok`。
- N2 EndpointSlice 第一版：Gateway 支持 namespace-scoped watch/list、Ready 过滤、稳定
  backend ID、动态 in-flight counter 和 Service fallback；实验 overlay 已在 K3s 验证
  `/v1/models` 返回 `prefix-affinity` 与 `endpoint-*`，验证后已恢复 baseline。
- N3 Backend Snapshot 第一版：按 EndpointSlice backend ID 采集对应 vLLM `/metrics`，暴露
  queue、running、Prefix Cache、TTFT、status 和 freshness；backend-snapshot overlay 已在
  K3s 验证两个副本均为 `status=ok`，验证后已恢复 baseline。
- N4 身份映射与故障状态：backend targetRef 已映射到 Pod、Node 和声明 GPU request；discovery
  status/freshness/ready backend 已进入 Gateway metrics，EndpointSlice 缓存过期会让 `/readyz`
  返回 503；临时撤销 RoleBinding 后状态变为 `degraded/503`，恢复权限后约一个刷新周期
  自动回到 `ok/200`。
- P0 实验可信度：Loadgen 输出 `run_metadata -> request -> summary`；15 个仍存活的历史 Job
  已恢复 exact Job YAML、原始压缩 JSONL 和 provenance manifest；新实验方案要求重复运行、
  随机 treatment 顺序、失败 attempt 保留和开源 router 同环境对照。
- P0 方向收敛：MVP 从任意 weighted hybrid score 改为 bounded affinity；GPU 指标只作为
  节点级诊断证据，eBPF、自动 actuator 和 disaggregation 明确移出 MVP。
- P1 bounded-affinity-v1：仅保存 routing key 的 SHA-256，以 Rendezvous Hash 选择 preferred
  backend，使用独立 queue/local-inflight threshold 溢出，具备 TTL/容量回收、Service
  fallback、决策 provenance 和 race/integration tests。
- P1 K3s 行为 smoke attempt 2：24/24 成功，1 次 miss、11 次 hit、12 次
  local-inflight spillover，两个 backend 各选择 12 次；完整运行配置、镜像 digest、Job、
  EndpointSlice、日志和 JSONL 已归档。该结果只证明行为正确性，不是性能 benchmark。
- 工程方向章程：standalone Gateway 定位为开发/conformance 载体，生产形态提前为
  EPP/llm-d 集成；Analyst 冻结扩展，实验系统不再与可靠性和交付能力并列为产品主线。
- P1 request-path reliability：全局 admission 128、每 backend 连接上限 32、transport error
  EWMA circuit、endpoint state GC、queue/running per-field sample、client cancellation 和
  no-retry-after-headers 边界均有 race/fault tests。
- P1 K3s 验证：`fishmesh:0.3.0-p1` manifest digest
  `sha256:036149be62f706b5cc3580e1caf29714282a784bc21c56fc30f88a09d3bc0223`；Gateway/Analyst
  rollout Ready 且零重启，启动日志确认全部 reliability 参数；真实 vLLM smoke 8/8 成功，
  两个 routing key 保持 affinity，新 metrics 正常暴露。该 smoke 是兼容性验证，不是性能结论。

## 下一步待办

1. 用可控 simulator 把 slow/error/removed backend、discovery stale、overload 和 cancellation
   变成不依赖 GPU 的自动 E2E；
2. 完成 EPP/llm-d integration spike，记录协议、插件扩展点、失败模式和版本约束，再选择一个
   integrated runtime path；
3. 增加 dashboard、trace/log correlation、runbook、multi-arch release 和 supply-chain metadata；
4. 工程闭环后再运行有限 workload matrix，并加入一个开源 scheduler 对照；只有简历使用
   性能数字时才要求至少两个独立物理 GPU；当前单卡 time-slicing 结果只
   能作为 correctness/profile 证据；
5. 在声称 NetworkPolicy 已生效前迁移到支持 policy enforcement 的 CNI；当前 Flannel 不能
   执行声明式 NetworkPolicy。
