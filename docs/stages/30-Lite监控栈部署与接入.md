# 阶段 30：Lite 监控栈部署与接入

## 目标与不变量

将阶段 27 的监控资产变成可运行的 Lite 监控面，同时保持以下边界：

1. Prometheus 只以 namespace-scoped Role 发现 `kubellm` 的 Service/Endpoints/Pods，并用
   `fishmesh-gateway` Service 名和 `http` port 筛选 `/metrics` target；不引入 cluster-wide RBAC。
2. Grafana 管理员密码只来自集群内 `fishmesh-grafana-admin` Secret，不进入 Git 或 ConfigMap；匿名访问
   关闭，仅允许本机 `kubectl port-forward` 体验。
3. 两个 workload 都有资源 request/limit、readiness/liveness probe、non-root security context 和持久卷；
   Grafana 仅增加受限 `emptyDir` `/tmp`，以保持只读根文件系统同时避免内置插件启动写入错误。
4. dashboard 和 Prometheus rule 都自动 provision；但没有 Alertmanager/notification receiver，因此规则
   的“加载/评估”不等于已经向值班系统投递。
5. Flannel 是否执行 NetworkPolicy 仍未假定；本阶段未以 NetworkPolicy 作为隔离声明。

## 实施与真实集群验证

`deploy/monitoring` 新增 Prometheus 配置/RBAC/Deployment/Service/PVC、Grafana provisioning/
Deployment/Service/PVC，并将已有 dashboard/rules ConfigMap 挂载到对应进程。镜像固定为
`prom/prometheus:v3.3.0` 与 `grafana/grafana:11.6.0`，在 ARM64 control-plane 实际运行。

2026-08-13 在 `kubellm` 实际 `apply` 后，两个 Deployment 均 `1/1 Ready`，两个 PVC 均 Bound。
通过 Prometheus API 验证：

```text
fishmesh-gateway   up   http://10.42.1.199:8080/metrics
FishMeshGatewayUnavailable: inactive
FishMeshKVAwareSignalUnavailable: inactive
FishMeshKVReplayStale: inactive
FishMeshKVAwareDegradationRateHigh: inactive
```

通过 Grafana API 验证 datasource `fishmesh-prometheus` 指向 cluster Service，且 dashboard
`FishMesh Gateway` 的 UID 为 `fishmesh-gateway`。Grafana 当前 Pod 日志不再出现只读 `/tmp` 的内置插件
安装错误。

## 体验与验证

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  port-forward svc/fishmesh-grafana 3000:3000
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get secret fishmesh-grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 --decode; echo
```

浏览器打开 `http://127.0.0.1:3000`，以 `admin` 登录，进入 **Dashboards → FishMesh → FishMesh Gateway**。
用 README 的 Lite SSE demo 产生请求后刷新面板，即可看到 request rate、TTFT P95、KV valid/freshness、
KV-aware degradation 与 RSS。Prometheus 也可通过 `svc/fishmesh-prometheus` 端口转发检查 `/targets` 和
`/alerts`。

仓库门禁执行 `make ci` 和 `git diff --check`。R6E 仍不在本阶段范围内。
