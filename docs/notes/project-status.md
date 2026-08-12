# FishMesh 项目当前状态

最后项目状态更新时间：2026-08-16；集群最后真实验收时间：2026-08-15。仓库为
`frozenf1sh/fishmesh`（private），当前 `main` 分支
包含可信 serving baseline、三种 routing mode（`load-balanced`、`session-key`、`kv-aware`，
对应策略标识为 `load-balanced-v1`、`session-key-v1`、`kv-aware-v1`）、request-path reliability
和工程优先的项目章程。

Serving 默认配置已收口到 `internal/serving/config.DefaultConfig()`：环境变量只覆盖这一份
standalone 产品默认，domain 包不再拥有生产 `DefaultConfig` 或对关键业务零值静默补值。运行时依赖
（clock、HTTP client）以及 Kubernetes 协议端口等 adapter 级兜底仍保持在实现边界内。

本次 routing 收敛已完成：普通负载均衡使用 `load-balanced`，客户端传入 key 的有界粘性使用
`session-key`，真实 KV locality 与已知负载联合选路使用 `kv-aware`。纯 prefix hash 和独立
Service 选路不再是可配置策略；Service 只保留为 discovery/策略失败时的最终 fallback。请求头、
环境变量、指标、llm-d 参数、部署 overlay 和文档均已同步到这组三策略命名。

request-path reliability、Serving Domain R1–R4、无 GPU simulator 基础、R5C llm-d 本地集成和
R6A 真实 KV 信号门禁、R6B-1 至 R6B-6、R6C、R6D 与 R6D2 已完成：真实 Render、同步 KV index、pure routing、
requestpath 编排、有界 body/SSE delivery、组合根、真实 ZMQ/replay 和可安装 Lite 产品面均已闭环。R6C
在真实双 vLLM 集群以不带 session key 的两个相同 system prompt、不同 user message 请求，记录到第二请求
`available`、`kv-aware-v1` 和 80 cached prefix tokens；删除实际命中 Pod 后，过渡请求显式降级、
替代 Pod replay 后恢复 KV-aware 命中。R6D 以每组 3×200 默认 loadgen requests 对比 direct Service、
load-balanced 与 KV-aware：KV-aware 保持 explicit degradation correctness，但在单卡短生成 profile 的 TTFT/吞吐
不优于 Service，结论仅限 profile evidence。R6D2 修复 vLLM rollout 后 EndpointSlice membership 未主动换代
KV subscriber 的缺口，并以独立 cache generation、单 Pod prewarm 的 c=1 控制完成 512/2048 两档
Service/KV-aware 对照：KV-aware P50 TTFT 分别低 21.89%/20.65%，但绝对收益 6.845/6.580 ms 均未超过约 10 ms
routing overhead，故没有声称 prefix breakpoint。R5D 标准 Gateway/EPP 部署不取消，但后移到 Lite KV-aware MVP 之后。
方向基准见 `docs/design/project-charter.md`，新决策见 `docs/design/decisions/002-lite-kv-aware-routing.md`。

仓库基础配置默认为 `load-balanced`；`X-FishMesh-Session-Key` 只在显式 `session-key` 模式下作为
客户端提示。历史参考集群在 R6D2 收尾后曾恢复 `deploy/experiments/r6d-session-key`，本次已为 R6H-3
临时切换到 `deploy/experiments/long-context-kv-aware`；压测结束后按证据恢复基线。KV-aware 模式通过
EndpointSlice/Pod UID 建立实例、订阅 KVEvents/replay；unknown/stale 永不伪装成零命中。

Lite 监控已在参考集群完成部署和接入：`deploy/monitoring` 创建 namespace-scoped Prometheus、Grafana、
持久卷、dashboard、rule 文件与最小服务发现 RBAC。Prometheus 的 Gateway target 为 `up`，四条规则均已
加载且 inactive；Grafana 已 provision Prometheus datasource 和 `FishMesh Gateway` dashboard。该最小栈不
部署 Alertmanager/外部 notification receiver，故规则可见且会评估，但不声称已完成值班告警投递。接入和
体验步骤见 `deploy/monitoring/README.md` 与 `docs/notes/runbook.md`。

