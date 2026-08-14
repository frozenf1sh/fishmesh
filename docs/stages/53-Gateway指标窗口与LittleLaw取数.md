# 阶段 53：Gateway 指标窗口与 Little’s Law 取数

## 1. 目标

把客户端 workload 证据和 Gateway admission 事实放在同一份 benchmark report 中，避免用客户端
completed QPS 冒充 Gateway accepted QPS，也避免把瞬时 in-flight 当成平均并发。

## 2. 实现

- `fishmesh-client bench --metrics-endpoint <Gateway /metrics>` 可选开启 Gateway metrics 采样，默认关闭，
  不改变既有 benchmark 行为；
- 按采样间隔读取低基数的 `admitted_requests_total`、`requests_total` 和 `inflight_requests`，完成计数器按
  全部 method/status 求和；
- report 写入 admitted/completed counter delta、accepted/completed QPS、时间加权平均 in-flight 和
  Little’s Law `W = L / λ`；
- counter reset、缺失指标、采样失败或窗口不足时，Gateway metrics 标记为 invalid，但不取消 workload；
- 不采集 prompt、Token IDs、routing key、upstream URL，也不把 metrics 采样值用于在线路由。

## 3. 解释边界

```text
lambda_accepted = delta(admitted_requests_total) / window
L_gateway       = time_weighted_average(inflight_requests)
W_gateway       = L_gateway / lambda_accepted
```

这组结果描述 Gateway 侧接受的请求，不等于客户端 offered rate。当前 window 覆盖整个 benchmark invocation；
如果计划包含 warmup，报告会写出 `warmup_requests`，正式容量结论应使用零 warmup，或后续实现分段窗口。

Gateway metrics reader 仍只提供 vLLM/Gateway 进程侧事实；容器 CPU、GPU 利用率、显存等信号没有伪装成
per-backend load，也没有进入路由评分。

## 4. 验证

- Prometheus 文本解析、completed status 求和、counter reset、Little’s Law 数值计算和非法 endpoint 测试；
- `go test -race ./...`、`go vet ./...`、`go build ./...`、`make manifest`、`git diff --check`；
- 本阶段没有 rollout、GPU 压测或容量结论。
