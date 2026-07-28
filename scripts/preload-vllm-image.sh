#!/usr/bin/env bash
set -euo pipefail

image="${1:-docker.io/vllm/vllm-openai:v0.11.0}"
remote="${FISHMESH_GPU_SSH_HOST:-ubuntu}"

docker image inspect "$image" >/dev/null
docker save "$image" | ssh "$remote" \
  'sudo /opt/kubellm/bin/k3s ctr --address /run/k3s/containerd/containerd.sock --namespace k8s.io images import -'

ssh "$remote" \
  'sudo /opt/kubellm/bin/k3s crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock images' \
  | grep -F "$image"
