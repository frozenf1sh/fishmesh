# 阶段 48：路由维护边界与 Load-aware 升级

## 1. 范围

本阶段先固定产品维护边界：`load-balanced` 与 `kv-aware` 进入后续负载和容量升级主线；
`session-key` 保留协议、配置、llm-d 插件和历史 overlay，但标记为 frozen compatibility mode。

冻结不等于立即删除。现有用户可以继续回滚或验证 `session-key`，但除安全、构建和兼容性修复外，
不再为该模式增加新的路由能力、指标语义或性能优化。

## 2. 决策

- 普通负载均衡后续升级为 `load-aware`，优先消费新鲜的 vLLM queue/running，再使用 Gateway
  local in-flight 作为补偿和最终 fallback。
- KV-aware 信号缺失时仍回退到普通负载均衡；该 fallback 必须复用同一份 load-aware 选择逻辑。
- Service fallback 只处理 discovery、circuit 或策略候选完全不可用，不作为 KV signal 的直接替代。
- 实际选定 vLLM 的 transport/stream failure 不在当前请求内自动换 backend；通过 circuit 影响后续选择。

## 3. 本阶段变更

- 在 routing contract 和 llmd package comment 中标记 session-key 为 frozen compatibility mode；
- 在项目章程、架构说明、中英文 README 中同步维护边界；
- 保留 `session-key-v1`、环境变量、EPP 参数和实验 overlay，避免破坏历史部署与回滚。

## 4. 验证与下一阶段

本阶段不改变运行时选择行为。后续阶段将把 `load-balanced` 和
`kv-aware-load-fallback-v1` 统一到新鲜 vLLM load 优先的纯 routing contract，并补充 queue/load
缺失、硬过载和 local fallback 测试。