R6F 已补齐性能归因所需的最小观测契约：只有带有效 publisher timestamp、已成功同步 apply 的 KV
event batch 才记录 `fishmesh_gateway_kv_event_publish_to_apply_seconds`，并以 `live`/`replay` 分开；
它是 publisher-to-apply lag，不是网络 RTT，replay 可能包含历史 event 的较大年龄。只有
`X-FishMesh-KV-Status: available` 的选择才记录 `fishmesh_gateway_kv_aware_cached_prefix_tokens`；其中 0
是真实零命中，unknown/stale 不会被计作零样本。真实两请求验收记录首请求 `available/0`、第二请求
`available/768`，cached-prefix histogram count/sum 为 `2/768`；live event histogram 有 4 个样本、
sum `0.002878708s`。验收后恢复 session-key，未将该低负载 evidence 扩展为性能结论。

R6G 的最终客户端收敛为独立的 `fishmesh-client`：`chat` 在未指定路径时以 UTC 纳秒时间戳自动创建
`~/.local/state/fishmesh/<timestamp>.json`，`request` 用于单次 SSE smoke，`bench` 读取 JSON 计划或内置矩阵，
覆盖多种 prompt 长度、请求数量、批次、同前缀、不同前缀和混合前缀。每次运行自动输出 `plan.json`、无 prompt
内容的 `requests.jsonl`，以及按场景/批次聚合的 `report.json` 和 `report.md`；KV unavailable 不会被计作零命中。
API key 只从 cmd 组合根的 `FISHMESH_API_KEY` 出站使用，压测产物不包含 key、prompt、raw SSE 或任意 upstream
headers。最终测试只使用该客户端，不再扩展旧 loadgen。

本次清理移除了 analyst、Diagnostics、simulator、旧 loadgen、一次性 R6A 探针及其默认 validation/job 入口；
历史结论保留在阶段记录和 Git 历史。`fishmesh-epp`、`internal/serving/llmd` 与
`deploy/integrated/llmd-config` 保留但从默认 `make manifest` 中挂起，待 Standard mode 单独恢复。

R6G 的面向人诊断已补充终端色彩：默认 `auto` 只在 TTY 突出 TTFT、policy、reason、KV-aware status 和 backend，
重定向输出及 JSONL 仍为纯文本，可由 `--color always|never` 显式覆盖。当前长上下文 profile 不发送
session key；无 session key 的请求会由当前 routing mode 决定，故改动 system prompt/history 不应被解释为应切换
upstream 的信号。

R6H 已完成成本式 KV-aware 路由改造：`kv-aware-v1` 将未缓存 token、有效 vLLM queue/running 和 Gateway
local in-flight 显式折算为等价未缓存 token，仍先排除 hard overload，仍将 KV unknown/stale 降级而不当作
零命中。GPU 节点恢复后已部署 `r6h-degrade-r1`，并按受控 profile 保留成功率、失败分类、显存和温度证据。

R6H-2 已完成本地预测 TTFT 的影子观测契约：新的独立 `prediction` domain 对每个 backend 保留有界、
15 分钟内的数值首 SSE TTFT 样本，以非负 ridge 最小二乘拟合未缓存 token、queue、running 和 Gateway
local in-flight；至少 16 个样本且所有候选 load 已知才返回 `would-select`。它不读取或记录 prompt、
Token IDs、routing key 和 SSE 内容，不进入 `routing`，不改变 `kv-aware-v1` 的实际选择、
hard-overload 或 unknown/stale 的 load-balanced 降级。默认 `FISHMESH_PREDICTION_MODE=off`；`shadow` 只增加
低基数状态/误差指标，仍不产生 HTTP 决策变化。GPU 恢复后先用影子数据验证误差和安全边界，再决定是否新开
实际预测选路切片；当前预测模式仍为 off，不改变实际选路。

