# FishMesh 项目当前状态

最后项目状态更新时间：2026-08-16；集群最后真实验收时间：2026-08-16。仓库为
`frozenf1sh/fishmesh`（private），当前 `main` 分支
包含可信 serving baseline、两条维护中的 routing 主线（`load-balanced`、`kv-aware`；
`session-key` 仅为 frozen compatibility mode，对应策略标识仍为 `load-balanced-v1`、`session-key-v1`、
`kv-aware-v1`）、request-path reliability
和工程优先的项目章程。

Serving 默认配置已收口到 `internal/serving/config.DefaultConfig()`：环境变量只覆盖这一份
standalone 产品默认，domain 包不再拥有生产 `DefaultConfig` 或对关键业务零值静默补值。运行时依赖
（clock、HTTP client）以及 Kubernetes 协议端口等 adapter 级兜底仍保持在实现边界内。

本次 routing 收敛已完成：普通负载均衡使用 `load-balanced`，真实 KV locality 与已知负载联合选路使用
`kv-aware`；客户端传入 key 的有界粘性 `session-key` 仅作为冻结兼容模式保留。纯 prefix hash 和独立
Service 选路不再是可配置策略；Service 只保留为 discovery/策略失败时的最终 fallback。请求头、
环境变量、指标、llm-d 参数、部署 overlay 和文档均已同步到这组三策略命名。

request-path reliability、Serving Domain R1–R4、无 GPU simulator 基础、R5C llm-d 本地集成和
R6A 真实 KV 信号门禁、R6B-1 至 R6B-6、R6C、R6D 与 R6D2 已完成：真实 Render、同步 KV index、pure routing、
requestpath 编排、有界 body/SSE delivery、组合根、真实 ZMQ/replay 和可安装 Lite 产品面均已闭环。R6C
在真实双 vLLM 集群以不带 session key 的两个相同 system prompt、不同 user message 请求，记录到第二请求
`available`、`kv-aware-v1` 和 80 cached prefix tokens；删除实际命中 Pod 后，过渡请求显式降级、
替代 Pod replay 后恢复 KV-aware 命中。R6D 以每组 3×200 默认 loadgen requests 对比 direct Service、
load-balanced 与 KV-aware：KV-aware 保持 explicit degradation correctness，但在单卡短生成 profile 的 TTFT/吞吐
不优于 Service，结论仅限 profile evidence。R6D2 修复 vLLM rollout 后 EndpointSlice membership 未主动换代
KV subscriber 的缺口，并以独立 cache generation、单 Pod prewarm 的 c=1 控制完成 512/2048 两档
Service/KV-aware 对照：KV-aware P50 TTFT 分别低 21.89%/20.65%，但绝对收益 6.845/6.580 ms 均未超过约 10 ms
routing overhead，故没有声称 prefix breakpoint。R5D 标准 Gateway/EPP 部署不取消，但后移到 Lite KV-aware MVP 之后。
方向基准见 `docs/design/project-charter.md`，新决策见 `docs/design/decisions/002-lite-kv-aware-routing.md`。

仓库基础配置默认为 `load-balanced`；`X-FishMesh-Session-Key` 只在显式 `session-key` 模式下作为
客户端提示。历史参考集群在 R6D2 收尾后曾恢复 `deploy/experiments/r6d-session-key`，本次已为 R6H-3
临时切换到 `deploy/experiments/long-context-kv-aware`；压测结束后按证据恢复基线。KV-aware 模式通过
EndpointSlice/Pod UID 建立实例、订阅 KVEvents/replay；unknown/stale 永不伪装成零命中。

Lite 监控已在参考集群完成部署和接入：`deploy/monitoring` 创建 namespace-scoped Prometheus、Grafana、
持久卷、dashboard、rule 文件与最小服务发现 RBAC。Prometheus 的 Gateway target 为 `up`，四条规则均已
加载且 inactive；Grafana 已 provision Prometheus datasource 和 `FishMesh Gateway` dashboard。该最小栈不
部署 Alertmanager/外部 notification receiver，故规则可见且会评估，但不声称已完成值班告警投递。接入和
体验步骤见 `deploy/monitoring/README.md` 与 `docs/notes/runbook.md`。

R6F 已补齐性能归因所需的最小观测契约：只有带有效 publisher timestamp、已成功同步 apply 的 KV
event batch 才记录 `fishmesh_gateway_kv_event_publish_to_apply_seconds`，并以 `live`/`replay` 分开；
它是 publisher-to-apply lag，不是网络 RTT，replay 可能包含历史 event 的较大年龄。只有
`X-FishMesh-KV-Status: available` 的选择才记录 `fishmesh_gateway_kv_aware_cached_prefix_tokens`；其中 0
是真实零命中，unknown/stale 不会被计作零样本。真实两请求验收记录首请求 `available/0`、第二请求
`available/768`，cached-prefix histogram count/sum 为 `2/768`；live event histogram 有 4 个样本、
sum `0.002878708s`。验收后恢复 session-key，未将该低负载 evidence 扩展为性能结论。

R6G 的最终客户端收敛为独立的 `fishmesh-client`：`chat` 在未指定路径时以 UTC 纳秒时间戳自动创建
`~/.local/state/fishmesh/<timestamp>.json`，`request` 用于单次 SSE smoke，`bench` 读取 JSON 计划或内置矩阵，
覆盖多种 prompt 长度、请求数量、批次、同前缀、不同前缀和混合前缀。每次运行自动输出 `plan.json`、无 prompt
内容的 `requests.jsonl`，以及按场景/批次聚合的 `report.json` 和 `report.md`；KV unavailable 不会被计作零命中。
API key 只从 cmd 组合根的 `FISHMESH_API_KEY` 出站使用，压测产物不包含 key、prompt、raw SSE 或任意 upstream
headers。最终测试只使用该客户端，不再扩展旧 loadgen。

本次清理移除了 analyst、Diagnostics、simulator、旧 loadgen、一次性 R6A 探针及其默认 validation/job 入口；
历史结论保留在阶段记录和 Git 历史。`fishmesh-epp`、`internal/serving/llmd` 与
`deploy/integrated/llmd-config` 保留但从默认 `make manifest` 中挂起，待 Standard mode 单独恢复。

