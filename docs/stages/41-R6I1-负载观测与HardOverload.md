# 阶段 41：R6I-1 负载观测与 HardOverload

## 1. 目标与边界

本阶段只补齐 R6H 固定成本路由所缺失的真实负载安全边界，不引入 TTFT estimator、在线学习或新的
常驻组件。实现完成后，Lite manifest 会直接读取两个 vLLM 的 Prometheus queue/running，并由
requestpath 把外部观测与本 Gateway 的 local in-flight 合成一次不可变 routing snapshot。

## 2. 实现

- config 新增 observation request timeout、hard queue 和 hard local in-flight 三个环境键；零门槛
  表示关闭，负值和非正 timeout 在启动期拒绝；
- Lite profile 显式使用 `500ms` interval、`2s` max age、`400ms` request timeout；
- queue 达到 16 或单 backend local in-flight 达到 32 时发布 `routing.Load.HardOverload=true`；
- queue unknown/stale 时不触发 queue 门，local 门仍独立工作；
- 外部 queue/running 完整有效时，成本不再重复叠加完整 local in-flight；外部负载未知时仍使用
  local in-flight 作为单 Gateway fallback；
- Prometheus collector 和 Kubernetes Pod identity enrich 都受 request timeout 约束；
- Lite namespace Role 增加 Pods `get/list`，用于既有 identity enrich。权限仍不包含 Secrets、写操作
  或 cluster-scope 资源。

HardOverload 的所有权没有移动：requestpath 负责发布由多种事实合成的门，routing 负责在 cache
locality 比较前强制排除。所有候选都越过门槛时继续返回
`kv-aware-hard-overload-fallback`，没有引入随机拒绝或隐藏 Service fallback。

## 3. Contract tests

已覆盖：

- queue 等于门槛即触发；
- 缺失/无效 queue 不触发 queue 门；
- 没有 observation 时 local 门仍触发；
- load valid 时 local in-flight 不重复计费；
- load invalid 时 local in-flight 继续参与成本；
- 全部候选 hard overload 时 typed fallback；
- 环境变量映射、负门槛和非正 timeout 拒绝。

## 4. 部署与回滚边界

本阶段构建并离线导入 `fishmesh-gateway:r6i1-load-gates-r1`，manifest digest 为
`sha256:d8db3eac20b9cedadbec853793f16fb56a73bcd209986d9e79946d3fb4854a35`；overlay 的 image、revision
和 digest 已同步。回滚只需要恢复
上一 Gateway revision，并把 observation mode 设回 `none`；HardOverload 默认门槛为零，因此基础
`load-balanced` profile 行为不变。

真实 smoke 开始前必须确认 GPU Node Ready、vLLM 2/2 Available、EndpointSlice 有两个 Ready backend。
任何 GPU Node NotReady、持续 CrashLoop/Unknown 或 vLLM 长时间 1/2 都停止，不继续 rollout 或压测。

## 5. 验证状态

- `go test -race -count=1 ./...`、`go vet ./...`、`go build ./...`、`make manifest`、
  `git diff --check`：通过；
- rollout 后 GPU Node 保持 Ready，vLLM 2/2 Available、Gateway 1/1 Ready，两个 EndpointSlice backend
  均 ready/serving；
- 两 backend observation 均为 `ok`，空闲 freshness 约 0.09–0.30 秒；
- 8 个 `ignore_eos` 流式请求使一个 backend 的 running sample 从 `0` 升到 `8`，请求结束后恢复 `0`；
- Pods `list=yes`，Secrets/Nodes 读取为 `no`；
- 本轮 queue 始终为 0，没有为了制造过载继续提高并发。因此 HardOverload 真实触发仍未验收，当前证据只
  证明 observation 接线、running 变化和恢复，不是性能改进结论。

## 6. 下一阶段

R6I-2 只建立 calibrated-static estimator 的 profile/value contract 和纯函数测试；没有目标硬件数据时
profile 必须标记 uncalibrated，也不能改变实际 routing。