R6H-3 修复了 KV-aware 选路前 Render 的冷 DNS/连接突发问题：临时 Render failure/timeout 现在显式降级为
load-balanced，请求形状错误和调用方取消仍硬失败；专用 Render client 使用 16 个有界连接、30 秒 DNS
缓存、并发解析 singleflight 和绝对 FQDN 查询。参考集群当前运行 `long-context-kv-aware` profile：64 个
in-flight、16 个数据面连接、keepalive=true。使用 4 KiB/12 KiB prompt、6 个场景、2 个批次共 288 请求时
288/288 成功，TTFT P50/P95 为 85.17/479.83 ms，总耗时 P50/P95 为 234.25/843.63 ms；288 个请求均为
KV available，未观察 Gateway 503、admission rejection 或 upstream error。12 KiB 仍指 prompt 字节数，vLLM
`max-model-len=4096` 未改变，不能把这轮结果解释为 12K token 能力证明。

R6I-0 已固定下一阶段不直接把未验证的动态权重放入请求路径：先建立快速 load observation 和
HardOverload，再交付以毫秒为单位的 calibrated-static TTFT estimator，现有 learned model 继续 shadow，
只有误差门禁通过后才讨论 active。R6I-1 已实现并完成最小真实 smoke：Lite 使用
`500ms/2s/400ms` 的 queue/running observation，queue 16 或单 backend local in-flight 32 触发硬门；
外部 load 完整有效时不再重复叠加完整 local in-flight，未知时仍保留 local fallback。基础默认门槛为零，
因此 load-balanced profile 保持兼容。新镜像 `r6i1-load-gates-r1` 的 manifest digest 为
`sha256:d8db3eac20b9cedadbec853793f16fb56a73bcd209986d9e79946d3fb4854a35`；rollout 后 GPU Node 保持 Ready、
vLLM 2/2、Gateway 1/1，两 backend observation 均为 `ok`。8 个受控流式请求把一个 backend 的 running
sample 从 0 提高到 8，结束后恢复 0；queue 未堆积，因此没有声称真实 HardOverload 已触发或 TTFT 已改善。

README 与 README_CN 现以五分钟 Lite demo 为入口：确认 `load-balanced` 默认基线后临时安装
`lite-kv-aware`，用不带 session key 的同一长 system prompt、不同 user message 请求读取 KV-aware 决策头，
再恢复基线；监控面板可同时观察请求、TTFT、KV 有效性/freshness、降级与 RSS。Standard mode（R6E/llm-d
部署）仍明确后置。

Lite 的发布/回滚边界已记录在 `docs/notes/release-notes.md`：当前已实证的是 macOS arm64 构建机
交叉构建并离线导入 GPU 节点的 Linux amd64 Gateway；Dockerfile 的 arm64 target、远程 multi-arch
manifest 与 SBOM attestation 是发布前流程，尚无真实 release 验证。升级和回滚以 manifest/image digest
及实际 ConfigMap 为证据，并在 KV-aware 信号异常时恢复 `session-key`，不改变 unknown/stale 的降级语义。

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
- Gateway 当前运行只含 Gateway 二进制的 `fishmesh-gateway:r6i1-load-gates-r1`；参考集群当前应用
  `deploy/experiments/long-context-kv-aware`，使用 64 个 in-flight 上限、16 个数据面连接、upstream
  keepalive，并启用 EndpointSlice + KVEvents/replay KV-aware routing；长上下文实验的 vLLM 使用
  `gpu-memory-utilization=0.40` 以覆盖 CUDA-graph 启动开销。它使用专用 SA，只有 namespace 内
  EndpointSlice `get/list/watch` 和 Pods `get/list`，无 Secrets/写操作/cluster-scope 权限；
  Prometheus observation 已启用，unknown/stale 仍回退 load-balanced；
- analyst、simulator 和 loadgen 已冻结且不在当前产品部署或 Gateway 镜像中；遗留 analyst
  Deployment/RBAC 与 loadgen SA 已从集群清理；
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
  `/v1/models` 返回 `session-key` 与 `endpoint-*`，验证后已恢复 baseline。
