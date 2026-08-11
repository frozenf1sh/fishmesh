# FishMesh 项目章程与方向约束

> 状态：强制执行，2026-08-11 方向复位。
> 本文件是项目方向的最高优先级依据。路线、实验或新技术提案与本章程冲突时，必须先更新并
> 评审本章程，禁止在实现过程中静默改变目标。

## 1. 一句话定位

FishMesh 是面向小型、自托管 Kubernetes GPU 集群的轻量 LLM-aware Router：它代理
OpenAI-compatible HTTP/SSE 请求，利用 vLLM 真实 KV cache locality、实时负载和 endpoint
生命周期选择后端；在平台化环境中，同一策略可通过 llm-d EPP 接入 Envoy-compatible Gateway。

## 2. 工程目标

FishMesh 解决五个可以实际部署和演示的问题：

1. vLLM Pod 动态创建、终止、重启或失联时，只向可用且状态足够新的 endpoint 发送请求；
2. 不依赖客户端声明 cache 位置，识别不同会话共享的 system prompt 在哪些 Pod 上拥有真实
   KV blocks；
3. 在 cache locality、queue/in-flight、过载和近期故障之间执行有界、可解释的选择；
4. 在长连接和流式请求下维持正确的取消、超时、排空、连接复用和资源回收；
5. 让部署者通过指标、日志、reason 和 runbook 定位一次选择、降级、溢出或失败。

项目完成的标志不是证明某个算法普遍更快，也不是积累大量实验代码，而是交付一个可安装、
可运行、可观测、可恢复、可升级，并能说明开源边界的工程产品。

## 3. 目标用户与岗位映射

主要用户是管理 2–8 个模型服务器副本的小型内部推理平台团队。它们希望获得比 Kubernetes
Service 更懂 LLM 的逐请求路由，但暂时不需要完整 Gateway API/Envoy/EPP 平台。

项目重点展示：

- Go HTTP/SSE、并发、取消传播、连接池和有界状态；
- Kubernetes EndpointSlice、Pod 生命周期、RBAC、滚动发布和故障恢复；
- vLLM Render API、KVEvents、prefix cache 和 token/block 语义；
- cache/load/failure 联合调度及可信降级；
- Prometheus、结构化日志、告警、runbook 和 release 工程；
- 与 Envoy、Gateway API Inference Extension、llm-d 的清晰集成边界。

FishMesh 对应 AI Infra 平台、云原生推理服务和基础架构研发岗位，不以 CUDA kernel、编译器、
训练框架或模型算法岗位为主要目标。

## 4. 产品交付物

### 4.1 Lite mode：主交付物

Lite mode 以独立 `fishmesh-gateway` Deployment + Service 存在，拥有：

- OpenAI-compatible HTTP/SSE 透明代理；
- EndpointSlice/Pod 动态发现与 endpoint-scoped 状态回收；
- vLLM Render API 的真实 Token IDs；
- vLLM KVEvents 驱动的逐 Pod、有界、近实时 KV block index；
- cache locality、queue/in-flight、健康和故障联合选择；
- KV-aware 信号过期时的 load-balanced/确定性降级；
- admission、connection bounds、circuit、graceful shutdown；
- metrics、structured logs、request provenance、dashboard 和 runbook；
- 一条命令可重复部署的声明式 Kubernetes 清单和版本化镜像。

Lite mode 不等于通用 API Gateway。TLS、认证、域名和外部流量治理可以由已有 Ingress/Gateway
承担；FishMesh 负责从请求进入 LLM Router 到选中 vLLM Pod 的路径。

### 4.2 Standard mode：标准生态交付物

Standard mode 保留 `fishmesh-epp`：

- 数据面使用 Envoy-compatible Gateway；
- endpoint 组使用稳定的 InferencePool API；
- ext_proc、解析、flow control、retry 和 response lifecycle 使用固定版本 llm-d Router；
- llm-d precise prefix producer 提供真实 cache match；
- FishMesh 只翻译信号并执行与 Lite mode 相同的纯 routing policy。

Standard mode 用来证明策略可以进入主流平台，不要求 FishMesh 复制 Envoy 或 llm-d。

## 5. 能力所有权与开源边界

### FishMesh 负责

- Lite HTTP/SSE 数据面和请求生命周期；
- endpoint eligibility、freshness、membership 与本地状态生命周期；
- tokenization/KV index 的调用、边界、freshness 和降级编排；
- cache/load/failure 联合选择及 typed reason；
- 轻量部署、资源预算、可观测性和运维交付；
- Standard mode 的薄 adapter 与跨运行时 conformance。

### 优先复用开源

- vLLM/SGLang 等模型执行引擎和本地 KV cache；
- vLLM Render API 和 KVEvents 协议；
- `llm-d-kv-cache` 的事件解析、block key 重建和索引基础能力；
- Envoy/Gateway API 的入口、TLS、通用流量治理；
- llm-d 的 EPP、InferencePool data layer、flow control 和 response lifecycle；
- Prometheus、OpenTelemetry 和 Grafana 的遥测生态；
- Kubernetes 原生 Deployment、Service、EndpointSlice、RBAC 和 PDB。

“为什么不用开源”的回答不是强调自研数量，而是说明：FishMesh 复用 engine/protocol/library，
在小集群交付更小的完整 Router；平台化环境则直接进入标准 EPP，不重复造轮子。

## 6. 优先级与冻结边界

