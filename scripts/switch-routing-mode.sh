#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 只指向 fishmesh 集群文件,避免误用本机 KUBECONFIG 里的多集群列表。
kubeconfig="${FISHMESH_KUBECONFIG:-$HOME/.kube/fishmesh.yaml}"
namespace="${FISHMESH_NAMESPACE:-kubellm}"

usage() {
  cat <<'EOF'
用法: scripts/switch-routing-mode.sh <round-robin|load-balanced|load-aware|session-key|kv-aware>

  round-robin    无调度信号的请求级轮转实验基线
  load-balanced  仅按 Gateway local in-flight 的普通负载均衡
  load-aware     优先按 vLLM queue/running 选路，观测不可用时回退 load-balanced
  session-key    客户端 session key 的有界粘性
  kv-aware       真实 KV 感知路由

五个 overlay 只改 Gateway 的 FISHMESH_ROUTING_MODE 和 runtime-profile 注解;
vLLM 副本已带 KVEvents,切换不触发 vLLM 重启,只滚动 Gateway。
EOF
}

mode="${1:-}"
case "$mode" in
  round-robin)
    overlay="deploy/experiments/r6i22-final/round-robin"
    ;;
  load-balanced)
    overlay="deploy/experiments/r6i22-final/load-balanced"
    ;;
  load-aware)
    overlay="deploy/experiments/r6i22-final/load-aware"
    ;;
  session-key)
    overlay="deploy/experiments/r6d-session-key"
    ;;
  kv-aware)
    overlay="deploy/lite-kv-aware"
    ;;
  *)
    usage
    exit 1
    ;;
esac

echo "==> 应用 $overlay"
kubectl --kubeconfig "$kubeconfig" apply -k "$repo_root/$overlay"

echo "==> 等待 fishmesh-gateway 滚动完成"
kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
  rollout status deployment/fishmesh-gateway --timeout=5m

echo "==> 当前路由模式"
kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
  get cm fishmesh-gateway-config -o jsonpath='{.data.FISHMESH_ROUTING_MODE}'
echo
echo "==> 运行中的 Gateway Pod"
kubectl --kubeconfig "$kubeconfig" -n "$namespace" \
  get pods -l app.kubernetes.io/name=fishmesh-gateway
