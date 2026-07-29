# Serving baseline

`deploy/baseline/base` 现在代表 V2 的默认 Serving 基线：

- Gateway → vLLM 使用 Kubernetes Service；
- Gateway → vLLM 默认开启 HTTP keep-alive；
- 路由模式为 `service`；
- Gateway、Loadgen、vLLM 仍保持可替换边界，后续策略通过实验 overlay 接入。

建议流程：

```bash
kubectl apply -k deploy/baseline/base
kubectl apply -f deploy/baseline/jobs/service-keepalive-baseline.yaml
```

`deploy/baseline/jobs/random-service-baseline.yaml` 是旧的 no-keepalive 记录，保留用于
历史复现；新的 no-keepalive 对照请使用
`deploy/experiments/connection-matrix/gateway-service-no-keepalive.yaml` 与对应 Job，
避免误把历史条件当成默认运行方式。
