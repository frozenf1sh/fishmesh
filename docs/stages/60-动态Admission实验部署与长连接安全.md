# 阶段 60：动态 Admission 实验部署与长连接安全

## 1. 实验 overlay

- `deploy/experiments/admission-shadow`：只计算 suggested target，不改变新请求准入。
- `deploy/experiments/admission-active`：启用 active target，仅用于受控实验，不作为默认产品 overlay。

两份 overlay 都继承 `lite-kv-aware`，只修改 ConfigMap 的 tuning mode 和 runtime annotation；不得把 active overlay 直接当生产默认。

## 2. 长连接 drain 验证

1. 先以固定 target 建立若干长 SSE 请求，记录 active connection 数和 request ID。
2. 将配置切换为 active，并让控制器把 target 下调到低于当前 active 数。
3. 确认已有请求持续收到 SSE、最终正常完成或由客户端取消；新请求收到 capacity rejection，不进入 Gateway 隐式等待队列。
4. 等已有请求释放 permit 后，再发送新请求，确认新请求可以进入。

必须同时保存 Gateway metrics、客户端 requests JSONL、SSE 完成/取消分类和 rollout event。任何已有连接被 target 调整主动关闭，都判定为失败。

## 3. 过期信号验证

停止或隔离 Gateway metrics signal 的更新，确认 controller reason 变为 `signal-stale-or-invalid`，target 不继续向危险方向变化。恢复 signal 后，先观察一个完整窗口再允许新的调整。

## 4. Kubernetes 验收边界

overlay 的 kustomize 渲染只证明 YAML 结构；真实验收仍需分别确认 Gateway rollout、Pod Ready、`/readyz`、vLLM 2/2、EndpointSlice、runtime metrics freshness 和没有重启/断流。未执行这些检查前不报告 active 控制器的 GPU 收益。
