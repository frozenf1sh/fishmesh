# FishMesh 后续阶段:owner 检查清单 + 极简 agent 提示词

> 规格以仓库文档为准(阶段 17-22、ADR-002、plan.md)。本文档只做两件事:
> A. 给 owner 的检查清单(面试前核对交付与验收);
> B. 给 agent 的极简提示词(导航:读哪个文件 + 本次切片 + 验收)。
> 提示词保持精简,不重复文档内容;agent 在仓库内会读取文档。

---

## A. Owner 检查清单(按优先级)

| 阶段 | 交付什么 | 验收(检查点) | 优先级 |
| --- | --- | --- | --- |
| R6B-6 | 组合根装配 + 真实 KVEvents 接入 + 模式开关(默认 session-key) | 真实集群命中演示 + 降级演示记录在阶段文档;UID 清理/断流恢复有 race 测试;CI 全绿 | **面试必需** |
| R6C | 一条命令安装、独立镜像、RBAC/探针/PDB、runbook | 新环境按文档可部署/验证/回滚;demo 含命中/降级对照 | 加分第一 |
| R6D | Service / load-only / KV-aware 有限对比 | ≥3 轮、记录环境边界、结论写明"单卡仅 correctness 证据" | 可选 |
| R6E | Standard mode(llm-d scorer 消费真实 prefix match) | 空 subset→503、一致性测试通过 | 面试后可做 |

面试前核对点:R6B-6 的命中演示要能给出**三个证据线**——R6A 的 128-token 真实命中数据、R6B-5 闭环单测、R6B-6 集群命中/降级演示。

---

## B. 极简 agent 提示词

### 通用约定(所有阶段)

- 规格以仓库文档为准(AGENTS.md、阶段文档、ADR);提示词只指定本次切片与验收,不重复规格。
- 遵守 AGENTS.md 门禁:`go test -race ./...`、`go vet ./...`、`go build ./...`、`make manifest`。
- 完成切片必须同步 `docs/stages/NN-*.md`、`docs/stages/README.md`、`docs/notes/project-status.md` 后提交。
- 遇到诡异问题不猜,记录现象并暂停报告;连续两次未过门禁或排查超半天 → 暂停等升级。

### R6B-6(最高优先级)

```text
你是在 fishmesh 仓库工作的开发 agent。执行 R6B-6。

1. 先读 AGENTS.md、docs/stages/17-R6方向复位与ExactKV主线.md、
   docs/design/decisions/002-lite-kv-aware-routing.md(规格以文档为准);
2. 按阶段 17 第 5 节 R6B-6 完成:组合根显式装配、真实 KVEvents 接入、
   路由模式开关(默认保持 session-key)、降级语义不变;
3. 完成后必须做真实集群端到端演示:两个相同 system prompt、不同 user message、
   不带 session key 的请求,证明第二个命中真实缓存(记录响应头),再记录一个降级场景;
4. 门禁 + 阶段文档(23-*)同步后提交;
5. 诡异问题不猜,记录现象并暂停。
```

### R6C(Lite 产品化)

```text
执行 R6C。先读阶段 17 第 5 节 R6C 任务与最近阶段文档。
完成:一条命令安装 overlay、gateway 独立镜像(拆出冻结模块)、
生产资源边界(SA/RBAC/探针/资源/PDB)、可观测性指标、滚动更新与断流演练、
demo runbook(含命中/降级对照)。
门禁 + 阶段文档(24-*)同步后提交;诡异问题记录暂停。
```

### R6D(有限性能对照)

```text
执行 R6D。先读阶段 17 第 5 节 R6D 任务。
固定环境,对比 Service / FishMesh load-only / FishMesh KV-aware(条件允许加 llm-d);
指标:cache-cold 开销、公共 system prompt TTFT、流式吞吐、CPU/RSS、事件延迟、错误选择率;
≥3 轮重复、随机顺序、记录环境边界(单卡 time-slicing 仅 correctness 证据)。
结论写进阶段文档(25-*),不扩展实验矩阵;门禁后提交。
```

### R6E(Standard mode 闭环)

```text
执行 R6E。先读阶段 17 第 5 节 R6E 任务与 ADR-001/002。
部署 Gateway/InferencePool/EPP;llmd adapter 翻译 llm-d prefix match 到 routing.KVAwareInput
(不启动第二套 KV 索引);wire-level 验证空 subset→503、429、served endpoint 回写;
standalone/integrated 一致性测试。
门禁 + 阶段文档(26-*)同步后提交;诡异问题记录暂停。
```

---

## C. 面试素材整理(最后 1-2 天,自己准备)

1. 30 秒电梯版 + 3 分钟版(结构:背景→问题→方案→工程→验证→不足)。
2. 三条证据线:R6A 128-token 真实命中 / R6B-5 闭环单测 / R6B-6 集群命中+降级演示。
3. 追问应答:"为什么不用开源"(Lite 面向小集群、Standard 复用生态、KV-aware 有区分度)、
   "和 llm-d 内置 scorer 区别"(组合语义 + 真实 KVEvents + 故障验证,不是算法更强)、
   "为什么不做精确 block 匹配之外的东西"(章程边界)。
