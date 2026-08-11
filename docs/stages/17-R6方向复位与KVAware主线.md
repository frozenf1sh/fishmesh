# 阶段 17：R6 方向复位与 KV-aware 主线

> 日期：2026-08-11
> 类型：架构与交付边界调整
> 代码行为：未改变
> 集群状态：未改变，本阶段只进行了只读核对

## 1. 为什么需要本阶段

R5C 完成后，FishMesh 已能把自定义 scorer 编译进 llm-d EPP。但重新从秋招项目和真实交付物
角度检查时，出现了明显失衡：

- 仓库中最完整、已经在 K3s 运行的 Go Gateway 被描述为“退役开发载体”；
- 主要生产叙事只剩一个较薄的 scorer 和 EPP 组合入口；
- 当前路由依赖客户端提供的 key，不能利用不同会话共享的 system prompt；
- simulator、conformance 和实验文档持续增长，但安装、运行、观测和性能交付仍然不足；
- 如果继续增加近似 cache scorer，能力会与 llm-d 内置 prefix-cache 路由重复。

所以问题不只是 README 写法，而是主交付物选择错误。本阶段正式修正项目章程和后续路线。

## 2. 新的项目主线

FishMesh 的主要交付物改为：

> 面向小型、自托管 Kubernetes GPU 集群的轻量 LLM-aware Router。它直接代理 OpenAI/SSE
> 请求，消费 vLLM 的真实 KVEvents，维护逐 Pod cache locality，并结合 queue、in-flight、
> endpoint 生命周期和故障状态选择最合适的后端。

同时保留 Standard mode：

> 在已有 Gateway API/Envoy 平台的集群中，FishMesh 通过 llm-d EPP 适配同一份 routing policy，
> 复用上游的 ext_proc、InferencePool、flow control 和 response lifecycle。

这不是放弃开源生态，而是形成清晰的双形态：Lite mode 解决小集群的部署和资源成本，Standard
mode 解决平台化接入。

## 3. 为什么必须使用真实 KV cache

Session affinity 只知道“同一个 key 尽量去同一 Pod”。它不知道：

- 另一个 session 是否拥有相同 system prompt；
- 目标 Pod 是否真的还保存对应 KV blocks；
- cache 是否已经被 eviction；
- Pod 重启后原有 cache 是否全部消失。

R6 的正确数据链路是：

```text
OpenAI request
  -> vLLM Render API
  -> KV-aware Token IDs
  -> per-Pod KV block lookup

vLLM Pod
  -> KVEvents over ZMQ
  -> block stored / removed / replay
  -> bounded local index
```

这样，两个不同 session 只要前置 system prompt 的完整 token blocks 相同，就能复用真实 cache。
客户端 key 仍可作为可选 session hint，但不再充当 cache 事实。

## 4. 交付优先级重新划分

| 级别 | 模块/产物 | 决定 |
| --- | --- | --- |
| 主交付 | `fishmesh-gateway`、routing、requestpath、discovery、observation、circuit、admission、transport | 继续开发并产品化 |
| 新主能力 | tokenization、逐 Pod KV event/index、cache/load 联合策略 | R6A 门禁后渐进实现 |
| 标准交付 | `fishmesh-epp`、llmd adapter、Gateway/InferencePool overlay | 保留，排在 Lite MVP 后 |
| 开发工具 | simulator、loadgen | 冻结功能，只用于已有回归和有限 benchmark |
| 冻结模块 | `fishmesh-analyst`、Diagnostics Context | 只接受安全和构建修复，从默认镜像/部署移除 |
| 历史材料 | 旧 experiments、实验 overlay、原始阶段记录 | 保留可追踪性，不继续扩展为产品 |
| 明确排除 | eBPF、Agent actuator、自研 CRD/Operator、P/D、通用 AI Gateway、共享数据库 | 不进入当前 MVP |

“从主产品移除”不等于本阶段立即删除源代码。先停止新增功能并从默认发布面拆出，待 Lite MVP
稳定后再根据依赖和维护成本决定是否物理删除，避免又进行一次没有用户价值的大规模重构。

## 5. 新的开发顺序

### R6A：真实 KV 信号闭环

直接使用现有双 vLLM K3s 集群：

1. 开启 KVEvents publisher 和 replay endpoint；
2. 使用上游 `llm-d-kv-cache` library 建立最小订阅与索引 spike；
3. 通过 vLLM Render API 获得真实 Token IDs；
4. 验证不同 session 的相同 system prompt 能得到公共 prefix match；
5. 验证 eviction、Pod 重启、断流和 stale 降级；
6. 记录 event lag、render latency、lookup latency 和内存成本。

