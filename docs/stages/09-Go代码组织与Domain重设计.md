# 09｜Go 代码组织与 Domain 重设计

状态：规范与目标设计完成；代码迁移尚未开始。

## 为什么先停下来整理代码

P1 补齐可靠性后，Gateway 已经能正确处理 admission、circuit、取消、流式失败和 endpoint GC，
但这些能力大多集中在一个 651 行的 `server.go` 中。继续直接加入 simulator 和 EPP adapter，
会让新能力继续依赖这个大文件，之后更难拆开。

所以这一阶段不增加功能，也不为了目录整齐立即搬代码。先明确每个包拥有什么、允许依赖谁、
一个文件先写什么后写什么，再按小提交迁移。

## 对附带方案的判断

保留的部分：

- 每个能力使用独立 domain 包；
- 契约文件与实现文件角色明确；
- 原子 domain 到编排层保持单向依赖；
- 编排函数只展示步骤，算法和外部协议下沉；
- 文件内固定为常量、类型、构造、导出方法、私有方法、辅助函数。

调整的部分：

- 不新建顶层 `internal/domain`，继续保留 Serving/Diagnostics/Workload 上下文；
- 不新建 `shared`，Backend、Observation、Decision 分别回到真正的 owner；
- 不强迫每个包创建只有一个实现的接口；
- Diagnostics 已冻结，不为了风格统一进行无收益迁移。

## 新增的长期约束

- `docs/design/code-organization.md`：强制包、文件、声明顺序、接口、字面量、并发和测试规范；
- `docs/design/serving-domain-redesign.md`：目标 domain、类型所有权、依赖 DAG 和 R0–R5 迁移顺序；
- 根目录 `AGENTS.md`：要求后续开发者和 AI 在修改 Go 代码前阅读上述规范。

## 目标流程

完成重构后，`cmd` 只创建和注入能力，Gateway 只处理 HTTP/SSE，RequestPath 只按以下步骤编排：

1. 读取后端、观测和本地状态 snapshot；
2. 应用 discovery freshness 与 circuit eligibility；
3. 调用纯 routing strategy；
4. 登记一个可回收的 backend lease；
5. Gateway 转发请求并用 typed outcome 完成 lease。

## 这次没有改变什么

- 没有修改 Go 运行代码、route reason、配置或部署清单；
- 没有重启 K3s，也没有重新运行 GPU 实验；
- P1 的实现和验证结论保持不变；
- P2 仍是 simulator E2E 与 EPP/llm-d 集成，只是在开发功能前先建立可复用边界。

## 下一步

执行 R1：先拆最底层类型与纯策略。这个阶段只迁移 Backend、Observation/Sample、Routing 和
Circuit 的所有权，加入 import 架构测试；每个提交保持 race tests、构建和清单渲染通过。
