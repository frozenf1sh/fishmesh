# 阶段 55：Tokenization 与 KV 并发边界

## 1. 结论

Token IDs 是 KV lookup 的输入，因此 Tokenize 与 `KVCache.Lookup` 不能直接并发。此前实现和注释没有把这个
数据依赖表达清楚。本阶段将真正可并发的两项工作拆开：

```text
Tokenize ─────────────┐
                      ├─> KVCache.Lookup ─> routing input
KV instance reconcile ┘
```

## 2. 实现

- Tokenizer 与 KV instance reconcile 使用同一个 request-scoped context 并行执行；
- 两者都完成后，才根据 Token IDs 构造 `kvcache.Query` 并执行 Lookup；
- 调用方取消仍然是硬错误；可降级的 Render/reconcile 故障继续显式降级到
  `kv-aware-load-fallback-v1`；
- selection snapshot、routing decision 和 local in-flight reservation 仍保持串行提交，避免 thundering herd；
- 新测试用 barrier 验证 Tokenize 与 reconcile 确实重叠，不把 KV lookup 错误地宣称为并行。

## 3. 验证

- `go test ./internal/serving/requestpath`：KV query 构造、降级、取消和并发边界；
- 本阶段不改变 KV unknown/stale fallback 语义，也不改变 `session-key` frozen compatibility 边界。