R6A 是产品风险门禁，不建设新的 simulator，不先设计完整抽象后再寻找信号。

### R6B：KV-aware 能力域

门禁通过后，按“叶子能力先行”实现：

1. tokenization 和 kvcache 契约、值对象及 contract tests；
2. vLLM render 与 KVEvents adapter；
3. routing 的 cache/load 输入和纯策略；
4. requestpath 编排与明确降级；
5. Gateway 请求体有界读取、原始 body 复用和 SSE 透传；
6. llmd adapter 翻译 `PrefixCacheMatchInfo`，不启动第二套索引。

### R6C：Lite 产品化

- 一条命令安装的声明式 overlay；
- 独立 gateway 镜像，不捆绑 analyst/simulator/loadgen；
- 专用 ServiceAccount、最小 RBAC、探针、资源边界、PDB 和安全上下文；
- cache/index freshness、降级、选择原因和资源使用指标；
- 真实集群滚动更新、Pod 删除、事件断流和恢复验收；
- 面向第一次使用者的 demo 与 runbook。

### R6D：性能与开源边界

在同一环境对比 Service、FishMesh load-balanced、FishMesh KV-aware 和 llm-d precise。重点检查 cache-cold
开销、公共 system prompt TTFT、流式吞吐、CPU/RSS、事件延迟和错误选择率，不以扩大 workload
矩阵作为交付。

### R6E：Standard mode 闭环

完成 Gateway/HTTPRoute/InferencePool/EPP 部署，FishMesh scorer 消费 llm-d precise prefix match，
验证 wire-level 503/429、retry served endpoint 和相同输入的策略 conformance。

## 6. 后续代码质量硬约束

R6 会引入请求体、tokenization、事件订阅和索引状态，最容易产生难懂的大函数。后续提交必须：

- 先写同包名契约文件和值对象，再写 contract test 和具体实现；
- 一个编排函数只表达 3–7 个步骤，目标不超过 40 行；
- token 解析、ZMQ 消息、索引更新、freshness 和路由计算分别由明确能力拥有；
- 第三方 llm-d/vLLM 类型只能停留在 adapter，不进入 routing contract；
- 主流程使用编号中文注释，说明为什么和失败时怎样降级；
- 辅助函数按调用顺序排列，禁止把常量、结构体散落在执行流程中；
- goroutine 必须有 owner、cancel、wait 和错误可观测路径；
- 不使用 `common/shared/utils/helpers` 或超大 `Manager` 隐藏职责；
- 一个 domain 内多个子能力/实现，或多个同级 domain，真正共享且语义一致、无外部依赖的
  数据模型，可以放入最近共同 owner 下按概念命名的
  `entity/<concept>` 子包，并让值对象通过 `Validate` 等纯方法维护自身不变量；禁止建立笼统的
  根 `entity` 类型仓库；
- 每个切片同时更新阶段文档、项目状态并通过完整 CI 后提交。

详细规则已写入 [`code-organization.md`](../design/code-organization.md)。

## 7. 本阶段产物

- 新增 [`ADR-002`](../design/decisions/002-lite-kv-aware-routing.md)，正式记录主次关系和 KV-aware
  KV 决策；
- 更新项目章程、架构、实施计划和实验规则；
- 更新中英文 README，区分当前已实现能力与 R6 目标能力；
- 更新 Serving Domain 后续扩展顺序；
- 更新阶段索引和项目当前状态；
- 不修改 Go 行为，不创建或删除集群资源。

## 8. 下一阶段边界

下一阶段只执行 R6A 真实 KV 信号闭环。它可以编写最小 spike、部署参数和可复现验证脚本，但
不能顺便重写整个 Gateway、增加 Redis、扩展 simulator 或安装完整 Envoy 栈。R6A 结果必须先
决定 KV-aware 路径是否可行，之后才进入生产代码。

## 9. 本阶段验证

本阶段虽然只修改文档，仍执行完整仓库门禁，确认方向调整没有破坏已有工程基线：

```text
go test -race ./...  PASS
go vet ./...         PASS
go build ./...       PASS
make manifest        PASS
git diff --check     PASS
local Markdown links PASS
```

验证没有访问或修改 Kubernetes API。集群真实状态沿用 2026-08-11 的只读复核结果；R6A 才会按
声明式配置变更 vLLM KVEvents 参数并记录 rollout。
