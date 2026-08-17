# FishMesh 面试素材包(2026-08-12 终版)

> 基于项目真实状态与实测数据。原则:诚实讲边界,不讲算法壁垒;理解收益成因 > 背数字。

## 1. 一句话定位

> 一个 Kubernetes 上的 LLM 推理流量调度器:消费 vLLM 的真实 KV cache 事件,知道"哪个副本缓存了这个请求的前缀",结合负载做有界、可解释、可降级的选路。实测相对 direct Service 的 TTFT P50 改善约 21%。

## 2. 四条证据线(面试主证据,按序讲)

| 证据 | 数据 | 说明 |
| --- | --- | --- |
| ① 信号可行(R6A) | 不同 session、相同 system prompt → 命中 **128 tokens** 真实公共前缀;event lag 0.678ms;eviction 后 128→0 | 真实 KV 信号可被消费,不是客户端 key/累计命中率 |
| ② 代码链路闭环(R6B-5) | 闭环单测:body replay → render → lookup → 选路 → SSE 原样返回 | 架构链路完整、降级语义正确 |
| ③ 生产路径真实命中(R6B-6) | 无 session key 第二请求命中 **160 tokens**,vLLM hit rate 16.5% 佐证 | 生产 Gateway 真用上了 KV 信号 |
| ④ **受控对照(R6D2)** | 512/2048 前缀,KV-aware 相对 Service **TTFT P50 −21.9% / −20.7%**;绝对收益 ~6.6ms < 路由开销,如实记录"未观测到拐点" | 正向收益 + 收益模型验证 + 诚实边界 |

## 3. 收益模型(面试核心,可手写)

```
KV-aware 净收益 = 命中收益 − 路由开销
命中收益 = 随机 miss 概率(1 − c/N) × 请求数 × 前缀长度 × prefill 单位成本
路由开销 ≈ +10ms(Render 5-6ms + lookup <1ms + replay)
```

关键洞察:
- **c=1、N=2 时随机 miss 概率 50%**;N=10 时 90% → 副本越多收益空间越大;
- **0.5B 模型 prefill 极便宜**(2048 tokens 只值十几 ms)→ 绝对收益小是环境使然,不是项目缺陷;
- **生产环境每个因素都朝有利方向移动**(更大模型/更多副本/热点前缀集中),实验是在最不利环境拿到 21%。

## 4. 30 秒电梯版

> "我做了一个 Kubernetes 上的 LLM 推理流量调度器。K8s Service 只做连接级分发,不知道哪个 vLLM 副本缓存了你的 prompt 前缀——换副本就重新计算,浪费 GPU。我做的是消费 vLLM 真实 KV cache 事件的精确路由:请求进来先分词,查每个副本缓存了哪些前缀,选最可能命中的;信号不可靠时显式降级到负载路由。实测在共享 system prompt 场景,KV-aware 相对 Service 的 TTFT 降低约 21%,命中数据不可靠时降级零违例。"

## 5. 3 分钟完整版(六步)

1. **背景**:自托管 LLM 推理(K3s + vLLM 双副本,单 GPU time-slicing)。
2. **问题**:Service 连接级分发不懂 LLM 请求——prefix cache 是进程内的,换副本即失效(Google 公开数据:cache-aware 路由 TTFT 峰值提升最多 96%)。
3. **方案**:session-key(会话亲和+溢出)→ 演进到 KV-aware:Render API 拿真实 token IDs → 订阅 KVEvents(ZMQ)维护逐 Pod 前缀索引 → cache+load 词典序选路。
4. **工程**:限界上下文 + 组合根 + 自动依赖测试(拦过一次真实违规);typed reason;降级不变量(unknown/stale ≠ 零命中)。
5. **验证**:无 GPU 故障模拟器 E2E;真实集群四步证据(探针 128 → 单测闭环 → 生产 160 → 对照 21%);环境边界诚实标注。
6. **不足与边界**:拐点未观测(0.5B 环境);`engine key not found` 高事件量下未定位(走降级,已知边界);R6E Standard 集成后置。

## 6. 追问应答库

### Q:为什么做这个?不用开源吗?
三层:问题真实(有公开数据支撑);想完整掌握请求路径工程能力;边界收敛——调研过 Envoy/GIE/llm-d,网关框架已成熟,但"消费真实 KVEvents 做精确前缀路由 + 严格故障验证"生态没有工程化实现。不自研网关,Standard mode 复用 llm-d。

