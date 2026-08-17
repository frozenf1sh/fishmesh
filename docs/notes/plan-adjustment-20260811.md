# FishMesh 计划调整与背景交接(2026-08-11)

> 本文档给协作的 AI agent:说明项目 owner 的目标与顾虑、当前状态核实、交付边界,以及调整后的开发计划。
> 请先完整读完本文档再调整计划;涉及代码改动时遵守 AGENTS.md 全部规则。

## 1. 项目 owner 的目标与顾虑

### 1.1 目标

项目 owner 正在准备秋招,方向为 AI Infra / 云原生推理。FishMesh 是核心项目谈资,目标是:

- 通过项目完整展示工程能力:Go 并发、K8s 稳定性工程、可观测性、架构纪律;
- 展示技术选型与工程判断力(比算法本身更重要);
- 让项目在面试中"扛得住追问"。

### 1.2 顾虑(近期反复讨论中明确的,请理解这些是真实诉求)

1. **"看起来复杂但实际薄"**:仓库约 9,000 行,但生产交付物只有 scorer(纯策略逻辑约 250 行),standalone 运行时占大部分代码且未来会被开源替代。owner 担心项目"工程量大、内容单薄"。
2. **验证工程难讲**:无 GPU 模拟器、故障 E2E、race 测试这些是项目的真实亮点,但 owner 不确定面试中怎么讲才不显得像"自嗨"。
3. **自研价值怀疑**:开源生态已成熟(Envoy AI Gateway v1.0、GIE InferencePool GA、llm-d Router、GKE Inference Gateway),owner 担心"为什么不用开源"答不好,也担心 scorer 本身价值有限。
4. **是否转向 cache-aware**:owner 认为"prefix-cache-aware 调度"比当前"会话键亲和"更有价值,这是生态热点(Google 报告 cache-aware 路由 TTFT 提升最高 96%)。
5. **"只做一个 scorer"是不是太薄**:生产形态只交付 scorer,owner 不确定这是合理收敛还是项目缺陷。

### 1.3 已达成共识(除非有更强证据,不要推翻)

- **价值叙事** = "策略设计 → 运行时验证 → 标准 EPP 集成(R5C)的完整工程闭环 + 技术选型决策",**不是算法壁垒**。禁止"算力壁垒/护城河"类表述(算法是经典组合,开源已覆盖)。
- **诚实面对未完成项**(R5D 标准部署验证、与开源方案的量化对照 P4 均未完成),面试中主动承认优于虚张。
- **决定执行两条线**:R5D(标准部署 + wire-level 验证)+ 近似 prefix-cache-aware 增量(加厚策略本体)。
- **不做**:自研网关/ext_proc、eBPF 信号、拓扑/PD 分离感知、精确 token-block KV cache 匹配、共享 affinity 数据库(与 project-charter.md 冲突,或超出时间预算)。

## 2. 当前状态核实(2026-08-11,基于代码)

- `2384ee9 feat: 完成 R5C llm-d 调度适配` 已合入 main,llmd 包测试通过。
- 已存在的交付组件:`internal/serving/llmd`(891 行)、`cmd/fishmesh-epp`(150 行)、`deploy/integrated/llmd-config/epp-config.yaml`。
- **llmd 翻译层(`translation_impl.go`)目前只消费 in-flight 与 queue 两个信号**,不含 prefix-cache 信号;`routing` 策略本体约 250 行逻辑,是"会话键亲和 + 阈值溢出",**不是** prefix-cache-aware。
- 集成入口:`EndpointPickerConfig`(llm-d.ai/v1alpha1),plugins 列表 = fishmesh scorer + max-score-picker + single-profile-handler。
- 路由键来源:`X-FishMesh-Session-Key` 请求头由客户端显式提供,Gateway/llmd 原样读取;空 key 退化为 least-loaded;只存 SHA-256 摘要(进程内 TTL registry,5 分钟/1 万条上限)。

## 3. 交付边界(哪些交付、哪些被替代、哪些保留)

