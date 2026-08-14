# 阶段 58：Pod 身份运行时指标观测

## 1. 交付

- identity 现在保留 Pod name、Pod UID、Node name 和声明 GPU request；Pod UID 用于区分 IP 复用后的实例生命周期。
- observation 增加可选的 Prometheus HTTP API runtime collector，支持 Pod CPU、内存、GPU utilization、GPU memory 和温度。
- 每条 runtime sample 都携带 `ObservedAt`、`Source`、`Valid` 和错误语义；未配置或查询无结果不转换为零。
- Gateway `/metrics` 暴露低基数 backend runtime gauges，backend 删除时同步清理旧 label。

## 2. 安全边界

runtime PromQL 必须同时包含 `$namespace` 和 `$pod` 占位符。组合根先通过 Kubernetes Pod identity 建立归属，再替换占位符；因此节点级 GPU 指标不能直接挂到某个 vLLM backend。GPU exporter 如果没有 Pod 维度，保持 unavailable，只作为环境级证据。

runtime collector 是观测证据，不改变当前路由和 admission。后续只有在 identity、freshness 和指标语义通过真实环境验收后，才允许加入路由或动态控制。

## 3. 配置示例

```text
FISHMESH_RUNTIME_PROMETHEUS_URL=http://prometheus.monitoring.svc:9090
FISHMESH_RUNTIME_CPU_QUERY=sum(rate(container_cpu_usage_seconds_total{namespace=$namespace,pod=$pod,container!="POD"}[1m]))
FISHMESH_RUNTIME_MEMORY_QUERY=sum(container_memory_working_set_bytes{namespace=$namespace,pod=$pod,container!="POD"})
FISHMESH_RUNTIME_GPU_UTILIZATION_QUERY=avg(DCGM_FI_DEV_GPU_UTIL{namespace=$namespace,pod=$pod})
```

实际 metric 名称和 GPU Pod label 必须以目标集群 exporter 的 `/metrics` 及 Prometheus 查询结果为准；仓库不把上述示例当作已验证的部署事实。

## 4. 验证

- 单测验证 Prometheus query API 按 Pod identity 替换、部分指标可用、无 Pod identity 以及未绑定 query 拒绝。
- `go test -race ./...`、`go vet ./...`、`go build ./...`、`make manifest` 和 `git diff --check` 通过。
- 本阶段没有连接真实 GPU/DCGM 数据源，没有把 runtime 指标用于 routing，也没有声称完成真实运行时收益验收。
