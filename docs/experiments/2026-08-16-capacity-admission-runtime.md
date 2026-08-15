# R6I 容量、动态 Admission 与 Runtime 路由实验手册

## 结论先行

当前代码和配置已经具备实验条件，但本文件不是一次已经执行的 GPU 报告。执行顺序必须是：

1. 先验证集群、Pod 身份、Gateway `/metrics` 和 runtime freshness；
2. 先跑固定 admission baseline，再跑 shadow，最后才允许 active；
3. 每个 treatment 保留同一份 plan、requests JSONL、report JSON/Markdown 和集群快照；
4. 只有正确性、长连接安全和指标有效性都通过，才比较 QPS、并发、TTFT 和 Little’s Law。

`session-key` 不属于维护路径，也不进入收益主对照；它只保留兼容回归。

## 1. 实验矩阵

### Admission 轴

| 编号 | 配置 | 目的 |
| --- | --- | --- |
| A0 | `FISHMESH_ADMISSION_TUNING_MODE=off`，初始 target=hard limit | 当前固定并发基线 |
| A1 | `off`，离线选择的固定 target | 动态控制的静态 oracle |
| A2-shadow | `shadow`，其余参数与 A0 相同 | 观察 suggested target，不改变准入 |
| A2-active | `active`，从 A2-shadow 已验证的参数开始 | 验证阶梯负载下的动态控制 |

A2-active 不得直接作为首轮实验。先用相同 workload 完成 A2-shadow，并确认 signal valid、target 没有
异常振荡，再切换 active。A1 与 A2-active 必须在相同 hard limit、模型、Pod 数和 workload seed 下比较。

### Routing 轴

| 编号 | 配置 | 目的 |
| --- | --- | --- |
| B0 | `load-balanced`，仅本地事实可用 | 本地计数消融 |
| B1 | `load-balanced`，启用 vLLM queue/running | 当前普通负载感知主线 |
| B2 | `kv-aware`，KV 失败显式回退 B1 | KV locality 与降级正确性 |
| B3 | B1/B2 加 fresh、Pod-mapped runtime hard gate | runtime 信号的安全增量 |

B3 只启用已经在目标集群验证过身份和 freshness 的阈值；不把节点级 GPU 指标直接绑定到 Pod，也不把
不同量纲拼成未经校准的 weighted score。

### Routing 的短上下文旁路

KV-aware 的短上下文优化使用独立实验 overlay：
`deploy/experiments/r6i22-final/kv-aware-short-bypass`。初始 `2048` 只是候选值，不能直接作为默认；阶段 66
已完成 512/1024/2048/3072 threshold sweep 与 threshold 576 双轮 repeat。Gateway 先执行精确 tokenization，再在阈值内跳过
per-request KV lookup，改用 load-aware 普通选择；这不是 KV failure，响应应记录
`X-FishMesh-KV-Status: short-context-bypassed`、短上下文 fallback reason/policy 和
`kv_aware_bypasses_total`。阈值为 0 时关闭，KV unknown/stale 仍按原有显式 degradation 处理。

当前决策是按 model + hardware + vLLM profile 实验确定并在运行期固定；本参考 profile 的候选为 `576`，只覆盖
512-token 极短请求，默认仍关闭。完整结果见
[`2026-08-16-r6i24-kv-short-threshold.md`](2026-08-16-r6i24-kv-short-threshold.md)。

## 2. 准入检查与安全边界

先确认 kubeconfig 和 namespace，再确认不在执行别的 rollout 或压测：

```bash
export KUBECONFIG="$HOME/.kube/fishmesh.yaml"
kubectl get nodes -o wide
kubectl -n kubellm get deploy,pod -o wide
kubectl -n kubellm get endpointslice -l kubernetes.io/service-name=qwen-vllm -o wide
kubectl -n kubellm rollout status deploy/fishmesh-gateway --timeout=5m
kubectl -n kubellm rollout status deploy/qwen-vllm --timeout=5m
```

