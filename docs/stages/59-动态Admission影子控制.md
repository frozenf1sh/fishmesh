# 阶段 59：动态 Admission 影子控制

## 1. 交付

- admission 将 `MaxInflight` 固定 hard limit 与可调整的 `target` 分离。
- `TryAcquire` 只允许 `active < target` 的新请求进入；`SetTarget` 永远不撤销已有 permit。
- 新增 `off`、`shadow`、`active` 三种调谐模式：默认 off，shadow 只发布建议，active 才实际调整新请求准入。
- 控制器使用 Gateway 的 admitted/completed/rejected/in-flight 单调计数，带 stale/reset 检查、最小/最大 target、步长、滞回水位和 cooldown。
- Gateway metrics 新增 target、hard limit、suggested target、signal validity、change count、mode 和 reason。

## 2. 连接生命周期语义

target 下调时，当前 SSE/HTTP 请求继续持有原 permit 并正常完成；只有新请求会被拒绝，直到 active 数量降到新 target 以下。target 上调只逐步放开新请求。该设计不需要修改连接、不需要强制取消、不在 Gateway 内建立等待队列。

## 3. 配置

```text
FISHMESH_MAX_INFLIGHT_REQUESTS=128       # 不可突破的 hard limit
FISHMESH_ADMISSION_INITIAL_TARGET=128    # 启动时 soft target
FISHMESH_ADMISSION_TUNING_MODE=off      # off|shadow|active
FISHMESH_ADMISSION_MIN_TARGET=16
FISHMESH_ADMISSION_MAX_TARGET=128
FISHMESH_ADMISSION_TARGET_STEP=8
FISHMESH_ADMISSION_TUNING_INTERVAL=2s
FISHMESH_ADMISSION_TUNING_COOLDOWN=5s
```

正式实验先跑 shadow，再使用同一 workload 切换 active；不得把 shadow 的 suggested target 当作已经生效的 admission 上限。

## 4. 验证

- 单测覆盖 target 下调不撤销已有 permit、active 调整、shadow 不改变 target、counter reset/stale 保护。
- Gateway metrics 保留 Little’s Law 所需的 admitted/completed/in-flight/rejected 事实，并额外暴露控制器状态。
- 真实实验仍待后续 stage：本阶段没有把默认部署切换到 active，也没有形成新的 GPU 收益结论。