R6G 的面向人诊断已补充终端色彩：默认 `auto` 只在 TTY 突出 TTFT、policy、reason、KV-aware status 和 backend，
重定向输出及 JSONL 仍为纯文本，可由 `--color always|never` 显式覆盖。当前长上下文 profile 不发送
session key；无 session key 的请求会由当前 routing mode 决定，故改动 system prompt/history 不应被解释为应切换
upstream 的信号。

R6H 已完成成本式 KV-aware 路由改造：`kv-aware-v1` 将未缓存 token、有效 vLLM queue/running 和 Gateway
local in-flight 显式折算为等价未缓存 token，仍先排除 hard overload，仍将 KV unknown/stale 降级而不当作
零命中。GPU 节点恢复后已部署 `r6h-degrade-r1`，并按受控 profile 保留成功率、失败分类、显存和温度证据。

R6H-2 已完成本地预测 TTFT 的影子观测契约：新的独立 `prediction` domain 对每个 backend 保留有界、
15 分钟内的数值首 SSE TTFT 样本，以非负 ridge 最小二乘拟合未缓存 token、queue、running 和 Gateway
local in-flight；至少 16 个样本且所有候选 load 已知才返回 `would-select`。它不读取或记录 prompt、
Token IDs、routing key 和 SSE 内容，不进入 `routing`，不改变 `kv-aware-v1` 的实际选择、
hard-overload 或 unknown/stale 的 load-balanced 降级。默认 `FISHMESH_PREDICTION_MODE=off`；`shadow` 只增加
低基数状态/误差指标，仍不产生 HTTP 决策变化。GPU 恢复后先用影子数据验证误差和安全边界，再决定是否新开
实际预测选路切片；当前预测模式仍为 off，不改变实际选路。

R6H-3 修复了 KV-aware 选路前 Render 的冷 DNS/连接突发问题：临时 Render failure/timeout 现在显式降级为
load-balanced，请求形状错误和调用方取消仍硬失败；专用 Render client 使用 16 个有界连接、30 秒 DNS
缓存、并发解析 singleflight 和绝对 FQDN 查询。参考集群当前运行 `long-context-kv-aware` profile：64 个
in-flight、16 个数据面连接、keepalive=true。使用 4 KiB/12 KiB prompt、6 个场景、2 个批次共 288 请求时
288/288 成功，TTFT P50/P95 为 85.17/479.83 ms，总耗时 P50/P95 为 234.25/843.63 ms；288 个请求均为
KV available，未观察 Gateway 503、admission rejection 或 upstream error。12 KiB 仍指 prompt 字节数，vLLM
`max-model-len=4096` 未改变，不能把这轮结果解释为 12K token 能力证明。

R6I-0 已固定下一阶段不直接把未验证的动态权重放入请求路径：先建立快速 load observation 和
HardOverload，再交付以毫秒为单位的 calibrated-static TTFT estimator，现有 learned model 继续 shadow，
只有误差门禁通过后才讨论 active。R6I-1 已实现并完成最小真实 smoke：Lite 使用
`500ms/2s/400ms` 的 queue/running observation，queue 16 或单 backend local in-flight 32 触发硬门；
外部 load 完整有效时不再重复叠加完整 local in-flight，未知时仍保留 local fallback。基础默认门槛为零，
因此 load-balanced profile 保持兼容。新镜像 `r6i1-load-gates-r1` 的 manifest digest 为
`sha256:d8db3eac20b9cedadbec853793f16fb56a73bcd209986d9e79946d3fb4854a35`；rollout 后 GPU Node 保持 Ready、
vLLM 2/2、Gateway 1/1，两 backend observation 均为 `ok`。8 个受控流式请求把一个 backend 的 running
sample 从 0 提高到 8，结束后恢复 0；queue 未堆积，因此没有声称真实 HardOverload 已触发或 TTFT 已改善。

R6I-2 已建立不参与实际选路的 calibrated-static TTFT 纯契约：profile 绑定 model、hardware、vLLM、
prompt range 和版本，以 prompt token × cached-prefix ratio 二维单调网格插值 prefill duration，再加
queue/running 或 local fallback 与 safety margin。未校准 profile 只能返回 `uncalibrated`，load unknown
返回 `degraded`；身份不匹配、越界、非单调 profile 和 duration overflow 均显式拒绝。本阶段没有改变
`kv-aware-v1` 的实际选择，也没有访问 GPU。

R6I-3 已完成 static estimator 的请求路径接入：显式 `static-ttft` 模式从不超过 1 MiB 的 JSON profile
构造 immutable estimator，requestpath 投影逐 backend estimate，routing 在 circuit/HardOverload 之后选择
最小 TTFT。完整选择标记 `kv-aware-ttft-static-v1/kv-aware-static-ttft`；候选 estimate 缺失、无效或
uncalibrated 时，整次选择原子回到 `kv-aware-v1/kv-aware-static-fallback`，不混合量纲。基础与当前 Lite
manifest 仍为 `token-cost`，本阶段未 rollout、未改变当前集群流量。

R6I-4 已补齐逐请求可审计证据：固定 `X-FishMesh-*` allowlist 和 benchmark JSONL 记录实际
prompt/cached/uncached token、static estimate、confidence/version/reason、load age、queue/running、
local in-flight 与 HardOverload 候选数，不记录 prompt、Token IDs 或 prefix identity。Prometheus 新增
低基数 static selection/estimate/absolute-error 与 HardOverload outcome 指标；`fishmesh-client compare`
可从多轮 request JSONL 生成 pooled P50/P95/P99、run-median P95、estimator MAE/P95 error 和固定 seed 的
bootstrap 95% CI。本阶段未访问 GPU，完整 cache generation/provenance 留到 R6I-5。

R6I-5 已把正式 benchmark 升级为缓存隔离协议：`cold` 每请求使用独立派生 `cache_salt`，
`controlled-warm` 的 warmup 与同 prefix group 正式请求复用 salt 并验证 backend provenance，
`steady-warm` 在独立 run namespace 内自然复用。每个 isolated run 固定 workload seed、执行顺序、
treatment、唯一 nonce 和 vLLM cache generation；raw JSONL 不保存派生 salt。场景现在以 Gateway 返回的
实际 prompt token 对目标/tolerance 做硬验收，报告 token min/P50/P95/max、缺失和越界。`formal=true`
还要求 Git/image/Pod UID/vLLM/model/config/profile provenance。static profile 同时区分 4096 model capacity
和例如 3072 的 calibrated prompt 上界，未测区间不会被外推为 calibrated。本阶段未访问 GPU。