### Q:你的路由和 Envoy / llm-d 内置 scorer 有什么区别?
诚实:近似 cache-aware(会话亲和/累计命中率)开源已有。差异两点:**真实信号**(KVEvents + Render 的逐 block 精确前缀)与**工程化**(unknown/stale 显式降级、sequence 应用后才提交、replay 心跳判新鲜,有自动化验证)。不说"算法更强",说"同一语义下做了更严格的工程化"。

### Q:KV-aware 什么时候有优势?为什么 R6D 里 Service 也不差?
讲收益模型:收益 = miss 概率(1−c/N)× 前缀长度 × prefill 成本 − 路由开销。R6D 失败原因:c=2(统一 corpus,无 miss 可避免)+ 短生成 + 0.5B。优势场景四条件:各副本缓存**不同**前缀、**长** prompt、**多**副本、eviction 频繁。R6D2 用 c=1 受控证明:收益 21%,绝对 < 开销 → 环境边界,非项目失败。

### Q:共享前缀 vs 私有前缀怎么区分?(长对话场景)
多轮对话越长,"可共享公共前缀"≠ 对话全长——后续轮次私有分叉。真正热的是**多会话共享的长 system prompt**(agent 工具说明/合规条款/RAG 固定注入)。共享前缀 → KV-aware 主场;私有长前缀 → 会话亲和(session-key)就够。面试答"区分共享 vs 私有"比"越长越好"精确得多。

### Q:生产环境哪些因素影响这些指标?
模型规模(prefill 成本,放大收益)、副本数 N(miss 概率)、缓存分散度 c、共享前缀长度/共享度、eviction 压力、流量热度分布(80/20)、并发/排队(cache+load 联合)、网络/拓扑(跨 AZ 附加价值,项目未做拓扑感知)、vLLM 版本行为(KVEvents 语义变动,已踩过 group_idx 坑)。实验环境几乎每个因素都最不利,仍拿到 21% → 生产方向全有利。

### Q:项目适用什么生产场景?
甜蜜点:多会话共享长前缀(Agent 平台、RAG、合规问答)+ 自托管中小集群(Lite mode)+ llm-d 生态 scorer(Standard mode)。不适合:短 prompt 无共享前缀、大规模多租户入口(用 Envoy AI Gateway/GKE)、事件流不可靠无运维的环境。定位:"单模型小集群的专用 KV 感知路由器",不是通用 AI 网关。

### Q:遇到最难的问题?
① vLLM 0.23 KV 事件带 `group_idx=0/full_attention`,初版全当 HMA 拒绝 → 索引永远 invalid;真实集群取证定位,修复只归一化单组语义。② "空闲健康 vLLM 不发 KV event",stale 不能用最后事件时间判,必须 replay END 心跳。③ 修了 vLLM rollout 后 KV subscriber 不随 EndpointSlice 换代的 lifecycle 缺陷。

### Q:项目有什么没解决/不足?
诚实三件:拐点未观测(0.5B 环境限制);`engine key not found` 高事件量双副本下未定位(保持降级,已记录取证计划);与 llm-d 内置 scorer 的量化对照未做(P4 后置)。

## 7. 数字速查表

| 指标 | 数值 |
| --- | --- |
| Render API 延迟 | 5-6ms(512-3072 前缀 4.7-6.9ms) |
| KV lookup 延迟 | 0.08-0.38ms |
| KVEvents publisher→consumer lag | 0.678ms |
| 空索引内存 | RSS ~33 MiB / HeapAlloc ~2.6 MiB(压力后 11.7 MiB) |
| 真实命中 | 探针 128 / 生产 160 tokens,hit rate 16.5% |
| **受控对照(R6D2)** | 512:TTFT P50 24.4 vs 31.3ms(−21.9%);2048:25.3 vs 31.9ms(−20.7%) |
| 对照正确性 | 200/200×4 成功;无错误 KV-aware 声明;降级 800/800 零违例 |
| eviction | BlockRemoved 3105+,旧前缀 128→0 |
| 故障 E2E | 端点删除/发现过期/过载/上游错误/取消 |
| 实验环境边界 | 单卡 time-slicing、Qwen2.5-0.5B、双副本、同机网络;仅 profile 证据 |

## 8. 诚实边界(主动说,别等追问)

- 21% 是 c=1 受控场景、0.5B 环境;绝对收益 ~6.6ms,拐点未观测——不能声称生产级提升;
- `engine key not found` 在双副本高事件量下未定位(单副本复现通过),保持显式降级;
- KV-aware 当前支持 Qwen 文本单组语义,LoRA/窗口/多模态显式降级;
- 与开源 scheduler 的量化对照未做;跨副本共享 prefix cache 不是本项目目标(我做的是"选到缓存所在副本")。