- N3 Backend Snapshot 第一版：按 EndpointSlice backend ID 采集对应 vLLM `/metrics`，暴露
  queue、running、Prefix Cache、TTFT、status 和 freshness；backend-snapshot overlay 已在
  K3s 验证两个副本均为 `status=ok`，验证后已恢复 baseline。
- N4 身份映射与故障状态：backend targetRef 已映射到 Pod、Node 和声明 GPU request；discovery
  status/freshness/ready backend 已进入 Gateway metrics，EndpointSlice 缓存过期会让 `/readyz`
  返回 503；临时撤销 RoleBinding 后状态变为 `degraded/503`，恢复权限后约一个刷新周期
  自动回到 `ok/200`。
- P0 实验可信度：Loadgen 输出 `run_metadata -> request -> summary`；15 个仍存活的历史 Job
  已恢复 KV-aware Job YAML、原始压缩 JSONL 和 provenance manifest；新实验方案要求重复运行、
  随机 treatment 顺序、失败 attempt 保留和开源 router 同环境对照。
- P0 方向收敛：MVP 从任意 weighted hybrid score 改为 session-key；GPU 指标只作为
  节点级诊断证据，eBPF、自动 actuator 和 disaggregation 明确移出 MVP。
- P1 session-key-v1：仅保存 routing key 的 SHA-256，以 Rendezvous Hash 选择 preferred
  backend，使用独立 queue/local-inflight threshold 溢出，具备 TTL/容量回收、Service
  fallback、决策 provenance 和 race/integration tests。
- P1 K3s 行为 smoke attempt 2：24/24 成功，1 次 miss、11 次 hit、12 次
  local-inflight spillover，两个 backend 各选择 12 次；完整运行配置、镜像 digest、Job、
  EndpointSlice、日志和 JSONL 已归档。该结果只证明行为正确性，不是性能 benchmark。
- 2026-08-10 工程方向章程曾把 standalone Gateway 定位为开发/conformance 载体、把生产形态
  提前为 EPP/llm-d 集成；这一主次关系已在 2026-08-11 由 ADR-002 修订，但当时对 Analyst 和
  实验系统的冻结决定继续有效。
- P1 request-path reliability：全局 admission 128、每 backend 连接上限 32、transport error
  EWMA circuit、endpoint state GC、queue/running per-field sample、client cancellation 和
  no-retry-after-headers 边界均有 race/fault tests。
- P1 K3s 验证：`fishmesh:0.3.0-p1` manifest digest
  `sha256:036149be62f706b5cc3580e1caf29714282a784bc21c56fc30f88a09d3bc0223`；Gateway/Analyst
  rollout Ready 且零重启，启动日志确认全部 reliability 参数；真实 vLLM smoke 8/8 成功，
  两个 routing key 保持 affinity，新 metrics 正常暴露。该 smoke 是兼容性验证，不是性能结论。
- 代码组织 R0：确认“能力包 + 单向依赖 + 显式组合根”适用于 Serving，但拒绝顶层
  `internal/domain`、`shared` 类型仓库和无意义的一实现一接口。已固定包/文件/声明/字面量规范、
  目标依赖 DAG 和 R1–R5 渐进迁移计划；本阶段未改变运行代码或集群。
- Serving Domain R1：Backend/ID、Identity、Observation/Sample、Routing 和 Circuit 已回到各自
  owner；routing mode/policy/reason 与 circuit outcome 已类型化，transport 不再依赖 routing；
  `go list` 架构测试会阻止原子 domain 出现反向依赖。运行协议、配置、部署和集群均未改变。
- Serving Domain R2：`endpoint` 已更名并拆分为 contract-first 的 `discovery`；identity、
  observation、transport 只向 Gateway 暴露接口和明确构造器；环境变量、HTTP/SSE header、
  metric/label 与 vLLM/Kubernetes 协议常量已收口。完整本地 CI 通过，运行协议、部署和集群均
  未改变；GPU 节点暂停期间不执行无价值的 K3s smoke。
- Serving Domain R3：新增协议无关的 requestpath 编排与幂等 lease；discovery freshness、
  circuit eligibility、Service fallback、local in-flight 和 membership GC 已从 Gateway 移出；
  client cancellation、deadline、upstream/downstream stream failure 保持独立 typed outcome。
