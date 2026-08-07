# 阶段 22：R6B-4 请求路径 Exact 编排

> 日期：2026-08-12
> 类型：Lite MVP 编排切片
> 状态：已完成
> 产品边界：requestpath 消费真实 Token IDs、KV Match 与 routing ExactInput/Decision；不接 Gateway、Kubernetes Pod 到 KV instance 翻译或部署

## 1. 本阶段真正交付什么

`internal/serving/requestpath` 现在是 tokenization、kvcache 与纯 routing 的唯一 standalone 编排边界。配置 `exact-cache-load` 时，它用 Render 产生的只读 TokenIDs 构造 `kvcache.Query`，读取逐 backend `Match`，再投影为 routing 自有的 `ExactInput` 和 `Load`，最后消费 `routing.Decision` 创建 lease。

tokenization 或 lookup 的普通失败、unknown match、stale match 都不会伪装为 cache miss：requestpath 将 Exact 状态发布为 unavailable，并使用阶段 21 固定的 `exact-signal-unavailable` / `exact-load-fallback-v1` 明确降级到 load-aware。context cancellation/deadline 则继续返回调用方，不创建 lease。

## 2. 固定契约与不变量

1. 只有 `ModeExactCacheLoad` 注入且调用 `Tokenizer` 与 `kvcache.Index`；其他 routing mode 不读取原始 body、不调用 Render 或 KV lookup；
2. exact 模式构造 requestpath 时必须同时提供 Tokenizer 和 KVCache，避免运行到首个请求才发现组合根遗漏依赖；
3. `Prompt.TokenIDs()` 返回副本，requestpath 只将该副本作为 `Query.TokenGroups` 发布，不保留可变 token slice；
4. `Result.Model`、`CacheSalt`、全部 prompt token groups 和当前 eligible backend IDs 必须原样进入一次 KV query；
5. kvcache `Match.Valid=false`、缺少候选 match 和 stale/unknown 都映射为 routing 的 invalid `CacheMatch`，绝不改写成 `MatchedTokens=0`；
6. `Valid=true, MatchedTokens=0` 仍保留为真实 exact zero miss；
7. queue/running 只有两个 observation sample 都有效、有限且非负时才成为 `routing.Load`；浮点压力向上取整，缺失指标不伪装成零；
8. requestpath 只编排降级和 lease，不解析 Render/KVEvents wire、不启动 subscriber，也不连接 Gateway。

## 3. 实际实现

`Dependencies` 新增稳定替换边界 `tokenization.Tokenizer` 与 `kvcache.Index`，并扩展架构门禁以允许 requestpath 单向依赖这两个叶子 domain。`Select` 保持四个可读阶段：同步 discovery、构建 eligibility/负载快照、生成 exact 输入、执行纯策略并登记 lease。

`buildExactInput` 仅在 exact mode 运行。它调用 tokenizer、从 profile 的只读访问器复制 token groups、以 eligible backend 列表查询 index，然后将 `kvcache.Match` 投影到 `routing.CacheMatch`。lookup 失败与无效 match 返回空/invalid ExactInput；阶段 21 的 requestpath fallback 因此负责最终 decision，不在 leaf domain 内隐式 fallback。

`State.Exact` 增加 `not-requested`、`available`、`tokenization-failed`、`lookup-failed` 和 `match-unavailable`，为后续 Gateway metrics/logging 提供协议无关状态；本阶段不新增 delivery metric。

## 4. 验证

新增同包 contract tests 覆盖：

- kvcache valid zero match 与 unknown match 投影到 routing 时仍有不同语义；
- Render 返回的 TokenIDs、model、cache salt、当前 backend IDs 被准确构造成 KV query；
- empty/unknown KV snapshot 通过 `ExactMatchUnavailable` 与 typed load-aware decision 显式降级；
- tokenizer cancellation 保留 `errors.Is(err, context.Canceled)`，不静默降级；
- exact strategy 缺少 Tokenizer/KVCache 时构造失败；
- requestpath 的新增 import 已由 architecture test 限制为 tokenization/kvcache 两个叶子 domain。

完整门禁：

```text
go test -race ./...
go vet ./...
go build ./...
make manifest
```

## 5. 尚未交付的产品行为

Gateway 尚未把原始 HTTP body/route 传入 `requestpath.Request`，也没有在组合根创建 renderer 或 KV index；当前运行配置仍不会启用 exact mode，线上行为仍是 bounded affinity。Pod discovery 到 `kvcache.Instance` 的翻译、subscriber 生命周期和 hard overload 阈值配置同样尚未接入。

## 6. 下一阶段

R6B-5 接入 Gateway 与组合根：有界读取并原样 replay 请求 body，创建 tokenizer/index，翻译当前 Pod/endpoint 为 KV instances，并把 Exact 状态与 decision 投影为 Gateway 指标；随后再做受控真实集群验收。
