# FishMesh monitoring stack

This directory installs the Lite monitoring stack: namespace-scoped Prometheus, Grafana, dashboard, alert rules
and their persistent data volumes. It has been deployed and verified in the reference K3s cluster: Prometheus
successfully scrapes the Gateway target; all four rules are loaded; Grafana has provisioned its Prometheus datasource
and the `FishMesh Gateway` dashboard.

`kubectl apply -k deploy/monitoring` creates the workloads and ConfigMaps. Prometheus is deliberately limited to
discovering scrape targets in `kubellm`, and retains its local data for seven days. Grafana has no anonymous access;
its admin password comes only from the uncommitted `fishmesh-grafana-admin` Secret.

Install or update it with:

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/monitoring
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deploy/fishmesh-prometheus --timeout=5m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deploy/fishmesh-grafana --timeout=5m
```

For a different cluster, create the Secret before applying (do not put the password in Git):

```bash
kubectl -n kubellm create secret generic fishmesh-grafana-admin \
  --from-literal=admin-password="$(openssl rand -base64 32)"
```

The supplied dashboard shows request rate, TTFT P95, exact-safe KV instances, replay freshness, degradation rate,
Gateway RSS, live/replay publisher-to-apply lag P95, and exact cached-prefix-token P50/P95. Publisher-to-apply lag
is not ZMQ network RTT: replay can correctly show the age of historical publisher timestamps. Cached-prefix tokens
only include `Exact-Status: available`; a zero is a real miss, while unavailable state is excluded. The rules are
loaded directly from the mounted ConfigMap; no `PrometheusRule` CRD is required.

If integrating these assets into another existing monitoring stack instead, arrange all of the following:

- Configure Grafana to discover the `grafana_dashboard: "1"` label or import the dashboard JSON.
- Configure Prometheus to discover the `prometheus_rule: "1"` label or mount the rule file; do not replace it with
  `PrometheusRule` unless that operator CRD is installed.
- Scrape the existing Gateway Pod annotations: `/metrics` on port `8080`.

First validate the integration and alert delivery in that environment. The alert thresholds are Lite operating
signals, not SLO promises. This deployment has no Alertmanager/notification receiver, so it evaluates visible rules
but does not deliver external notifications.
