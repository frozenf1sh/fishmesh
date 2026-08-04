# FishMesh 项目章程与方向约束

> 本文件是项目方向的最高优先级依据。路线、实验或新技术提案与本章程冲突时，先修改并评审
> 本章程，而不是在实现过程中静默改变项目目标。

## 1. 工程目标

FishMesh 是面向 Kubernetes 自托管 LLM 推理的请求流量调度组件。它解决四个工程问题：

1. vLLM Pod 动态创建、终止或失联时，只向可用且状态足够新的 endpoint 发送请求；
2. 长连接和流式请求下，维持正确的取消、超时、排空和资源回收语义；
3. 在请求亲和与后端过载之间执行有界、可解释、可降级的选择；
4. 让部署者能够从指标、日志和决策原因定位一次路由、溢出或回退。

项目完成的标志不是证明某个调度算法普遍更快，而是交付一个行为有界、故障语义明确、能够
接入主流 Kubernetes 推理网关生态，并可通过自动化测试验证的工程组件。

## 2. 目标用户和岗位映射

主要用户是负责 Kubernetes 推理平台的基础架构工程师。项目重点展示：

- Go 并发、HTTP/SSE 请求生命周期和资源管理；
- Kubernetes API、EndpointSlice、RBAC、部署与故障恢复；
- 负载均衡、背压、熔断、可观测性和稳定性工程；
- 与 vLLM、Gateway API Inference Extension、llm-d 等开源系统的集成边界；
- 从故障和测量结果回到工程决策的能力。

FishMesh 对应 AI Infra 平台、云原生推理服务和基础架构研发方向，不以 CUDA kernel、编译器
或推理引擎内核岗位为主要目标。

## 3. 产品边界

### FishMesh 负责

- request context 到 endpoint selection 的确定性策略；
- endpoint eligibility、freshness 和 routing state lifecycle；
- request-path overload、fallback 和 failure contract；
- 策略运行所需的最小 backend observation；
- 可自动验证的 standalone 模式和标准网关集成模式。

### 优先复用开源

- vLLM/SGLang/TensorRT-LLM 等模型执行引擎；
- Envoy/Gateway API 的入口、TLS、认证、通用限流和协议治理；
- llm-d 或兼容 EPP 的 request-control 和插件框架；
- Prometheus、OpenTelemetry 和 Grafana 的遥测、存储与展示；
- Kubernetes 原生 Deployment、Service、EndpointSlice 和 RBAC。

### 不进入当前 MVP

- 自研模型执行、CUDA kernel、量化或编译优化；
- 精确 token-block KV cache index；
- per-backend GPU utilization 加权评分；
- eBPF 请求重定向；
- LLM Agent 自动修改集群；
- 自研 CRD/Operator、Service Mesh 或 prefill/decode disaggregation；
- 多租户计费、完整认证体系和通用 AI Gateway 功能。

## 4. 两种运行形态

### Standalone mode

当前 Go Gateway 负责解析请求、选择 endpoint、代理 SSE 和记录结果。它用于开发、行为测试、
故障注入和本地演示，也是 scheduler core 的可执行参考实现。

### Integrated mode

生产形态复用 Envoy-compatible Gateway 和 llm-d EPP，FishMesh 只提供纯调度策略与决策
provenance。InferencePool、subset、flow control、retry 和 stream lifecycle 由上游运行时负责。
R5C 已实现编译期 scorer 与 `fishmesh-epp` 组合根；不维护第二套完整 Gateway 或 EPP。完整
Gateway/EPP/InferencePool 部署和 wire-level smoke 仍需在 R5D 完成。

两种形态必须复用同一个纯 scheduler core，并在相同候选和负载输入下运行选择不变量测试。
delivery fallback、retry 和流生命周期按各自协议测试，不能为表面一致破坏 EPP 约束。

## 5. 方向决策门槛

任何新增技术或里程碑必须回答以下问题：

1. 它解决了哪个已复现的用户问题或故障模式？
2. 它改善稳定性、可操作性、兼容性或交付能力中的哪一项？
3. 为什么现有开源组件不能直接解决，或 FishMesh 只需要在哪个扩展点补充能力？
4. 新增的运行时状态、权限、依赖和故障模式是什么？
5. 最小验收测试是什么，失败时如何回退？

若只能回答“技术先进”“简历好看”或“可以增加一个实验变量”，默认拒绝进入主线。

以下变化必须先更新本章程：

- 从请求流量调度转向模型执行、训练平台或通用 Agent 平台；
- 引入新的常驻服务、数据库、CRD/Controller 或集群写权限；
- 改变 standalone 与 EPP/llm-d 的主次关系；
- 把当前明确延期的技术重新加入 MVP。

## 6. 实验在项目中的位置

实验只有三个合法用途：

1. 在两个工程方案之间做选择；
2. 验证一个功能或故障恢复的验收条件；
3. 防止性能或稳定性回归。

每个实验在运行前必须写明：待决定事项、对照方案、判定标准、结果将触发的工程动作。没有
后续工程动作的探索不得阻塞主线。单次 K3s smoke 是行为验证，不是性能结论；性能对照使用
有限矩阵、多轮重复和明确环境边界。

## 7. MVP 完成条件

MVP 同时满足以下条件才算完成：

- **可靠性**：动态 endpoint、过载、上游错误、请求取消和滚动发布都有有界行为；
- **资源安全**：连接、等待请求、affinity、observation、circuit 和 metric state 均有上限
  或回收机制；
- **标准集成**：基于 llm-d EPP 的 FishMesh scorer 可部署，standalone 仍可用于测试；
- **可操作性**：关键路径具有 metrics、structured logs、trace 或等价关联手段，并有故障
  runbook；
- **自动验证**：无 GPU simulator E2E 覆盖正常、过载、endpoint 删除、stale discovery 和
  transport failure；真实 GPU 只补充 vLLM/SSE 集成验证；
- **可交付性**：版本化镜像、最小权限清单、multi-arch 构建和可重复部署入口齐备。

与开源 scheduler 的性能比较是重要验证，但不是掩盖上述工程缺口的替代品。

## 8. 当前优先级

1. P1：request-path reliability——circuit、GC、admission、connection bounds；（已完成）
2. P2：simulator E2E、EPP/llm-d adapter 与标准部署；（当前，adapter 垂直切片已完成）
3. P3：dashboard/tracing、runbook 与 release 工程；
4. P4：有限、可复现的开源 scheduler 对照；
5. P5：只有在真实需求出现后再评估延期项。