R6I-6 已在 RTX 4060 双 time-sliced vLLM 参考集群完成 512/1024/2048/3072 prompt token 校准与
1/4/8/16 并发阶梯。低负载 static cold/warm estimator MAE 为 2.34/5.44 ms；2048-token 长生成阶梯中
static 和 token-cost 均 120/120 成功，但 static TTFT P95 为 132.64 ms，对照为 128.62 ms（+3.13%，
bootstrap 95% CI [-3.98%, +9.38%]），estimator MAE 为 27.57 ms，故未通过 active/default promotion
门禁。实验同时修复 sampled running 的 local-delta 盲区和并发选择/reservation 非原子问题，并以 load
bounds 禁止 static 向未测 queue/pressure 外推。当前参考集群已恢复 `r6i6-calibration-token-cost`：GPU
Node Ready、vLLM 2/2、Gateway 1/1，真实请求返回 200；最终 Gateway rollout 后两个既有 vLLM generation
的 replay 分别出现 awaiting/unrecoverable sequence gap，故请求显式降级为 `kv-aware-load-fallback-v1`，
尚不构成 KV replay 恢复验收。为避免扰动健康 GPU，本轮没有重启 vLLM 掩盖该缺口。下一步先单独复验 replay，
再只做 R6I-7 learned-shadow；若误差不能稳定优于 static，不增加在线动态权重复杂度。完整证据见
`docs/experiments/2026-08-16-r6i6-token-ladder.md`。

R6I-7 的 learned-shadow 实现已完成：prediction 仍只记录已选 backend 的首个 SSE TTFT，样本有界、过期
清理，模型按固定完成样本数重拟合，非负系数带特征量纲上界；Gateway 与 `fishmesh-client` 新增固定低基数
shadow 证据头和 JSONL 字段，`compare` 可汇总 learned/static paired error 与 would-select 一致率。当前已用
声明式 `r6i7-learned-shadow` overlay 部署 shadow + static 研究切片；实验前因一个旧 vLLM generation 的
sequence gap 停止压测，随后按 PDB 逐个滚动重建 vLLM，两个 endpoint 已恢复 `kv_cache_status=ready`。正式门禁
结果和是否允许 active 以 R6I-7 实验报告为准；R6I-6 的并发 promotion 失败仍是 active 的独立阻断条件。

R6I-7 真实双轮结果已完成：每轮 160/160 成功、160/160 KV available、prompt token evidence 0 缺失/越界，
每个 backend 每轮有 60–66 条可用 shadow 记录。剔除实际 3078 token、超出 static 3072 上界的档位后，
learned MAE 两轮约比 static 低 10.4%，P95 absolute error 均不高于 static；但 would-select 一致率从
第一轮 71.1% 降到第二轮 62.7%，未通过两轮稳定性门禁。R6I-7 只保留 shadow 研究能力，不允许 active；实验
报告见 `docs/experiments/2026-08-16-r6i7-learned-shadow.md`。实验完成后已应用 token-cost + prediction off
恢复 overlay，Gateway 1/1、vLLM 2/2、GPU node Ready，两个 KV instance 均为 ready/valid。

R6I-8 已完成第一步 load-aware 主线：`load-balanced` 在完整 vLLM queue/running 观测有效时按 queue、
running、local delta 和 local in-flight 进行确定性比较，观测不完整时退回本地事实；hard-overload 候选在
存在替代项时先排除。KV-aware signal unavailable 的 `kv-aware-load-fallback-v1` 复用这份普通策略，
不再因为 Render/KV signal 失败而无条件丢弃仍新鲜的 vLLM load。Little’s Law/QPS 观测和容量结论留待下一阶段。

R6I-9 已补齐 Gateway 侧 Little’s Law 最小观测契约：`admitted_requests_total` 只统计通过 admission
的请求，`inflight_requests` 表示当前请求路径占用，`requests_total{status}` 保留结束状态，
`admission_rejections_total` 单独记录容量拒绝。当前只完成观测，不把静态 `MaxInflight` 宣称为容量结论；
固定 arrival-rate 的 open-loop benchmark 留待下一阶段。

R6I-10 已为 benchmark 增加可选 `arrival_rate_qps`：正数场景按目标间隔投递 jobs，零值保留原有
closed-loop fixed-concurrency 行为。该值明确记录为 offered rate；客户端 worker 饱和时不把它冒充
Gateway accepted QPS，真实 QPS 仍须由 Gateway admitted/completed counters 和时间窗口计算。

R6I-11 已为 benchmark attempt、batch、scenario 和全局 report 补齐客户端完成窗口：记录起止时间、elapsed
和 `completion_rate_qps`，并在 Markdown 中同时展示 offered arrival QPS 与 completed QPS。completed rate
包含成功和失败的已结束请求，只描述 workload 侧完成事实；它不替代 Gateway `admitted_requests_total`，
也不直接构成容量或 Little’s Law 结论。下一阶段若做真实容量实验，必须在同一稳定窗口 join admission
counter、in-flight、完成 counter 与 vLLM queue/running。

R6I-12 已为 `fishmesh-client bench` 增加可选 `--metrics-endpoint` 与采样间隔：读取 Gateway
`admitted_requests_total`、全状态 `requests_total` 和 `inflight_requests`，在 report 计算 accepted/completed
QPS、时间加权平均 in-flight 与 Little’s Law `W=L/lambda`。采集失败、counter reset 和窗口不足只标记
Gateway metrics invalid，不中断 workload。该采集仍不读取容器/GPU runtime 指标，也不改变在线路由。

R6I-13 修正 metrics window 生命周期：每个 scenario 在 warmup 后、正式 batch 前独立采样，结束后停止，scenario
之间 pause 不进入 active elapsed；全局 accepted/completed delta 求和，平均 in-flight 按 active duration 加权。
报告保留 warmup 计划数并标记 `warmup_excluded=true`，因此正式容量窗口不再被 warmup 或 scenario gap 污染。