以下任一项失败就停止，不进入正式窗口：Gateway 非 Ready、vLLM 副本数不足、EndpointSlice 没有预期
backend、Pod 重启计数增加、模型/generation/config 与上一 treatment 不一致，或上一次实验产物缺失。

先只渲染，不 apply：

```bash
kubectl kustomize deploy/experiments/admission-shadow >/tmp/fishmesh-admission-shadow.yaml
kubectl kustomize deploy/experiments/admission-active >/tmp/fishmesh-admission-active.yaml
```

实际切换属于外部集群变更，必须在确认窗口后执行。切换后至少检查：

```bash
kubectl -n kubellm apply -k deploy/experiments/admission-shadow
kubectl -n kubellm rollout status deploy/fishmesh-gateway --timeout=5m
kubectl -n kubellm get pod -l app.kubernetes.io/name=fishmesh-gateway -o wide
```

active overlay 只允许在 shadow 证据通过后使用；实验结束恢复默认 `load-balanced` + admission `off`，并
再次等待 rollout 与 `/readyz` 通过。

## 3. 端点和 runtime 观测校验

用本地 port-forward 隔离实验客户端与集群网络：

```bash
kubectl -n kubellm port-forward svc/fishmesh-gateway 8080:8080
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/metrics > /tmp/fishmesh-gateway.metrics
rg 'admitted_requests_total|completed_requests_total|inflight_requests|admission_rejections_total|admission_target|backend_(cpu|memory|gpu)' /tmp/fishmesh-gateway.metrics
```

runtime PromQL 必须以目标 Prometheus 和 exporter 的实际 metric 名称为准，并且每条 backend query 都要
包含 `$namespace` 与 `$pod`。先确认 Pod UID、Pod label、DCGM/container exporter 维度能够把样本归属到同一
backend；只有在 `/metrics` 中看到有效、持续更新的 runtime sample，才打开 B3 hard limit。缺失、过期、
NaN/Inf 或没有 Pod 归属的 sample 必须保持 unavailable，不能当作 0。

## 4. 正式运行顺序

每个 treatment 都使用新的 `run-nonce`，但同一轮 A/B 使用相同的 workload seed、模型、vLLM cache generation、
Pod 数和 `configs/*.json`。先做一轮小 smoke，再做至少两轮正式窗口。

### 4.1 固定容量与路由基线

```bash
fishmesh-client bench \
  --endpoint http://127.0.0.1:8080 \
  --plan configs/capacity-ladder.json \
  --metrics-endpoint http://127.0.0.1:8080/metrics \
  --metrics-interval 250ms \
  --run-id a0-r1 \
  --treatment a0-fixed-load-balanced \
  --run-nonce a0-r1-20260816 \
  --cache-generation <vllm-generation> \
  --output-dir artifacts/bench/a0-r1
```

依次对 A1、A2-shadow、B0、B1、B2、B3 替换 treatment 和 output directory。B2 使用
`configs/routing-ablation.json`，动态控制使用 `configs/admission-dynamic-step.json`，长连接安全使用
`configs/long-connection-drain.json`。模板中的 `run_nonce`、`cache_generation` 和 treatment 不能原样提交为
正式产物。

### 4.2 Shadow 到 active

1. A2-shadow 运行完整低→中→高→恢复阶梯，记录 `admission_target`、`admission_suggested_target`、
   `admission_tuning_actions_total`、signal validity、reason 和 Gateway metrics window。
2. 检查 shadow 不改变 admitted/rejected 事实；如果 counter reset、signal stale、target 频繁上下摆动，
   只保留 shadow 失败证据，不进入 active。
3. 应用 active overlay，保持 workload、seed、cache generation 和窗口划分不变，再重跑同一阶梯。
4. active 只影响新请求准入；target 下调期间已有 SSE permit 不会被撤销，必须持续收到 SSE 并正常完成或由
   客户端取消。新请求可以 capacity reject，但不能在 Gateway 内隐式排队。

## 5. 连接安全实验

`long-connection-drain` 用于专门验证动态 target 对已有连接的语义：

