# Cluster Analyst 骨架

当前阶段实现的是一个慢速、只读的 Infra Analyst 控制面，不是聊天机器人，也不在
Gateway 请求路径中调用 LLM。

```text
Incident API
    -> Analysis Engine
        -> domain tools
            -> structured Signals
        -> RulePolicy
            -> Diagnosis + Evidence + Recommendation
```

## 代码边界

```text
cmd/fishmesh-analyst/                    # 进程生命周期、环境变量和优雅关停
internal/diagnostics/domain/             # Incident/Signal/Diagnosis 和规则策略
internal/diagnostics/application/        # Tool Registry、Engine 和应用端口
internal/diagnostics/adapters/           # demo、Gateway metrics 等外部适配器
internal/diagnostics/delivery/           # /healthz、/readyz、/v1/tools、/v1/analyze
internal/diagnostics/config/             # 环境变量解析与启动校验
```

MVP 的 `RulePolicy` 不依赖 LLM，因此没有 API key、向量库或 Memory。未来接入 API LLM
时，只增加一个结构化 narrator/planner 实现，不改变 Signal、Diagnosis 和安全边界。

## 本地验证

```bash
go run ./cmd/fishmesh-analyst
curl -s http://127.0.0.1:8090/v1/tools | jq
curl -s -X POST http://127.0.0.1:8090/v1/analyze \
  -H 'content-type: application/json' \
  -d '{"metric":"ttft_p99","description":"TTFT P99 increased"}' | jq
```

默认 `demo` 模式会返回：

```text
diagnosis.code = prefix_locality_degraded
recommendation.code = enable_bounded_prefix_affinity
```

这个 fixture 明确表达“Prefix Cache 命中下降、网络正常、GPU 未饱和、队列正常”的
可验证场景。它不是线上指标，不应被写入实验报告。

## K3s 验证

先构建并导入镜像，再部署 demo：

```bash
make image
kubectl apply -k deploy/analyst/base
kubectl -n kubellm rollout status deploy/fishmesh-analyst
kubectl -n kubellm port-forward svc/fishmesh-analyst 8090:8090
```

只接入 Gateway 真实指标时使用 overlay：

```bash
kubectl apply -k deploy/analyst/gateway-metrics
```

接入 vLLM 指标和 Kubernetes Events/Pod 状态时使用 observability overlay：

```bash
kubectl apply -k deploy/analyst/observability
```

`observability` 模式接入 `query_gateway_stats`、`query_llm_metrics` 和
`query_kubernetes_events`；GPU/DCGM URL 和 eBPF collector 未配置时会返回
`unavailable`。此时规则引擎会输出 `insufficient_observability`，不会把缺失数据误认为健康。

当前 K3s 验证结果已经包含真实信号：vLLM `queue_length=0`、`running_requests=0`、
Prefix Cache 命中率约 `0.83`，Kubernetes Events/Pod 状态为 `ok`；GPU 与 eBPF 因尚未部署
对应 exporter，仍会明确标记为 `unavailable`。

## 安全和演进边界

- analyst 使用独立 ServiceAccount，默认不挂载 Kubernetes token。
- Deployment 设置非 root、只读根文件系统、资源请求/限制和健康探针。
- NetworkPolicy 文件暂不加入 base，因为当前 K3s Flannel 不执行 NetworkPolicy；迁移到
  支持策略的 CNI 后再启用并验证。
- 当前 API 是同步单轮分析；长耗时异步任务、LLM tool-calling、Recommendation
  actuator 和 Incident Memory 都属于后续阶段。