R6I-14 修正 KV-aware 请求路径的并发边界：Tokenize 与 KV instance reconcile 现在在 request-scoped context
下并行；KV Lookup 因依赖真实 Token IDs 仍在两者完成后执行。调用方取消继续硬失败，Render/reconcile 的可降级
故障继续复用 `kv-aware-load-fallback-v1`；最终 snapshot、decision 和 local reservation 没有放宽串行提交。

R6I-15 完成容量阶梯取数闭环：Gateway metrics window 新增 admission rejection delta/rate，采样改为按正式
batch 分段，因此 warmup、batch pause 和 scenario gap 不进入 active elapsed；多段的 accepted/completed/rejected
rate 与时间加权 in-flight 使用同一窗口。新增 `configs/capacity-ladder.json`，覆盖 1/2/4/8/16/32 offered
QPS；它是需替换 run nonce、vLLM generation 并配合 `--metrics-endpoint` 的模板，尚未形成真实 GPU 容量结论。

R6I-16 固化后续容量与路由对照实验契约：新增动态 admission 阶梯、路由消融和长连接 drain 三份 workload
模板，明确 A0/A1/A2 admission 对照、B0/B1/B2/B3 路由对照、正式窗口边界和必留证据。该阶段只准备实验，
尚未启用动态控制，也没有形成新的真实 GPU 收益结论。

R6I-17 完成可选的 Pod identity runtime observation：EndpointSlice/Pod identity 现在保留 Pod UID，Prometheus
HTTP API runtime collector 仅接受同时包含 `$namespace` 和 `$pod` 的查询，按 backend 暴露 CPU、内存和可选
GPU utilization/memory/temperature sample 及 freshness。该能力只作为观测证据，默认关闭，不改变 routing 或
admission；没有 Pod 维度的节点级 GPU 指标保持 unavailable，尚未完成真实 DCGM/容器 exporter 验收。

R6I-18 完成动态 admission 控制器和 shadow 证据：`MaxInflight` 保持不可突破的 hard limit，soft target 支持
off/shadow/active；target 下调只限制新请求，不撤销已有 permit 或中断 SSE。控制器使用 Gateway 单调计数，
对 stale/reset signal 保守冻结，并以滞回、步长和 cooldown 避免振荡。默认部署仍为 off，尚未切换 active 或
形成真实 GPU 收益结论。

R6I-19 新增 `deploy/experiments/admission-shadow` 与 `admission-active` 两个声明式实验 overlay，并固化
长 SSE drain、stale signal 和 Kubernetes rollout 验收步骤。overlay 只用于受控实验，未执行真实 active rollout，
因此当前没有声称动态并发已经改善 GPU/QPS 或已完成真实连接迁移验收。

R6I-20 将已完成 Pod 归属且 fresh 的 runtime sample 接入 routing hard safety gate：CPU、内存、GPU 利用率、
GPU 显存和温度阈值均可独立配置，默认关闭；缺失/过期/无归属 sample 不触发 gate，所有候选过载时仍 availability-first。
当前没有启用默认阈值，也没有把 runtime 指标做成未经校准的 weighted score。

R6I-21 完善实验报告闭环：benchmark report 现在按 scenario/batch 保存 Gateway metrics window，`fishmesh-client
 compare` 可额外读取两侧 `report.json`，汇总 accepted/completed/rejected QPS、平均 in-flight 和 Little’s Law W，
 并与原有 request-level TTFT bootstrap CI 并列输出。该能力只完善实验证据，尚未执行新的真实 GPU 对照。

R6I-22 完成容量、动态 admission 和 runtime 路由实验手册：固定 A0/A1/A2 与 B0/B1/B2/B3 的执行顺序，明确
先 shadow 后 active、长 SSE drain、Pod identity/freshness 校验、Gateway report 与 requests JSONL 配对、
Little’s Law/收益判定、停止条件和恢复基线步骤。当前所有新增代码、配置和 overlay 已通过本地门禁与
Kustomize 渲染；本阶段没有执行新的 active rollout 或真实 GPU 压测，因此没有新增线上收益结论。

R6I-23 已完成真实容量、Admission 与路由收益验收。A0 target=128 为 384/384 成功、拒绝 0；A1 target=32
为 544/544 成功、accepted/completed 4.053 QPS；active 为 544/544 成功、accepted/completed 4.077 QPS，
单轮 TTFT P95 比 A1 低 14.57%（bootstrap 95% CI `[-22.54%, -5.51%]`），但 QPS 只增加约 0.6%，且 shadow
也出现尾延迟改善，故只记录为待重复验证的 tail signal。长连接对照显示 off/target=128 为 128/128 成功，
active 为 112/128，16 个新请求因 target 从 32 下调到 16 被拒绝；已接纳流全部完成，未观察已有 SSE 被撤销。
因此 active 当前参数不替换默认 off，Little’s Law 观测已可用于容量比较但不等于动态最优控制。

R6I-23 的 B1/B2 路由消融均为 288/288 成功、拒绝 0；B2 三类场景均为 KV `available`，shared/mixed 有真实
cached prefix，random 是 available 的零命中。短 profile TTFT P95 为 `555.31 → 588.57 ms`（`+5.99%`，
CI `[-24.20%, +29.45%]`），不形成短上下文性能收益结论；KV-aware 继续保留 locality/correctness 价值。
当前 Prometheus 仍只有 Gateway scrape，没有 vLLM Pod 维度 runtime sample，因此 B3 runtime hard-gate 收益
暂不可判定。实验修复了 load-balanced token evidence 缺失、metrics sampler 取消误报、首点 completed counter
缺失和不适配 steady-warm 的对照协议；原始产物保留在 `artifacts/bench/`。`session-key` 继续 frozen，
不进入维护和收益矩阵。实验结束后已恢复 `r6i22-final/load-balanced`：Gateway tuning off/target 128，
vLLM 2/2 Ready。