| 分类 | 内容 | 当前规则 |
| --- | --- | --- |
| P0 主线 | Gateway、requestpath、routing、discovery、observation、transport、circuit、admission | 持续产品化 |
| P0 新能力 | tokenization、KVEvents/index、KV-aware cache/load policy | R6A 通过后实施 |
| P1 标准集成 | llmd adapter、fishmesh-epp、Gateway/InferencePool 部署 | Lite MVP 后闭环 |
| 开发工具 | simulator、loadgen | 冻结功能，只允许回归修复和有限对照 |
| 冻结模块 | analyst、Diagnostics Context | 只允许安全/构建修复，从默认镜像和部署移除 |
| 历史材料 | 旧 experiments、overlay、阶段报告 | 保留，不继续扩张 |

冻结模块不得为了目录整齐而重构，也不得阻塞主线。物理删除必须在默认产品拆分完成后单独
决策、单独提交，不与 KV-aware 行为变更混合。

## 7. 当前 MVP 明确不做

- 自研模型执行、CUDA kernel、量化或编译优化；
- 自研 vLLM KVEvents 协议、分词器或另一套 KV cache library；
- Redis/Valkey 共享索引、数据库或独立 cache-index service；
- per-backend GPU utilization 加权评分；
- eBPF 请求重定向或 socket rewrite；
- LLM Agent 自动修改集群；
- FishMesh CRD/Operator、Service Mesh；
- prefill/decode disaggregation 和跨节点 KV transfer；
- 多租户计费、完整认证体系和通用 AI Gateway；
- 为增加代码行数而重写 llm-d 已有 scorer/filter。

只有真实规模、故障或用户需求证明当前边界不足时，才能通过新 ADR 重新评估。

## 8. 路由不变量

后续 KV-aware 路径必须始终满足：

1. cache locality 来自逐请求、逐 Pod 的 block match，累计 hit rate 不能冒充 locality；
2. 不同 session 的共同 token prefix 可以复用，不强制客户端提供 FishMesh key；
3. unknown/stale cache 不等于零命中，必须显式降级；
4. cache 命中不能覆盖 terminating、stale、open-circuit 或严重过载；
5. Pod UID 变化、eviction 或 subscriber 失效后，不继续使用旧 block 归属；
6. session hint 只影响平局/稳定性，不覆盖真实 cache/load；
7. 所有状态有容量、TTL、membership reconcile 或 Close 清理；
8. 请求取消传播到 upstream，响应开始后不做透明 retry；
9. Lite fallback 与 Standard 空 subset/503 契约保持独立；
10. 每次降级都具有固定 reason、metric 和日志，不发生静默策略变化。

## 9. 方向决策门槛

任何新增技术、常驻进程或里程碑必须回答：

1. 它解决哪个已复现的用户问题或故障模式？
2. 它改善交付、稳定性、性能、可操作性或生态兼容性中的哪一项？
3. 上游是否已有可直接复用的实现？FishMesh 差异边界在哪里？
4. 新增哪些状态、权限、依赖和故障模式？
5. 能否用现有真实集群先完成最小垂直闭环？
6. 验收失败时删除、降级或回退到什么？

若主要理由是“技术先进”“增加代码量”“简历好看”或“可以再跑实验”，默认拒绝。

以下变化必须先更新本章程和 ADR：

- 改变 Lite/Standard 主次关系；
- 新增常驻服务、数据库、CRD/Controller 或集群写权限；
- 改变真实 KV cache 的数据来源；
- 把冻结/排除项重新加入 MVP；
- 将项目转向模型执行、训练平台或通用 Agent 平台。

## 10. 实验与真实集群

实验只允许用于：技术门禁、工程选择、验收和回归防护。

- engine contract、KVEvents、tokenization、rollout 和性能必须优先使用现有真实集群；
- simulator 只保留确定性取消、竞态和故障状态机回归，不再扩展新的产品能力；
- 每个实验先写决策、baseline、判定标准和后续动作；
- 达到决策所需证据后立即停止，不以扩展矩阵代替交付；
- 当前单卡 time-slicing 集群可证明行为和相对开销，不能声称独立 GPU 水平扩展。

## 11. MVP 完成条件

以下条件同时满足才算 Lite MVP：

- **真实 locality**：跨 session 公共 prefix、eviction、Pod restart、replay 与 stale 降级均在
  真实 vLLM 上闭环；
- **联合选择**：cache/load/failure 策略可解释，且 hard overload guard 有测试和指标；
- **可靠性**：动态 endpoint、上游错误、请求取消、事件断流和滚动发布都有有界行为；
- **资源安全**：request body、连接、in-flight、KV index、observation、circuit 和 metric state
  均有上限或回收；
- **轻量交付**：独立 gateway 镜像、声明式安装、最小权限、探针、PDB 和资源预算齐备；
- **可操作性**：request ID 能关联 tokenization、cache match、选择、降级和 upstream outcome，
  并提供 dashboard/alerts/runbook；
- **真实性能**：完成 Service、load-balanced、FishMesh KV-aware 和 llm-d precise 的有限同环境对照；
- **标准兼容**：Standard mode 可部署并消费 llm-d precise prefix match。

## 12. 当前优先级

1. R6A：真实 KVEvents + Render + 跨 session prefix match 可行性门禁（已完成）；
2. R6B：tokenization/kvcache 能力域与 cache/load 纯策略（当前）；
3. R6C：Lite mode 产品化、真实故障验收和默认镜像收缩；
4. R6D：可操作性、资源预算和有限开源对照；
5. R6E：Envoy/Gateway/InferencePool/llm-d Standard mode 闭环；
6. MVP 后才重新评估冻结或排除项。