| 包/组件 | 行数 | 状态 | integrated 中的角色 |
| --- | --- | --- | --- |
| `internal/serving/routing` | 751 | **交付** | scorer 核心,standalone/integrated 共享 |
| `internal/serving/llmd` | 891 | **交付** | Filter/Scorer/ConsumerPlugin/ResponseHeaderProcessor 适配器 |
| `cmd/fishmesh-epp` | 150 | **交付** | 组合根:注册插件 + 启动 llm-d Runner |
| `deploy/integrated` | — | **交付** | EndpointPickerConfig + 部署清单 |
| `internal/simulator` + 故障 E2E | 478 | **保留** | conformance 验证资产 |
| `internal/serving/gateway` | 1243 | 退役 | → Envoy / Gateway API |
| `internal/serving/discovery` | 768 | 退役 | → llm-d 的 InferencePool 发现 |
| `internal/serving/observation` | 677 | 退役 | → llm-d data layer / metrics |
| `circuit/admission/transport` | 432 | 退役 | → llm-d flow control |
| `internal/serving/requestpath` | 618 | 退役 | → llm-d 请求生命周期(ADR-001 明确不调用) |
| `config/identity` | 831 | 退役 | → llm-d 参数 / 数据层 |

**InferencePool CRD 结论**:`inference.networking.k8s.io/v1` 由 llm-d 运行时消费(负责发现与 subset),**FishMesh 不创建、不 watch、不 import 任何 CRD 类型**;注册入口只用 llm-d 的 `EndpointPickerConfig`。

## 4. 调整后的任务清单(按优先级)

### P0 — R5D:标准部署与 wire-level 验证(预计 2-3 天)

1. 调研 llm-d v0.9.0 的 data layer:确认 endpoint metrics 暴露哪些信号(in-flight / queue / **prefix-cache 相关**?),结论写入 `docs/notes/`。**这是 cache-aware 增量可行性的前置验证**。
2. 在 K3s(`~/.kube/fishmesh.yaml`,命名空间 kubellm)跑通完整链路:EndpointPickerConfig + `fishmesh-epp` + 兼容 Gateway(Envoy)+ vLLM。
3. wire-level 故障验证(复用 simulator 或真实 vLLM):空 subset → 503、请求取消、served endpoint 回写、metrics 过期降级。
4. 更新 `docs/stages/17-R5D-*.md`、`docs/stages/README.md`、`docs/notes/project-status.md`。

### P1 — 近似 prefix-cache-aware 增量(预计 1 周)

1. `internal/serving/llmd/translation_impl.go`:从 llm-d endpoint 数据层读取 prefix-cache 信号(若 P0 确认数据源存在;否则降级为 standalone observation 已有的 Prefix Cache hits/queries 指标),加入 routing snapshot。
2. `internal/serving/routing`:把 prefix-cache 命中率作为 spill 决策的辅助信号(例如:preferred 有 cache 命中且未严重超载 → 容忍更高溢出阈值),保持"溢出不改写亲和"不变量。
3. 两种形态(standalone / integrated)在相同输入下的 selection/reason conformance 测试。
4. 若 P0 确认 llm-d 无 cache 数据源:记录限制,改为仅在 standalone 侧做增量,并说明 integrated 的数据缺口。
5. 目标:策略本体从 ~250 行加厚到 500-700 行,让"策略"成为项目叙事重心。

### P2 — 面试素材固化(与 P1 并行)

1. 每个 P0/P1 完成项都补一段"面试叙事"注记:技术决策 + 权衡 + 一句可背诵的话术。
2. 整理"为什么做近似 cache-aware / 为什么不做精确 block 级"的决策记录。
3. 准备追问应答:"你的 scorer 与 llm-d 内置 scorer 的区别""为什么不做 cache-aware""数据对照(P4)什么时候做"。

## 5. 验收标准

- `go test -race ./...`、`go vet ./...`、`go build ./...`、`make manifest` 全部通过(AGENTS.md 强制)。
- R5D:K3s 上 integrated 链路可运行;空 subset 行为符合 EPP 契约(503);故障验证有记录。
- cache-aware:引入 cache 信号后 standalone/integrated 选择一致;"溢出不改写 affinity"不变量测试保持绿色。
- 每阶段完成即更新 `docs/stages/`、`docs/stages/README.md`、`docs/notes/project-status.md`。

## 6. 约束

- 遵守 AGENTS.md 全部规则:先读 `docs/design/code-organization.md` 与 `docs/design/serving-domain-redesign.md`;行为变更与机械迁移分离;协议名/环境键/指标标签不重复字面量。
- 不新增 shared/common/utils 包。
- 交付物优先保证 `routing` + `llmd` + `fishmesh-epp` 的质量;standalone 退役包不做无谓重构。
- 与开源方案的量化对照(P4)不进入本轮范围,但结论记录里要保留其位置。