R6I-24 已完成 admission 反馈修正与 KV 短上下文旁路。Admission 现在区分 soft target rejection 与 hard
limit rejection；控制器只因 hard rejection 下调 target，soft rejection 和单纯高 in-flight 进入 hold/saturated，
避免长 SSE 自己触发持续降级。Gateway 保留总拒绝计数，并新增 soft/hard rejection counters。KV-aware 新增
可选 `FISHMESH_KV_AWARE_SHORT_PROMPT_TOKENS`；精确 tokenization 后，阈值内请求跳过 per-request KV lookup，
使用 load-aware fallback，并以 `short-context-bypassed`、独立 reason/policy 和 bypass counter 观测，不把主动
旁路误记为 KV failure。默认阈值仍为 0；`2048` 只是初始候选。阶段 66 已完成真实集群的
512/1024/2048/3072 threshold sweep，以及 threshold 0/576 各两轮 paired repeat：640/640 请求成功、拒绝 0；
整体 pooled TTFT P95 为 `940.87 → 839.14 ms`（`-10.81%`），但 CI `[-26.61%, +22.40%]` 跨 0。按场景只有
512 档出现稳定方向收益，1024/2048/3072 不支持统一旁路。当前 RTX 4060/Qwen2.5-0.5B/vLLM 0.23.0 profile
的候选固定阈值为 `576`，使用 `kv-aware-short-bypass-576` overlay；不写入全局默认，仍以 threshold 0 作为默认。
随后已完成真实 smoke：短上下文 overlay 返回
`short-context-bypassed` 且 bypass counter 增加、degradation counter 未增加；修正版 active 长连接
`r6i24-drain-active-r1` 为 128/128 成功、soft/hard rejection 均为 0，最终 target=64。实验完成后已恢复
`load-balanced / initial target=128 / tuning=off`，vLLM 2/2 Ready 且重启数为 0。当前只剩同一 profile 上更多
threshold 576 独立 paired rounds 与 promotion gate；不把当前候选外推到其他模型、GPU 或 vLLM profile。

README 与 README_CN 现以五分钟 Lite demo 为入口：确认 `load-balanced` 默认基线后临时安装
`lite-kv-aware`，用不带 session key 的同一长 system prompt、不同 user message 请求读取 KV-aware 决策头，
再恢复基线；监控面板可同时观察请求、TTFT、KV 有效性/freshness、降级与 RSS。Standard mode（R6E/llm-d
部署）仍明确后置。

Lite 的发布/回滚边界已记录在 `docs/notes/release-notes.md`：当前已实证的是 macOS arm64 构建机
交叉构建并离线导入 GPU 节点的 Linux amd64 Gateway；Dockerfile 的 arm64 target、远程 multi-arch
manifest 与 SBOM attestation 是发布前流程，尚无真实 release 验证。升级和回滚以 manifest/image digest
及实际 ConfigMap 为证据，并在 KV-aware 信号异常时恢复 `session-key`，不改变 unknown/stale 的降级语义。

## 当前运行拓扑

| 节点 | 角色 | 当前运行内容 |
| --- | --- | --- |
| `fishmesh-control-plane`（`100.111.18.52`） | OrbStack 内的 ARM64 Ubuntu VM | 官方 K3s server、CoreDNS、local-path provisioner、metrics-server |
| `fishmesh-gpu`（`100.98.49.106`） | 带 RTX 4060 的 x86_64 Ubuntu 笔记本 | K3s agent、NVIDIA device plugin、两个 vLLM 副本、amd64 FishMesh Gateway |

OrbStack 内置的 Kubernetes 集群不是这个多节点 control plane。官方 K3s server 运行在
已有的 OrbStack Ubuntu VM 内。GPU 笔记本上旧的 standalone `k3s.service` 数据被保留，
但服务 disabled；当前只有 `k3s-fishmesh-agent` enabled 且 active。

当前推理状态：

- `qwen-vllm` 使用 digest-pinned vLLM 0.23.0，两个副本提供本地缓存的
  Qwen2.5-0.5B-Instruct；
- RTX 4060 通过 NVIDIA device plugin 0.19.2 暴露两个 time-sliced resource；它们不是
  两块独立 GPU，也不提供显存或故障隔离；
- `qwen-vllm-baseline` 是普通 ClusterIP Service，负责随机的连接级 endpoint 选择；
  `qwen-vllm` 仍然是 headless Service，EndpointSlice 实验直接读取它的 Ready 地址；
- `fishmesh-gateway` 是一个 Go streaming proxy，运行在 GPU 节点。它不申请 GPU，
  但因为第一个镜像是 amd64 而 control plane 是 ARM64，所以被放在该节点；
- Gateway 当前运行只含 Gateway 二进制的 `fishmesh-gateway:r6i1-load-gates-r1`；参考集群当前应用
  `deploy/experiments/long-context-kv-aware`，使用 64 个 in-flight 上限、16 个数据面连接、upstream
  keepalive，并启用 EndpointSlice + KVEvents/replay KV-aware routing；长上下文实验的 vLLM 使用
  `gpu-memory-utilization=0.40` 以覆盖 CUDA-graph 启动开销。它使用专用 SA，只有 namespace 内
  EndpointSlice `get/list/watch` 和 Pods `get/list`，无 Secrets/写操作/cluster-scope 权限；
  Prometheus observation 已启用，unknown/stale 仍回退 load-balanced；
- analyst、simulator 和 loadgen 已冻结且不在当前产品部署或 Gateway 镜像中；遗留 analyst
  Deployment/RBAC 与 loadgen SA 已从集群清理；
- 模型持久化于 GPU 笔记本的 `/var/lib/fishmesh/models`，通过 retained local PV/PVC
  暴露，不会在每次 Pod 重启时重新下载。

## 从 Mac 体验推理

先在一个终端保持以下命令运行：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

另开终端查看模型：

```bash
curl http://127.0.0.1:8080/v1/models
```

发送不需要 API key 的流式 Chat Completion：

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [{"role": "user", "content": "用一句话介绍 FishMesh。"}],
    "stream": true,
    "max_tokens": 64,
    "temperature": 0.2
  }'
```

这是内部实验 endpoint，不是对公网暴露的服务。按 `Ctrl-C` 停止 port-forward 不会改变
集群状态。

## 暂停与恢复

如果只想释放 RTX 4060 显存，同时保留 K3s control plane 和 agent：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  scale deployment/qwen-vllm --replicas=0
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  scale deployment/fishmesh-gateway --replicas=0
```

恢复时，`qwen-vllm` 使用 `--replicas=2`，Gateway 使用 `--replicas=1`。模型文件仍在
磁盘上，但 vLLM 的内存权重、CUDA graphs 和 KV cache 会丢失；下一次启动需要重新加载
并 warm up。