- Serving Domain R4：新增非阻塞 admission domain 和进程 config package；Gateway 已拆为契约、
  handler、proxy stream 与 metrics 文件，只依赖注入能力；`cmd/fishmesh-gateway` 显式创建并按
  反序关闭 requestpath、observation、resolver 和 transport 等有状态资源。R1–R4 重构闭环。
- R5A 无 GPU simulator：新增独立 `internal/simulator` 和可执行入口，提供 OpenAI SSE、vLLM
  queue/running 指标及运行时故障控制；真实 HTTP/TLS E2E 已覆盖 slow、stream abort、circuit
  fallback、overload、cancellation、EndpointSlice removal/stale。该阶段未使用 GPU/K3s。
- R5B EPP/llm-d 决策：按 GIE v1.5.0、EPP Protocol v1.0.0 和 llm-d Router v0.9.0 核对
  protocol、InferencePool、Filter/Scorer/Picker、data layer 与 in-flight lifecycle；选择 pinned
  llm-d 编译期 scorer + 自定义 EPP 入口，拒绝自研 ext_proc 和生产使用 LWEPP。设计同时纠正
  integrated 复用整个 requestpath 的错误假设：两种模式只共享纯 routing 选择，空 subset 在
  EPP 中返回 503，不走 standalone Service fallback。本阶段未使用 GPU/K3s。
- R5C llm-d 本地集成切片：新增只依赖 backend/observation/routing 的 `internal/serving/llmd`，
  具体实现 pinned v0.9.0 的 Filter、Scorer、ConsumerPlugin 和 response hook；queue freshness、
  required in-flight、endpoint churn、并发、多实例、selected/served provenance 和纯策略
  conformance 均有 race/contract tests。新增 `fishmesh-epp` 组合根复用上游 runner，Docker 镜像
  包含新二进制；最小 EndpointPickerConfig 已进入 manifest 门禁。该阶段没有访问 GPU/K3s，
  完整 Gateway/EPP/InferencePool 部署和 ext_proc wire smoke 尚未完成。
- R6 方向复位：确认主要缺口不是 scorer 代码量，而是可独立交付的产品和真实 cache signal。
  Lite Gateway 恢复为主产品，Standard EPP 保留为生态集成；KV-aware 将复用 vLLM KVEvents、
  Render API 与上游 `llm-d-kv-cache` library。simulator/loadgen 冻结功能，Analyst/Diagnostics 从
  默认产品面移除，R6C 前不做破坏性源码删除。新增 ADR-002，并统一更新章程、架构、计划、
  实验规范、中英文 README 与代码可读性要求。代码规范补充允许按具体概念建立纯
  `entity/<concept>` 子包：只依赖标准库，可由值对象执行 `Validate` 等自身校验，但不得成为新的
  shared 类型仓库。本阶段未修改集群或 Go 行为。
- R6A 真实 KV 信号闭环：新增声明式 vLLM 0.23.0 KVEvents/replay overlay 和一次性真实探针；
  `llm-d-kv-cache` 升至正式版 v0.9.0。不同会话共享 system prompt 时只在真实缓存 Pod 命中
  8 blocks/128 tokens；断流期间 KV-aware 状态先 invalid，恢复后 replay 补回；真实压力产生 3105
  个 removed 并把旧命中降为零；旧 Pod UID 删除后 80-token 命中被清理，新 Pod 从 sequence 0
  独立发布。实测 Render 约 5–6 ms、lookup 约 0.08–0.14 ms，压力后 Go HeapAlloc 约 11.7 MiB。
  最小复测 event lag 为 0.678 ms，空索引探针 RSS 约 33.2 MiB。实验结束后恢复基础 vLLM
  Deployment，KV-aware 尚未接入 Gateway。
