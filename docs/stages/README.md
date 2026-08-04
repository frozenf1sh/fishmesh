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
| 05 | P0 方向收敛与实验可信度 | bounded affinity 定位、run metadata、版本升级和可信实验规范 |
| 06 | Bounded Affinity 调度核心 | TTL affinity registry、可解释 spillover 与 freshness fallback |
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

每个阶段完成后都会补充独立中文说明，并在文末明确下一阶段边界。