如果临时关闭 Ubuntu GPU 笔记本，Mac 上的 control plane 仍然存在，但
`fishmesh-gpu` 会变成 `NotReady`，推理没有可用 endpoint。Ubuntu 下次启动时，已启用的
`k3s-fishmesh-agent` 会自动启动并重新加入集群；Deployment 会重新创建 Gateway/vLLM
Pod。不会丢失模型数据，但会出现服务中断和 cold-start 延迟。关闭 OrbStack control-plane
VM 的影响更大：Kubernetes API 和 reconciliation 也会停止，应按完整集群关闭处理。

真正受推理影响的是 Ubuntu GPU 笔记本。两个副本当前即使 GPU 利用率接近 0，也会占用约
6874 / 8188 MiB 显存；time-slicing 不提供 VRAM isolation。Mac 只运行轻量 control plane
和 Tailscale 路径，正常 CPU/RAM 影响很小。Ubuntu 不接收请求时，vLLM 仍会保留显存，
直到你把副本缩容为 0。

## 已完成

- Tailscale 上的 K3s server/agent、NVIDIA toolkit/device plugin 和 CUDA smoke test；
- 使用本地 model PV/PVC 的离线 vLLM 部署，以及两个稳定副本；
- Go Gateway、streaming proxy、Prometheus metrics、确定性的 Loadgen、JSONL 输出、
  单元测试、镜像导入脚本、Kustomize 清单、GitHub Actions 和本地 `act` 入口；
- 第一轮 random-Service baseline：200/200 成功，TTFT P50 77.26 ms、P95 122.01 ms、
  P99 312.67 ms；原始输出已归档在本地 `artifacts/`。
- Evidence-based Diagnoser 骨架：`Incident -> 四个领域工具 -> rules-v1 -> Diagnosis/Evidence/
  Recommendation`，本地和 K3s demo API 均验证返回 `prefix_locality_degraded`。
- N1 真实观测适配器：vLLM/GPU Prometheus、Kubernetes Events/Pod 状态和显式
  `insufficient_observability` 缺口诊断已完成单元测试；observability overlay 已在 K3s
  部署验证，vLLM 与 Kubernetes signals 返回 `ok`。
- N2 EndpointSlice 第一版：Gateway 支持 namespace-scoped watch/list、Ready 过滤、稳定
  backend ID、动态 in-flight counter 和 Service fallback；实验 overlay 已在 K3s 验证
  `/v1/models` 返回 `session-key` 与 `endpoint-*`，验证后已恢复 baseline。
- N3 Backend Snapshot 第一版：按 EndpointSlice backend ID 采集对应 vLLM `/metrics`，暴露
  queue、running、Prefix Cache、TTFT、status 和 freshness；backend-snapshot overlay 已在
  K3s 验证两个副本均为 `status=ok`，验证后已恢复 baseline。
- N4 身份映射与故障状态：backend targetRef 已映射到 Pod、Node 和声明 GPU request；discovery
  status/freshness/ready backend 已进入 Gateway metrics，EndpointSlice 缓存过期会让 `/readyz`
  返回 503；临时撤销 RoleBinding 后状态变为 `degraded/503`，恢复权限后约一个刷新周期
  自动回到 `ok/200`。
- P0 实验可信度：Loadgen 输出 `run_metadata -> request -> summary`；15 个仍存活的历史 Job
  已恢复 KV-aware Job YAML、原始压缩 JSONL 和 provenance manifest；新实验方案要求重复运行、
  随机 treatment 顺序、失败 attempt 保留和开源 router 同环境对照。
- P0 方向收敛：MVP 从任意 weighted hybrid score 改为 session-key；GPU 指标只作为
  节点级诊断证据，eBPF、自动 actuator 和 disaggregation 明确移出 MVP。
- P1 session-key-v1：仅保存 routing key 的 SHA-256，以 Rendezvous Hash 选择 preferred
  backend，使用独立 queue/local-inflight threshold 溢出，具备 TTL/容量回收、Service
  fallback、决策 provenance 和 race/integration tests。
- P1 K3s 行为 smoke attempt 2：24/24 成功，1 次 miss、11 次 hit、12 次
  local-inflight spillover，两个 backend 各选择 12 次；完整运行配置、镜像 digest、Job、
  EndpointSlice、日志和 JSONL 已归档。该结果只证明行为正确性，不是性能 benchmark。
- 2026-08-10 工程方向章程曾把 standalone Gateway 定位为开发/conformance 载体、把生产形态
  提前为 EPP/llm-d 集成；这一主次关系已在 2026-08-11 由 ADR-002 修订，但当时对 Analyst 和
  实验系统的冻结决定继续有效。
- P1 request-path reliability：全局 admission 128、每 backend 连接上限 32、transport error
  EWMA circuit、endpoint state GC、queue/running per-field sample、client cancellation 和
  no-retry-after-headers 边界均有 race/fault tests。
- P1 K3s 验证：`fishmesh:0.3.0-p1` manifest digest
  `sha256:036149be62f706b5cc3580e1caf29714282a784bc21c56fc30f88a09d3bc0223`；Gateway/Analyst
  rollout Ready 且零重启，启动日志确认全部 reliability 参数；真实 vLLM smoke 8/8 成功，
  两个 routing key 保持 affinity，新 metrics 正常暴露。该 smoke 是兼容性验证，不是性能结论。
- 代码组织 R0：确认“能力包 + 单向依赖 + 显式组合根”适用于 Serving，但拒绝顶层
  `internal/domain`、`shared` 类型仓库和无意义的一实现一接口。已固定包/文件/声明/字面量规范、
  目标依赖 DAG 和 R1–R5 渐进迁移计划；本阶段未改变运行代码或集群。
- Serving Domain R1：Backend/ID、Identity、Observation/Sample、Routing 和 Circuit 已回到各自
  owner；routing mode/policy/reason 与 circuit outcome 已类型化，transport 不再依赖 routing；
  `go list` 架构测试会阻止原子 domain 出现反向依赖。运行协议、配置、部署和集群均未改变。