- R6B-1 真实分词能力域：新增无其他 internal 依赖的 `internal/serving/tokenization`，以不可变
  Result/Prompt 提供真实 Token IDs 和 cache salt；vLLM adapter 显式支持 Chat/Completions Render，
  保留扩展字段，并保护 timeout、取消、请求/响应体和 token 总数边界。模型错配、非 2xx、异常
  响应和多模态未支持均返回 typed error，不能被误解为零命中。本阶段没有接 Gateway、KV index
  或集群，当前线上行为仍是 session-key。
- R6B-2 真实 KV 状态域：新增只依赖 backend 的 `internal/serving/kvcache`，复用上游 vLLM parser、
  canonical hash、有界 index 和 longest-prefix scorer，但不用没有容量/完成回执的上游异步 event
  workqueue。每条 live/replay event 同步 apply 后才推进 sequence；gap 先 invalid，再 replay，无法
  覆盖时清理该 Pod。cache salt、BlockRemoved、replay TTL、Pod UID 替换、查询/事件/index 容量和
  Close 等待均有 race/contract tests。当前只承诺已验证的 vLLM 0.23 文本 GPU event，不支持的
  LoRA/HMA/offload 明确失效。本阶段仍未接 Gateway 或 routing，也没有访问集群。
- R6B-3 缓存/负载联合纯路由：routing 新增不依赖 kvcache/tokenization 的 `KVAwareInput`、
  `KVMatch`、`Load` 和 `kv-aware-v1`。策略按 eligible、hard overload、uncached tokens、
  queue/running、local in-flight 和最终 session 平局提示做确定性选择。有效零命中保持 KV-aware 语义；
  unknown/stale 则由 requestpath 显式改用 load-balanced，并返回 typed degradation。Gateway、Render、
  KV index 和线上部署尚未接入。
- R6B-4 请求路径 KV-aware 编排：requestpath 在 KV-aware mode 下显式注入 tokenization 与 kvcache，
  将只读 TokenIDs、模型、cache salt 和 eligible backends 构造成一次 KV lookup，再投影 Match 为
  routing `KVAwareInput` 并消费其 Decision。tokenizer/lookup 普通失败与 unknown/stale match 通过
  typed load-balanced 降级发布；取消/超时不吞掉而是返回调用方。Gateway、组合根、Pod instance 翻译、
  subscriber 和线上部署仍未接入，现网仍是 session-key。
- R6B-5 有界 Body 与 KV-aware 交付：Gateway 对原始请求 body 实施 2 MiB 默认硬上限，并用同一字节
  副本完成 requestpath Render/KV lookup 输入和 upstream replay；超限在选路前返回 413。SSE copy、
  `[DONE]`、headers 后不 retry 和 lease outcome 均保持原行为。响应新增 `X-FishMesh-KV-Status`，
  现有 route reason/policy/backend 头在选路后立即写出，unknown/stale 的 load-balanced 降级可直接观察。
  进程内闭环测试已走通 Render→Lookup→select→SSE；生产组合根和 KV subscriber 尚未接入。
- R6B-6 组合根真实 KV 接入：Gateway 在显式 KV-aware mode 创建 renderer 与有界 kvcache index，将
  EndpointSlice `targetRef.uid`、Pod IP 翻译为稳定 Instance 和 `5557/5558` ZMQ endpoint，并在退出时
  关闭 subscriber。真实 vLLM 0.23 事件包含 `group_idx=0/full_attention`；该单组文本语义已以同包
  contract test 窄化兼容，其他 HMA/LoRA/window 语义仍 explicit invalid。真实集群第二个无 session key
  请求得到 160 cached prefix tokens；首请求 `match-unavailable` 保持 load-balanced 降级。验收后已恢复默认
  session-key。
- R6C Lite 产品化：默认 Dockerfile 仅构建 `fishmesh-gateway`，新 `deploy/lite-kv-aware` 组合专用
  RBAC、PDB、probes、资源/安全边界与 KVEvents/replay 配置；指标发布 KV validity/freshness/sequence/
  batches、KV-aware degradation 与进程 CPU/RSS，且不泄露请求数据。真实滚动更新成功，删除实际命中 vLLM
  Pod 后旧事件流退出、剩余 backend 的 zero miss 仍为 KV-aware，替代 Pod replay 的过渡明确降级，随后
  恢复 80-token KV-aware hit。llm-d 的 controller-runtime logger 由 Gateway 入口显式 discard，修复后的
  live/recovery 日志未再出现 stack warning。Flannel 仍不执行 NetworkPolicy，策略只作为 CNI 迁移后待启用项。
