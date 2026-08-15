# FishMesh 阶段记录

本目录按实施阶段编号，记录“做了什么、为什么这样做、如何验证、还剩什么”。代码设计
总览仍在 `docs/design/architecture.md` 和 `docs/design/plan.md`；本目录面向第一次阅读
项目的人，按编号顺序阅读即可。

| 编号 | 阶段 | 结果 |
| --- | --- | --- |
| 01 | 真实观测适配器与诊断缺口 | vLLM/GPU/Kubernetes 只读信号和 `insufficient_observability` |
| 02 | EndpointSlice 动态发现 | Ready 地址 watch、稳定 backend ID、Service fallback |
| 03 | Backend Snapshot | 每个 vLLM endpoint 的指标、状态和 freshness 对齐 |
| 04 | 身份映射与故障状态 | Pod→Node/GPU request 映射、discovery freshness 和 readiness 门控 |
| 05 | P0 方向收敛与实验可信度 | session-key 定位、run metadata、版本升级和可信实验规范 |
| 06 | Session Key 调度核心 | TTL session-key registry、可解释 spillover 与 freshness fallback |
| 07 | 工程优先方向章程 | 可靠性与标准集成为主线，实验降为决策和验收工具 |
| 08 | 请求路径可靠性 | admission、transport circuit、endpoint state GC 和 per-field sample |
| 09 | Go 代码组织与 Domain 重设计 | 强制代码规范、类型所有权、目标依赖 DAG 和渐进迁移顺序 |
| 10 | R1 核心类型与依赖方向 | Backend/Observation/Circuit owner、typed routing contract 和 import 门禁 |
| 11 | R2 原子 I/O 能力边界 | discovery 与 I/O contract/impl 分层、协议常量收口 |
| 12 | R3 请求路径编排 | 幂等 lease、显式 fallback、membership 与 circuit 生命周期 |
| 13 | R4 显式组合根与 Gateway 交付边界 | domain 配置、依赖注入、HTTP/SSE delivery 与反序释放资源 |
| 14 | R5A 无 GPU Simulator 与故障 E2E | 可控 SSE/vLLM 契约、动态 EndpointSlice、取消与可靠性自动验证 |
| 15 | R5B EPP 与 llm-d 集成决策 | 选择 pinned llm-d scorer，拒绝自研 EPP 与双重 requestpath |
| 16 | R5C llm-d 适配器与 EPP 组合根 | 编译期 Filter/Scorer、上游 runner、选择 conformance 与最小配置 |
| 17 | R6 方向复位与 KV-aware 主线 | Lite 主产品、真实 KV cache 决策、冻结边界与 R6A–R6E 路线 |
| 18 | R6A 真实 KV 信号闭环 | Render/KVEvents/replay、跨会话 128-token 命中、eviction/restart 与 R6B 门禁 |
| 19 | R6B-1 真实分词能力域 | 不可变 prompt profile、vLLM Render adapter、typed degradation 与资源上限 |
| 20 | R6B-2 真实 KV 状态域 | 有界索引、同步 sequence、replay freshness、cache salt 与 Pod UID 回收 |
| 21 | R6B-3 缓存/负载联合纯路由 | kv-aware 值契约、硬过载保护与 requestpath 显式 load-balanced 降级 |
| 22 | R6B-4 请求路径 KV-aware 编排 | TokenIDs/KV Match 投影、KV-aware 输入构造与显式 load-balanced 降级 |
| 23 | R6B-5 有界 Body 与 KV-aware 交付 | body replay、SSE 透传、KV-aware 状态/决策头与可重复闭环测试 |
| 24 | R6B-6 组合根真实 KV 接入 | EndpointSlice/Pod UID 组合、真实 ZMQ/replay、单组 full-attention 兼容与集群双请求命中 |
| 25 | R6C Lite 产品化 | Gateway 独立镜像、Lite KV-aware overlay、最小 RBAC/资源边界、KV 指标与滚动/断流恢复演练 |
| 25-R6D | 有限性能对照 | Service/load-balanced/KV-aware 各三轮默认 loadgen profile、边界结论与 session-key 恢复 |
| 26-R6D2 | 前缀长度分段对照 | 修复 rollout 后 KV subscriber 生命周期；受控 c=1 的 512/2048 Service/KV-aware 对照、温度纪律与无拐点结论 |
| 27 | Lite 监控与故障 Runbook | 可导入 Grafana dashboard、Prometheus rule 配置与证据优先的故障处置；参考集群尚未启用或验证监控栈 |
| 28 | Lite 五分钟 Demo 与完成度对齐 | 中英文 README 明确 session-key 默认值、KV-aware 演示及未验证监控边界；Standard mode 后置 |
| 29 | Lite 发布与回滚说明 | 多架构/SBOM 发布流程、版本矩阵和 digest 驱动的升级与回滚；仅 amd64 离线路径已实证 |
| 30 | Lite 监控栈部署与接入 | Prometheus/Grafana 实装、真实 Gateway scrape/规则/dashboard 验证；未配置外部通知 |
| 31 | R6F KVEvents 与逐请求观测契约 | 成功 apply 的 publisher-to-apply histogram、available-only cached-prefix histogram 与真实双请求验收 |
| 32 | R6G Go 对话与压测客户端 | 独立 chat/request/bench、隐私安全 history/JSONL、终端色彩诊断与 session-key 真实短验收 |
| 33 | R6H 成本式 KV-aware 路由 | 以等价未缓存 token 合并 cache/queue/running/local in-flight；GPU 集群验收后置 |
| 34 | R6H-2 预测 TTFT 影子契约 | 逐 backend 有界非负拟合、首 SSE TTFT 回写和零副作用 would-select；GPU 验证后置 |
| 35 | R6G 默认历史路径修复 | `chat` 未指定 `--history` 时自动使用私有 UTC 时间戳 JSON 文件，并覆盖真实 CLI 对话保存 |
| 36 | Routing 三策略收敛与命名迁移 | 保留 `load-balanced`、`session-key`、`kv-aware`；移除纯 prefix hash/独立 Service 选路，统一协议、配置与部署命名 |
| 37 | Serving 默认配置收口 | standalone 产品默认值集中到 `config.DefaultConfig()`；domain 构造函数不再偷偷补业务默认值 |
| 38 | 最终压测矩阵与仓库收缩 | `fishmesh-client bench` 覆盖长度/数量/批次/前缀模式并自动报告；移除历史测试入口，llm-d 暂挂 |
| 39 | R6H Render 降级与长上下文压测准备 | 修复冷 Render DNS/连接导致的选路前 503；完成真实 mixed 的 1568 请求反向顺序 A/B，TTFT P95 降低 76.7%、总耗时 P95 降低 55.0% |
| 40 | R6I-0 可校准 TTFT 路由设计 | 固化 calibrated-static → learned-shadow → active gate 路线；先修 load/HardOverload，再做 token 阶梯与缓存隔离实验 |
| 41 | R6I-1 负载观测与 HardOverload | Lite 启用有界 queue/running 抓取，requestpath 发布真实硬门，并消除 external load 与 local in-flight 重复成本 |
| 42 | R6I-2 静态 TTFT 估算契约 | 版本化二维 prefill profile、毫秒 load cost、typed confidence/fallback 与纯函数门禁 |
| 43 | R6I-3 静态 TTFT 路由接入 | requestpath 投影毫秒 estimate，routing 在安全门后选择，证据不完整时原子回退 token cost |
| 44 | R6I-4 决策证据与对比报告 | 固定数值 headers/JSONL、低基数 estimator 指标与多轮 bootstrap A/B 汇总 |
| 45 | R6I-5 缓存隔离与 Token 工作负载 | cache salt/run nonce、三种 cache 状态、实际 token 门禁与完整 provenance |
| 46 | R6I-6 校准与 Token 阶梯 | 512–3072 token profile、并发阶梯、local-delta/原子 reservation 修正；static 未通过 active 门禁 |
| 47 | R6I-7 Learned-shadow 实现与门禁 | 有界重拟合、低基数 shadow 证据、双轮真实实验门禁与可回滚 overlay |
| 48 | 路由维护边界与 Load-aware 升级 | `session-key` 冻结为兼容模式，load-balanced/KV fallback 进入 load-aware 主线 |
| 49 | Load-aware 普通均衡与 KV 降级 | 普通策略优先消费完整 vLLM queue/running，KV fallback 复用同一 load-aware 选择 |
| 50 | Little’s Law 请求观测契约 | 区分 admitted、in-flight、completed/rejected，为 QPS 与并发计算提供低基数事实 |
| 51 | Open-loop QPS 压测计划 | benchmark 支持可选 arrival rate，并明确 offered rate、worker 饱和与 Gateway accepted rate 的边界 |
| 52 | 压测完成窗口与 Little’s Law 边界 | attempt/batch/scenario 记录实际完成窗口与 completed QPS，继续隔离 Gateway accepted rate |
| 53 | Gateway 指标窗口与 Little’s Law 取数 | 可选采样 admitted/completed/in-flight，计算 accepted QPS、平均并发与 Little’s Law W |
| 54 | Gateway 指标分段窗口 | 排除 warmup 与 scenario gap，按 active duration 聚合 Gateway accepted/completed/in-flight |
| 55 | Tokenization 与 KV 并发边界 | Tokenize 与 KV reconcile 并行，KV Lookup 等待 Token IDs，保持显式降级和串行 reservation |
| 56 | 容量阶梯与拒绝证据 | 增加 admission rejection rate、batch 分段窗口和 1/2/4/8/16/32 QPS open-loop 模板 |
| 57 | 容量基线与对照实验契约 | 固化 admission/routing 对照轴、动态阶梯、路由消融和长连接 drain 实验模板 |
| 58 | Pod 身份运行时指标观测 | 以 Pod name/UID 归属 CPU、内存和可选 GPU runtime sample，并暴露 freshness-aware 低基数指标 |
| 59 | 动态 Admission 影子控制 | 分离 hard limit/soft target，提供 off/shadow/active、连接安全语义和控制器指标 |
| 60 | 动态 Admission 实验部署与长连接安全 | 提供 shadow/active Kustomize overlay、SSE drain 和 stale signal 验收步骤 |
| 61 | 运行时负载硬安全门接入路由 | 将 fresh、Pod-mapped runtime sample 作为可选 overload gate，保留缺失信号降级和 availability-first |
| 62 | 容量报告与 A/B 收益汇总 | 把 Gateway 窗口下沉到 scenario/batch，并扩展 compare 汇总 accepted/completed/rejected 与 Little’s Law |
| 63 | 容量、Admission 与 Runtime 实验手册 | 固化执行顺序、长连接安全、产物留存、收益判定和停止/回滚条件 |
| 64 | 真实容量、Admission 与路由收益验收 | 完成真实 A0/A1/A2、B1/B2 与长连接对照；确认 active 背压边界、KV locality，runtime 观测仍缺 Pod 维度 |
| 65 | Admission 反馈修正与 KV 短上下文旁路 | 拆分 soft/hard rejection，阻断控制器自反馈降级；增加可观测的短上下文 KV lookup bypass |
| 66 | R6I-24 Short-context threshold calibration | 用实际 token 校准阈值，完成 512/1024/2048/3072 sweep 与 threshold 576 双轮 repeat；确定 profile 固定策略 |

每个阶段完成后都会补充独立中文说明，并在文末明确下一阶段边界。
