#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-$HOME/.kube/fishmesh.yaml}"
remote="${FISHMESH_GPU_SSH_HOST:-ubuntu}"
image="docker.io/vllm/vllm-openai:v0.11.0"
image_ready=false

for attempt in $(seq 1 60); do
  state="$(ssh "$remote" 'sudo systemctl is-active fishmesh-vllm-image-preload || true')"
  if [[ "$state" == "inactive" ]]; then
    if ssh "$remote" \
      "sudo /opt/kubellm/bin/k3s crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock images" \
      | grep -Fq "$image"; then
      image_ready=true
      break
    fi
    echo "vLLM image preload failed; inspect: sudo journalctl -u fishmesh-vllm-image-preload"
    exit 1
  fi
  echo "waiting for vLLM image preload ($attempt/60)"
  sleep 30
done

if [[ "$image_ready" != true ]]; then
  echo "timed out waiting for the vLLM image preload"
  exit 1
fi

kubectl --kubeconfig "$kubeconfig" apply -f "$repo_root/deploy/inference/vllm-qwen.yaml"
kubectl --kubeconfig "$kubeconfig" rollout status deployment/qwen-vllm \
  --namespace kubellm --timeout=20m
kubectl --kubeconfig "$kubeconfig" delete job vllm-api-smoke-test \
  --namespace kubellm --ignore-not-found
kubectl --kubeconfig "$kubeconfig" apply -f "$repo_root/deploy/validation/vllm-api-smoke-test.yaml"
kubectl --kubeconfig "$kubeconfig" wait --for=condition=complete job/vllm-api-smoke-test \
  --namespace kubellm --timeout=12m
kubectl --kubeconfig "$kubeconfig" logs job/vllm-api-smoke-test --namespace kubellm