- Serving Domain R2：`endpoint` 已更名并拆分为 contract-first 的 `discovery`；identity、
  observation、transport 只向 Gateway 暴露接口和明确构造器；环境变量、HTTP/SSE header、
  metric/label 与 vLLM/Kubernetes 协议常量已收口。完整本地 CI 通过，运行协议、部署和集群均
  未改变；GPU 节点暂停期间不执行无价值的 K3s smoke。
- Serving Domain R3：新增协议无关的 requestpath 编排与幂等 lease；discovery freshness、
  circuit eligibility、Service fallback、local in-flight 和 membership GC 已从 Gateway 移出；
  client cancellation、deadline、upstream/downstream stream failure 保持独立 typed outcome。
- Serving Domain R4：新增非阻塞 admission domain 和进程 config package；Gateway 已拆为契约、
  handler、proxy stream 与 metrics 文件，只依赖注入能力；`cmd/fishmesh-gateway` 显式创建并按
  反序关闭 requestpath、observation、resolver 和 transport 等有状态资源。R1–R4 重构闭环。
- R5A 无 GPU simulator：新增独立 `internal/simulator` 和可执行入口，提供 OpenAI SSE、vLLM
  queue/running 指标及运行时故障控制；真实 HTTP/TLS E2E 已覆盖 slow、stream abort、circuit
  fallback、overload、cancellation、EndpointSlice removal/stale。该阶段未使用 GPU/K3s。
- R5B EPP/llm-d 决策：按 GIE v1.5.0、EPP Protocol v1.0.0 和 llm-d Router v0.9.0 核对
  protocol、InferencePool、Filter/Scorer/Picker、data layer 与 in-flight lifecycle；选择 pinned
  llm-d 编译期 scorer + 自定义 EPP 入口，拒绝自研 ext_proc 和生产使用 LWEPP。设计同时纠正
  integrated 复用整个 requestpath 的错误假设：两种模式只共享纯 routing 选择，空 subset 在
  EPP 中返回 503，不走 standalone Service fallback。本阶段未使用 GPU/K3s。
- R5C llm-d 本地集成切片：新增只依赖 backend/observation/routing 的 `internal/serving/llmd`，
  具体实现 pinned v0.9.0 的 Filter、Scorer、ConsumerPlugin 和 response hook；queue freshness、
  required in-flight、endpoint churn、并发、多实例、selected/served provenance 和纯策略
  conformance 均有 race/contract tests。新增 `fishmesh-epp` 组合根复用上游 runner，Docker 镜像
  包含新二进制；最小 EndpointPickerConfig 已进入 manifest 门禁。该阶段没有访问 GPU/K3s，
  完整 Gateway/EPP/InferencePool 部署和 ext_proc wire smoke 尚未完成。
- R6 方向复位：确认主要缺口不是 scorer 代码量，而是可独立交付的产品和真实 cache signal。
  Lite Gateway 恢复为主产品，Standard EPP 保留为生态集成；KV-aware 将复用 vLLM KVEvents、
  Render API 与上游 `llm-d-kv-cache` library。simulator/loadgen 冻结功能，Analyst/Diagnostics 从
  默认产品面移除，R6C 前不做破坏性源码删除。新增 ADR-002，并统一更新章程、架构、计划、
  实验规范、中英文 README 与代码可读性要求。代码规范补充允许按具体概念建立纯
  `entity/<concept>` 子包：只依赖标准库，可由值对象执行 `Validate` 等自身校验，但不得成为新的
  shared 类型仓库。本阶段未修改集群或 Go 行为。
- R6A 真实 KV 信号闭环：新增声明式 vLLM 0.23.0 KVEvents/replay overlay 和一次性真实探针；
  `llm-d-kv-cache` 升至正式版 v0.9.0。不同会话共享 system prompt 时只在真实缓存 Pod 命中
  8 blocks/128 tokens；断流期间 KV-aware 状态先 invalid，恢复后 replay 补回；真实压力产生 3105
  个 removed 并把旧命中降为零；旧 Pod UID 删除后 80-token 命中被清理，新 Pod 从 sequence 0
  独立发布。实测 Render 约 5–6 ms、lookup 约 0.08–0.14 ms，压力后 Go HeapAlloc 约 11.7 MiB。
  最小复测 event lag 为 0.678 ms，空索引探针 RSS 约 33.2 MiB。实验结束后恢复基础 vLLM
  Deployment，KV-aware 尚未接入 Gateway。
- R6B-1 真实分词能力域：新增无其他 internal 依赖的 `internal/serving/tokenization`，以不可变
  Result/Prompt 提供真实 Token IDs 和 cache salt；vLLM adapter 显式支持 Chat/Completions Render，
  保留扩展字段，并保护 timeout、取消、请求/响应体和 token 总数边界。模型错配、非 2xx、异常
  响应和多模态未支持均返回 typed error，不能被误解为零命中。本阶段没有接 Gateway、KV index
  或集群，当前线上行为仍是 session-key。
- R6B-2 真实 KV 状态域：新增只依赖 backend 的 `internal/serving/kvcache`，复用上游 vLLM parser、
  canonical hash、有界 index 和 longest-prefix scorer，但不用没有容量/完成回执的上游异步 event
  workqueue。每条 live/replay event 同步 apply 后才推进 sequence；gap 先 invalid，再 replay，无法
  覆盖时清理该 Pod。cache salt、BlockRemoved、replay TTL、Pod UID 替换、查询/事件/index 容量和
  Close 等待均有 race/contract tests。当前只承诺已验证的 vLLM 0.23 文本 GPU event，不支持的
  LoRA/HMA/offload 明确失效。本阶段仍未接 Gateway 或 routing，也没有访问集群。
- R6B-3 缓存/负载联合纯路由：routing 新增不依赖 kvcache/tokenization 的 `KVAwareInput`、
  `KVMatch`、`Load` 和 `kv-aware-v1`。策略按 eligible、hard overload、uncached tokens、
  queue/running、local in-flight 和最终 session 平局提示做确定性选择。有效零命中保持 KV-aware 语义；
  unknown/stale 则由 requestpath 显式改用 load-balanced，并返回 typed degradation。Gateway、Render、
  KV index 和线上部署尚未接入。
