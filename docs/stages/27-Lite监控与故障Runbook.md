# 阶段 27：Lite 监控与故障 Runbook

## 目标与交付契约

Lite MVP 需要让运行者能看见 Gateway、KVEvents 和 exact 降级的健康状态，并能以可复核的
步骤处理故障。本阶段只交付仓库内、可被现有 Prometheus/Grafana 安装接纳的资产：

- `deploy/monitoring/grafana-dashboard.yaml`：一个标有 `grafana_dashboard: "1"` 的
  Grafana dashboard ConfigMap；
- `deploy/monitoring/prometheus-alert-rules.yaml`：一个标有 `prometheus_rule: "1"` 的
  标准 Prometheus rule 文件 ConfigMap；
- `docs/notes/runbook.md`：Gateway 不可用、KV 信号无效/replay 过期、exact 降级和 GPU 温度
  事件的取证与恢复步骤；
- 默认 `make manifest` 渲染 `deploy/monitoring`，以保证这些 YAML 在提交门禁内。

契约边界如下：dashboard 查询已经存在的 Gateway `/metrics` 指标；告警规则只是规则文件，
不会因为被 `kubectl apply -k deploy/monitoring` 而自动被 Prometheus 读取。部署方必须把标签、
ConfigMap 挂载或对应 operator 的规则发现方式接入已有监控栈，并先验证 scrape 与告警投递。

## 不变量

1. unknown/stale KV match 与零 token 命中不同；runbook 一律先检查 `Valid`/freshness，再解释
   exact cache hit。
2. 参考集群未安装 Prometheus、Grafana 或 `monitoring.coreos.com` CRD。仓库不伪造
   `ServiceMonitor`、`PrometheusRule` 已可用，也不声称 dashboard/alerts 已实集群验证。
3. Gateway 已暴露的 `/metrics` 保持唯一数据面；本阶段不改 Go 核心逻辑、不添加采集 sidecar。
4. Flannel 环境中 NetworkPolicy 是否生效取决于 CNI 能力；runbook 不将它作为隔离或恢复前提。
5. GPU 过热警告优先于性能实验。发现 `gpu-watchdog` WARN/CRITICAL 或温度持续超过 80°C 时，
   停止负载并等待降至 70°C 以下。

## 告警语义

| 告警 | 条件 | 首要处置 |
| --- | --- | --- |
| `FishMeshGatewayUnavailable` | Gateway `up` 连续 5 分钟为 0 | 检查 Pod、Service、EndpointSlice 与 scrape 目标 |
| `FishMeshExactSignalUnavailable` | 所有实例 `kv_cache_instance_valid` 连续 2 分钟为 0 | 取证 subscriber/replay/EndpointSlice，保留 load-aware 降级 |
| `FishMeshKVReplayStale` | freshness 大于 30 秒超过 2 分钟 | 检查 KVEvents/replay 链路和 sequence 日志 |
| `FishMeshExactDegradationRateHigh` | 5 分钟降级速率高于 0.1/s 持续 10 分钟 | 按 reason 区分 stale/unknown/overload，禁止把 unknown 当 miss |

阈值是 Lite 运行信号而非 SLO 承诺，需在真实 scrape 数据与业务流量上校准。

## 验证

在没有监控栈的参考集群中，仅做静态可安装性验证：

```bash
kubectl kustomize deploy/monitoring >/dev/null
make ci
git diff --check
```

本阶段未执行 Grafana 导入、Prometheus 抓取或真实告警投递；这些验证被明确保留给有监控栈的
环境。下一阶段更新面向用户的五分钟 demo，使其不将该未验证资产误写为已经上线。