1. 固定 target 下建立一批长 SSE，并记录客户端 request ID、建立时间和结束原因；
2. 让 active 控制器建议并执行低于当前 in-flight 的 target；
3. 检查已有连接没有被主动关闭、SSE 没有截断、没有 upstream retry；
4. 检查新请求的 rejection 与 target 变化一致；
5. 等旧连接释放后，再发送同等请求，确认 admission 可以恢复。

任何已有连接因为 target 调整而断流、Permit 被撤销或出现不可解释的完成状态，都判定 A2-active 失败，
即使平均 TTFT 或 rejection rate 看起来更好。

## 6. 收集产物

每轮至少保留：

- `plan.json`：最终实际运行计划，包含 treatment、seed、run nonce、cache generation；
- `requests.jsonl`：每请求 latency、TTFT、错误分类、routing/KV headers 和 prompt-token evidence；
- `report.json` / `report.md`：全局、scenario、batch Gateway window；
- `comparison.json` / `comparison.md`：多轮 A/B 汇总；
- `kubectl get ... -o yaml` 快照：Deployment、Pod、EndpointSlice、ConfigMap、Pod UID；
- Gateway `/metrics`、Prometheus runtime 查询结果、事件时间线和异常日志。

不要只留均值、截图或终端输出。Gateway invalid window、counter reset、runtime stale 和请求失败必须原样
保留并标记原因，不能改成零吞吐或零并发。

## 7. 收益对比与判定

请求级 TTFT/E2E 用原有 bootstrap compare；容量级证据用 report JSON：

```bash
fishmesh-client compare \
  --baseline artifacts/bench/a0-r1/requests.jsonl \
  --treatment artifacts/bench/a2-active-r1/requests.jsonl \
  --baseline-report artifacts/bench/a0-r1/report.json \
  --treatment-report artifacts/bench/a2-active-r1/report.json \
  --bootstrap 2000 \
  --seed 20260816 \
  --output-dir artifacts/bench/compare-a0-a2-active-r1
```

多轮时重复 `--baseline`/`--treatment` 及对应 report 参数，保证每个 requests/report 是同一 run。最终表格
至少包含：

| 类别 | 指标 | 判定方式 |
| --- | --- | --- |
| 正确性 | 成功率、错误/超时、KV degradation、runtime freshness | 先于性能；任一不可解释失败则不推广 |
| 连接安全 | 已有 SSE 中断、取消、Permit 撤销 | 已有连接中断必须为 0 |
| 容量 | offered、accepted、completed、rejected QPS | 同一有效 Gateway window；invalid 不算 0 |
| 并发 | 时间加权平均 in-flight、target、hard limit | 报告动态变化和稳定时间，不只报峰值 |
| 排队 | Little’s Law `W=L/λ` 与 observed duration/throughput 残差 | 只在稳定窗口比较，并记录残差 |
| 用户体验 | TTFT/E2E P50/P95/P99、TPOT/ITL | 与成功率、rejection 一起比较 |
| 资源 | Pod CPU/内存、GPU utilization/memory/temperature | 仅使用已完成 Pod identity 映射的 fresh sample |

动态 admission 的收益必须同时满足：在相同 SLO 下 completed QPS 不下降、tail latency 不恶化到阈值外、
拒绝/错误没有转移成 upstream failure、Little’s Law 残差可解释、target 没有持续振荡、长连接中断为 0。
稳态容量要与 A1 静态 oracle 比，阶梯/突发恢复才是 A2 相对 A1 的主要收益场景。单张 time-sliced GPU 只能得出
当前环境的相对 profile 结论，不能外推为独立 GPU 横向扩展收益。

## 8. 停止条件与回滚

遇到 Pod 重启、Gateway Ready 失败、SSE 断流、KV unknown 被错误当成命中、runtime sample 无法归属、metrics
counter reset 或报告窗口无效时立即停止当前 treatment，保留失败产物。不要为了“跑完矩阵”继续扩大压力。

实验结束后恢复默认配置，并重复 `rollout status`、`/readyz`、vLLM Pod 数、EndpointSlice 和 Gateway metrics
检查。只有恢复证据完整，才可以把本轮结果写成阶段结论。