- R6D 有限性能对照：复用默认 loadgen workload，对 Service/load-balanced/KV-aware 各执行 3×200 requests，
  以 SSE TTFT、两 vLLM Pod token counter delta、Gateway process CPU/RSS 与 policy headers 形成一页表。
  KV-aware 的 8/600 restart/replay 过渡请求显式降级，592/600 使用 KV-aware policy，未观察错误 KV-aware 声明；
  但其 P50/P95 TTFT 分别比 Service 高 32.19%/12.82%，generation throughput 低 7.91%。单 RTX 4060
  time-slicing、Qwen2.5-0.5B、两个共享副本的结果仅作 correctness/profile evidence；当前 metrics 不提供
  可按 treatment 归因的 per-event latency。实验后已恢复 session-key。
- R6D2 前缀长度分段对照：修复 requestpath 后台 reconcile 漏掉 KV lifecycle 的缺口；vLLM rollout 后无需
  业务请求即可为新 EndpointSlice Pod 建立 `Valid=true` subscriber。按独立 cache generation、单 Pod
  prewarm 验证 c=1，完成 512/2048 的 Service/KV-aware 各 200 requests：KV-aware P50/P95 分别为
  24.429/24.936、25.279/26.680 ms，Service 为 31.274/35.442、31.859/37.059 ms。收益低于约 10 ms
  routing overhead，未声称拐点；GPU dmon 峰值 68°C，无 watchdog 告警，最终恢复 session-key。
- R6H-3 长上下文验证：mixed 生成器已改为按整个场景的请求总数计算比例；`long-context-balanced` 计划的两个
  mixed 场景各自使用 100 请求，实际分布为 60 个 `shared-0`、20 个 `unique-*`、20 个其他共享前缀
  （`shared-1/2/3` 为 7/6/7）。两轮反向顺序 A/B 的四个正式 run 共 1568/1568 成功；load-balanced/KV-aware 的 TTFT
  P50/P95 分别为 48.40/431.42 ms 与 50.65/100.32 ms，KV-aware 的 TTFT P95 下降 76.7%，总耗时 P95
  下降 55.0%，而 TTFT P50 上升 4.6%，收益主要集中在尾延迟。真实 mixed-4k/mixed-12k 的 TTFT P95 分别
  下降 62.3%/84.3%。四次正式 run 无 Gateway admission/upstream error，KV-aware 784/784 `available`，
  GPU SM/memory-controller 峰值 100%，显存 7262/8188 MiB，温度峰值 65°C。报告位于
  `artifacts/bench/long-context-mixed-comparison-r6h-r2/`；下一步按并发 1/4/8/16 做阶梯测试。

## 下一步待办

1. 按 [`ADR-003`](../design/decisions/003-calibrated-ttft-routing.md) 和
   [`R6I 多阶段计划`](../design/ttft-routing-development-plan.md) 先完成 load observation、HardOverload 与
   calibrated-static TTFT 交付，不把未验证的 learned model 直接接入路由；
2. 为正式实验加入实际 prompt token、cache salt/run nonce、cache generation、完整 provenance 和多轮统计，
   先在当前 4096 上下文内完成 512/1024/2048/3072 token 阶梯；
3. GPU 容量允许后再评估 4096/8192/12288 token，禁止把 12 KiB 字节报告改称 12K token；
4. learned-shadow 只有在 MAE/P95 error/agreement 门禁通过后才另立 active 决策；
5. Standard mode/llm-d 完整闭环继续后置，避免与 Lite TTFT 主线同时扩大；
6. 在声称 NetworkPolicy 已生效前迁移到支持 policy enforcement 的 CNI；当前 Flannel 不能执行
   声明式 NetworkPolicy。
