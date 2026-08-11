# FishMesh Lite 故障 Runbook

> 适用范围：Lite Gateway、vLLM 与真实 KVEvents。参考 K3s 集群运行 `deploy/monitoring` 的 Prometheus
> 与 Grafana，且已验证 Gateway scrape、规则加载与 dashboard provision；未部署 Alertmanager 或外部通知，
> 因此不能把规则视为已接入值班系统。

## 监控面板与规则

在本机仅通过端口转发访问，不暴露 Grafana Service：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm \
  port-forward svc/fishmesh-grafana 3000:3000
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get secret fishmesh-grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 --decode; echo
```

访问 `http://127.0.0.1:3000`，以 `admin` 和上面读取的密码登录，打开 **Dashboards → FishMesh →
FishMesh Gateway**。Prometheus 的 `/targets` 应显示 `fishmesh-gateway` 为 `up`；`/alerts` 显示四条
FishMesh 规则。规则状态与通知投递不同：此 Lite 栈没有 Alertmanager。

R6F 后面板还显示两个严格区分的 histogram：**KV publisher-to-apply lag P95** 与 **KV-aware cached
prefix tokens**。前者仅来自成功 apply、带 publisher timestamp 的 batch，并按 `live`/`replay` 分开；
replay 的大值可能只是历史 event 到当前重放的年龄，不能当作 ZMQ 网络 RTT。后者只统计
`X-FishMesh-KV-Status: available` 的选择；零表示真实 miss，`match-unavailable` 不会进入该图。

## 先做什么

1. 保存 Gateway `/metrics`、Gateway logs、Pod 描述和响应头；不要记录 prompt、token IDs 或 session key。
2. 检查 `kubectl -n kubellm get deploy,pod,endpointslice`。只有 Gateway 1/1、vLLM 2/2 Ready 时才将
   流量问题归因给路由。
3. unknown/stale KV 信号不是零命中。`kv-aware-load-fallback-v1` 是正确的 load-balanced 降级，不能通过
   重试把它伪装成 KV-aware hit。

## Gateway unavailable

```bash
kubectl -n kubellm get deploy,pod -l app.kubernetes.io/name=fishmesh-gateway
kubectl -n kubellm describe pod -l app.kubernetes.io/name=fishmesh-gateway
kubectl -n kubellm logs deployment/fishmesh-gateway --since=30m
```

检查 `/readyz`、资源限制、镜像是否已离线导入，以及 EndpointSlice RBAC。若 rollout 失败，使用受审阅的
上一版 overlay/image revision 回滚；不要在流式响应 header 已发出后代理重试。

## KV-aware signal unavailable

症状：`X-FishMesh-KV-Status: match-unavailable`、`X-FishMesh-Policy: kv-aware-load-fallback-v1` 或
`fishmesh_gateway_kv_aware_degradations_total` 增长。

```bash
kubectl -n kubellm logs deployment/fishmesh-gateway --since=30m | grep -E 'KV|replay|sequence|subscriber'
kubectl -n kubellm get endpointslice -l kubernetes.io/service-name=qwen-vllm -o wide
kubectl -n kubellm port-forward svc/fishmesh-gateway 8080:8080
curl -sS http://127.0.0.1:8080/metrics | grep -E 'fishmesh_gateway_(kv_cache|kv_aware_)'
```

确认 `instance_valid`、freshness、sequence 与 EndpointSlice 新旧 Pod UID 对齐。若发生 event apply failure、
sequence gap 或 ZMQ/replay 连接异常，保留上述证据并停止猜测；保持降级语义，不把 `Valid=false` 解释成
零 cached tokens。

## KV replay stale

检查 GPU 节点与 vLLM Pod 的健康、5557/5558 事件端口、EndpointSlice 身份和 Gateway logs。Pod rollout
期间可暂时降级；等待一个 discovery/replay 周期后再检查。若持续 stale，停止 KV-aware 验收或 benchmark，
恢复 `deploy/experiments/r6d-session-key`，再按阶段 26 的证据流程处理。

## GPU 过热或节点不可达

在 GPU 节点检查：

```bash
nvidia-smi dmon -s pucv -d 10
sudo journalctl -u gpu-watchdog --since '30 minutes ago'
```

预计超过 30 分钟的实验必须持续监控。温度持续超过 80°C、或出现 `gpu-watchdog` WARN/CRITICAL 时，立即
停止 GPU workload，等待低于 70°C；不要提高 loadgen 默认并发 4 或并行启动多个 GPU workload。节点
`NotReady` 时先恢复节点/K3s agent，不能从残缺 KVEvents 推断路由缺陷。

## 结束与恢复

任何 KV-aware 试验后执行：

```bash
kubectl apply -k deploy/experiments/r6d-session-key
kubectl -n kubellm rollout status deployment/fishmesh-gateway --timeout=5m
kubectl -n kubellm rollout status deployment/qwen-vllm --timeout=25m
```

记录 Gateway/vLLM readiness、路由模式、温度峰值、watchdog 状态和原始日志位置。Flannel 不执行
NetworkPolicy；不得将仓库中保留的 policy YAML 表述为当前已生效的隔离。
