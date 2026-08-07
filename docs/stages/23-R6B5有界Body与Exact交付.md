# 阶段 23：R6B-5 有界 Body 与 Exact 交付

> 日期：2026-08-12
> 类型：Lite MVP delivery 切片
> 状态：已完成
> 产品边界：Gateway 有界 body/replay、Render→lookup→routing→SSE 交付和决策头；不创建生产 KV subscriber、Pod instance 翻译或部署 overlay

## 1. 本阶段真正交付什么

Gateway 现在只读取一次、且有明确上限地缓冲 `/v1/` 原始请求 body。相同字节副本传给 requestpath：exact mode 用它调用 tokenization Render 和 KV lookup；Gateway 随后将重新建立的 reader 转发给选定 upstream。因此 Render 不会消费掉上游实际需要的 OpenAI JSON body。

现有 SSE copy loop 保持不变：上游的 data events、`[DONE]` 和分块边界原样透传，响应头写出后不 retry。每个已选路请求还会暴露 routing reason、policy、backend 和 `X-FishMesh-Exact-Status`；unknown/stale、Render/lookup 异常导致的降级可以在响应头中直接观察。

## 2. 固定契约与不变量

1. `gateway.Config.MaxRequestBodyBytes` 是 delivery owner 的硬上限，配置必须为正；`FISHMESH_MAX_REQUEST_BODY_BYTES` 默认 2 MiB；
2. `Content-Length` 超限立即拒绝；未知长度 body 最多读取上限加一个字节后拒绝，绝不形成无界 buffer；
3. body 读取成功后，requestpath 与 upstream 使用同一字节内容，但各自使用独立 reader；Gateway 不解析 JSON；
4. body 超限在调用 requestpath、Render、KV lookup 或 upstream 前返回 `413 Request Entity Too Large`；
5. route、body、routing key 都按值交给 requestpath；非 exact mode 不会在 requestpath 读取或解析 body；
6. 已取得 lease 后先写决策头，所以上游连接/流失败响应也保留可解释的 selection provenance；
7. `X-FishMesh-Exact-Status` 直接投影 requestpath 的 typed Exact 状态；`exact-signal-unavailable` 与 `exact-load-fallback-v1` 继续表示明确的 load-aware 降级；
8. SSE 响应体、first-token detector、stream failure outcome 和“headers 后不 retry”行为不改变。

## 3. 实际实现

`proxy` 现在按五个阶段执行：admission、bounded body/replay、requestpath Select、一次 upstream stream、lease Complete。bounded read 使用 `io.LimitReader(max+1)`，成功后以 `io.NopCloser(bytes.NewReader(body))` 替换请求 reader；这使 requestpath 的 Render 请求不与 upstream body 竞争。

决策头拆成选路后立即写入的部分，以及拿到 upstream URL 后写入的 `X-FishMesh-Upstream`。现有 `X-FishMesh-Route-Reason`、policy、backend/preferred backend/spillover 原样保留；新增 `X-FishMesh-Exact-Status`。

配置 owner 新增一个命名环境键并在 LoadEnvironment、Validate、Gateway 构造中检查，避免 body 上限只存在于测试或 handler 常量中。

## 4. 验证

新增/更新测试覆盖：

- body 被 requestpath 接收后仍逐字节转发给 upstream；
- 超限 body 在选路前返回 413；
- exact load-aware 降级的 reason、policy、Exact status 头和完整 SSE stream 同时可见；
- 一个集成测试实际执行 Render HTTP call → `kvcache.Index.Lookup` → exact strategy 的显式 load-aware decision → upstream SSE copy；
- Config 拒绝零 body limit，并映射 `FISHMESH_MAX_REQUEST_BODY_BYTES`。

完整门禁：

```text
go test -race ./...
go vet ./...
go build ./...
make manifest
```

## 5. 可演示闭环

该闭环不需要 GPU、Kubernetes 或改变当前 deployment；它使用进程内 Render、KV index 和 SSE upstream HTTP server：

```bash
go test -count=1 -v ./internal/serving/gateway \
  -run '^TestExactGatewayClosesRenderLookupSelectionAndSSELoop$'
```

测试验证一个 Chat 请求依次到达 `/render`、调用 KV `Lookup`、因 zero Snapshot 明确选择 load-aware，并把 `data: ...` 与 `data: [DONE]` 原样返回；同时断言 `X-FishMesh-Route-Reason: exact-signal-unavailable` 和 `X-FishMesh-Exact-Status: match-unavailable`。

## 6. 尚未交付的产品行为

当前生产组合根仍不会创建 renderer、KV index 或 Pod `Instance`，`FISHMESH_ROUTING_MODE=exact-cache-load` 也尚未开放为运行配置；现网仍是 bounded affinity。真实 vLLM KVEvents/replay、Pod UID reconcile、hard overload 阈值与线上指标将在下一部署接入切片实现。

## 7. 下一阶段

R6B-6 创建 production composition：配置 renderer/index、将 discovery/Pod identity 翻译为 kvcache instances、管理 subscriber 生命周期，并在真实受控集群执行 Render→KVEvents lookup→routing→SSE smoke。
