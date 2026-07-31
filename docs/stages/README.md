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

每个阶段完成后都会补充独立中文说明，并在文末明确下一阶段边界。