- R6B-4 请求路径 KV-aware 编排：requestpath 在 KV-aware mode 下显式注入 tokenization 与 kvcache，
  将只读 TokenIDs、模型、cache salt 和 eligible backends 构造成一次 KV lookup，再投影 Match 为
  routing `KVAwareInput` 并消费其 Decision。tokenizer/lookup 普通失败与 unknown/stale match 通过
  typed load-balanced 降级发布；取消/超时不吞掉而是返回调用方。Gateway、组合根、Pod instance 翻译、
  subscriber 和线上部署仍未接入，现网仍是 session-key。
- R6B-5 有界 Body 与 KV-aware 交付：Gateway 对原始请求 body 实施 2 MiB 默认硬上限，并用同一字节
  副本完成 requestpath Render/KV lookup 输入和 upstream replay；超限在选路前返回 413。SSE copy、
  `[DONE]`、headers 后不 retry 和 lease outcome 均保持原行为。响应新增 `X-FishMesh-KV-Status`，
  现有 route reason/policy/backend 头在选路后立即写出，unknown/stale 的 load-balanced 降级可直接观察。
  进程内闭环测试已走通 Render→Lookup→select→SSE；生产组合根和 KV subscriber 尚未接入。
- R6B-6 组合根真实 KV 接入：Gateway 在显式 KV-aware mode 创建 renderer 与有界 kvcache index，将
  EndpointSlice `targetRef.uid`、Pod IP 翻译为稳定 Instance 和 `5557/5558` ZMQ endpoint，并在退出时
  关闭 subscriber。真实 vLLM 0.23 事件包含 `group_idx=0/full_attention`；该单组文本语义已以同包
  contract test 窄化兼容，其他 HMA/LoRA/window 语义仍 explicit invalid。真实集群第二个无 session key
  请求得到 160 cached prefix tokens；首请求 `match-unavailable` 保持 load-balanced 降级。验收后已恢复默认
  session-key。
- R6C Lite 产品化：默认 Dockerfile 仅构建 `fishmesh-gateway`，新 `deploy/lite-kv-aware` 组合专用
  RBAC、PDB、probes、资源/安全边界与 KVEvents/replay 配置；指标发布 KV validity/freshness/sequence/
  batches、KV-aware degradation 与进程 CPU/RSS，且不泄露请求数据。真实滚动更新成功，删除实际命中 vLLM
  Pod 后旧事件流退出、剩余 backend 的 zero miss 仍为 KV-aware，替代 Pod replay 的过渡明确降级，随后
  恢复 80-token KV-aware hit。llm-d 的 controller-runtime logger 由 Gateway 入口显式 discard，修复后的
  live/recovery 日志未再出现 stack warning。Flannel 仍不执行 NetworkPolicy，策略只作为 CNI 迁移后待启用项。
- R6D 有限性能对照：复用默认 loadgen workload，对 Service/load-balanced/KV-aware 各执行 3×200 requests，
  以 SSE TTFT、两 vLLM Pod token counter delta、Gateway process CPU/RSS 与 policy headers 形成一页表。
  KV-aware 的 8/600 restart/replay 过渡请求显式降级，592/600 使用 KV-aware policy，未观察错误 KV-aware 声明；
  但其 P50/P95 TTFT 分别比 Service 高 32.19%/12.82%，generation throughput 低 7.91%。单 RTX 4060
  time-slicing、Qwen2.5-0.5B、两个共享副本的结果仅作 correctness/profile evidence；当前 metrics 不提供
  可按 treatment 归因的 per-event latency。实验后已恢复 session-key。
- R6D2 前缀长度分段对照：修复 requestpath 后台 reconcile 漏掉 KV lifecycle 的缺口；vLLM rollout 后无需
  业务请求即可为新 EndpointSlice Pod 建立 `Valid=true` subscriber。按独立 cache generation、单 Pod
  prewarm 验证 c=1，完成 512/2048 的 Service/KV-aware 各 200 requests：KV-aware P50/P95 分别为
  24.429/24.936、25.279/26.680 ms，Service 为 31.274/35.442、31.859/37.059 ms。收益低于约 10 ms
  routing overhead，未声称拐点；GPU dmon 峰值 68°C，无 watchdog 告警，最终恢复 session-key。
- R6H-3 长上下文验证：mixed 生成器已改为按整个场景的请求总数计算比例；`long-context-balanced` 计划的两个
  mixed 场景各自使用 100 请求，实际分布为 60 个 `shared-0`、20 个 `unique-*`、20 个其他共享前缀
  （`shared-1/2/3` 为 7/6/7）。两轮反向顺序 A/B 的四个正式 run 共 1568/1568 成功；load-balanced/KV-aware 的 TTFT
  P50/P95 分别为 48.40/431.42 ms 与 50.65/100.32 ms，KV-aware 的 TTFT P95 下降 76.7%，总耗时 P95
  下降 55.0%，而 TTFT P50 上升 4.6%，收益主要集中在尾延迟。真实 mixed-4k/mixed-12k 的 TTFT P95 分别
  下降 62.3%/84.3%。四次正式 run 无 Gateway admission/upstream error，KV-aware 784/784 `available`，
  GPU SM/memory-controller 峰值 100%，显存 7262/8188 MiB，温度峰值 65°C。报告位于
  `artifacts/bench/long-context-mixed-comparison-r6h-r2/`；下一步按并发 1/4/8/16 做阶梯测试。

## 下一步待办

1. 补齐 `$namespace+$pod` 作用域的容器/GPU runtime exporter 与 Prometheus scrape，再重跑 B3 freshness、
   hard-gate 和资源归因实验；
2. 针对修正后的 active admission 增加短流/长流分层控制实验，至少两轮 paired compare，确认 soft rejection
   不再触发自反馈降级，再讨论是否改变默认模式；
3. 在同一 profile 上补足 threshold 576 的独立 paired rounds，满足 promotion gate 后再讨论是否进入默认 KV-aware
   overlay；model/hardware/vLLM/profile 变化时必须重新校准，不能外推 576；
4. KV-aware 继续按长上下文 mixed 矩阵重复验证，短 profile 只保留 correctness/locality 结论；
5. learned-shadow 仍保持 shadow，只有在独立 agreement/误差与 R6I-6 性能门禁重新通过后，才另立 active 决策；
6. Standard mode/llm-d 完整闭环继续后置；在声称 NetworkPolicy 已生效前迁移到支持 policy enforcement 的
   CNI，当前 Flannel 不能执行声明式 NetworkPolicy。
