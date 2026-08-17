#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${FISHMESH_KUBECONFIG:-}"
namespace="${FISHMESH_NAMESPACE:-kubellm}"
image_tag="${FISHMESH_GATEWAY_IMAGE_TAG:?set FISHMESH_GATEWAY_IMAGE_TAG to the preloaded Gateway image tag}"
artifact_root="${FISHMESH_ARTIFACT_ROOT:-${repo_root}/artifacts/bench/r6i31-conversation-ladder-28k}"
plan_path="${FISHMESH_PLAN_PATH:-${repo_root}/configs/r6i31-conversation-ladder-28k.json}"
local_port="${FISHMESH_BENCH_PORT:-28131}"
client_tmp_dir=""
port_forward_pid=""
experiment_started=0
restore_attempted=0

kctl() {
  if [[ -n "${kubeconfig}" ]]; then
    kubectl --kubeconfig "${kubeconfig}" "$@"
    return
  fi
  kubectl "$@"
}

stop_port_forward() {
  if [[ -n "${port_forward_pid}" ]]; then
    kill "${port_forward_pid}" 2>/dev/null || true
    wait "${port_forward_pid}" 2>/dev/null || true
    port_forward_pid=""
  fi
}

render_overlay() {
  local overlay="$1" rollout="$2" gateway_rollout="$3"
  kctl kustomize "${repo_root}/${overlay}" |
    sed "s/R6I31_GATEWAY_IMAGE_PLACEHOLDER/${image_tag}/g; s/r6i31-rollout-PLACEHOLDER/${rollout}/g; s/r6i31-gateway-rollout-PLACEHOLDER/${gateway_rollout}/g"
}

restore_baseline() {
  if [[ "${restore_attempted}" == "1" || "${experiment_started}" == "0" ]]; then
    return
  fi
  restore_attempted=1
  stop_port_forward
  echo "==> restore load-aware / 4096 baseline using tested current Gateway image"
  kctl kustomize "${repo_root}/deploy/experiments/r6i22-final/load-aware" |
    sed "s/fishmesh-gateway:r6i24-admission-kv-bypass-r1/fishmesh-gateway:${image_tag}/g; s/r6i24-admission-kv-bypass-r1/${image_tag}/g" |
    kctl apply -f -
  kctl -n "${namespace}" rollout status deployment/qwen-vllm --timeout=20m
  kctl -n "${namespace}" rollout status deployment/fishmesh-gateway --timeout=5m
  kctl -n "${namespace}" get deploy,pods -l app.kubernetes.io/name -o wide >"${artifact_root}/restore-pods.txt"
  kctl -n "${namespace}" get configmap fishmesh-gateway-config -o yaml >"${artifact_root}/restore-configmap.yaml"
}

cleanup() {
  local status=$?
  restore_baseline || status=$?
  stop_port_forward
  if [[ -n "${client_tmp_dir}" && -d "${client_tmp_dir}" ]]; then
    rm -rf "${client_tmp_dir}"
  fi
  exit "${status}"
}
trap cleanup EXIT INT TERM

assert_preflight() {
  [[ ! -e "${artifact_root}" ]] || { echo "refusing to overwrite ${artifact_root}" >&2; exit 1; }
  kctl -n "${namespace}" get deploy qwen-vllm fishmesh-gateway >/dev/null
  local strategy
  strategy="$(kctl -n "${namespace}" get deployment/qwen-vllm -o json | jq -r '[.spec.strategy.type, (.spec.strategy.rollingUpdate.maxSurge|tostring), (.spec.strategy.rollingUpdate.maxUnavailable|tostring)] | @tsv')"
  [[ "${strategy}" == $'RollingUpdate\t0\t1' ]] || { echo "unsafe qwen rollout strategy: ${strategy}" >&2; exit 1; }
  ssh ubuntu "sudo /opt/kubellm/bin/k3s ctr -n k8s.io images list --quiet" | grep -Eq "fishmesh-gateway:${image_tag}($|@)" || {
    echo "Gateway image fishmesh-gateway:${image_tag} is not preloaded on the GPU node" >&2
    exit 1
  }
}

wait_deployment_ready() {
  local deployment="$1" expected_generation="$2"
  for _ in $(seq 1 240); do
    local state desired observed total updated ready available
    state="$(kctl -n "${namespace}" get deployment/"${deployment}" -o json)"
    desired="$(jq -r '.spec.replicas // 1' <<<"${state}")"
    observed="$(jq -r '.status.observedGeneration // 0' <<<"${state}")"
    total="$(jq -r '.status.replicas // 0' <<<"${state}")"
    updated="$(jq -r '.status.updatedReplicas // 0' <<<"${state}")"
    ready="$(jq -r '.status.readyReplicas // 0' <<<"${state}")"
    available="$(jq -r '.status.availableReplicas // 0' <<<"${state}")"
    if [[ "${deployment}" == "qwen-vllm" && "${total}" -gt 2 ]]; then
      echo "refusing to continue: qwen-vllm observed ${total} Pods" >&2
      exit 1
    fi
    if [[ "${observed}" -ge "${expected_generation}" && "${updated}" == "${desired}" && "${ready}" == "${desired}" && "${available}" == "${desired}" ]]; then
      return
    fi
    sleep 5
  done
  kctl -n "${namespace}" describe deployment/"${deployment}" >&2 || true
  exit 1
}

qwen_generation() {
  kctl -n "${namespace}" get pods -l app.kubernetes.io/name=qwen-vllm -o json |
    jq -r '[.items[] | select(.status.phase == "Running" and .metadata.deletionTimestamp == null) | .metadata.ownerReferences[]? | select(.kind == "ReplicaSet") | .name] | unique | sort | join(",")'
}

start_port_forward() {
  local log_path="$1"
  stop_port_forward
  kctl -n "${namespace}" port-forward service/fishmesh-gateway "${local_port}:8080" >"${log_path}" 2>&1 &
  port_forward_pid=$!
  for _ in $(seq 1 60); do
    if curl --silent --fail "http://127.0.0.1:${local_port}/readyz" >/dev/null; then
      return
    fi
    sleep 1
  done
  cat "${log_path}" >&2 || true
  exit 1
}

wait_kv_replay() {
  local endpoint="$1"
  for _ in $(seq 1 120); do
    local metrics valid
    metrics="$(curl --silent --fail "${endpoint}/metrics" || true)"
    valid="$(awk '/^fishmesh_gateway_kv_cache_instance_valid\{/{if ($NF == 1) count++} END {print count + 0}' <<<"${metrics}")"
    if [[ "${valid}" -ge 2 ]]; then
      return
    fi
    sleep 2
  done
  echo "KV replay did not become valid for both backends" >&2
  exit 1
}

run_arm() {
  local run_id="$1" overlay="$2" seed="$3" treatment="$4"
  local output_dir="${artifact_root}/runs/${run_id}"
  mkdir -p "${output_dir}"
  render_overlay "${overlay}" "${run_id}" "${run_id}-gateway" >"${output_dir}/rendered.yaml"
  kctl apply -f "${output_dir}/rendered.yaml" | tee "${output_dir}/apply.txt"
  local qwen_deployment gateway_deployment qwen_target_generation gateway_target_generation qwen_rollout gateway_rollout
  qwen_deployment="$(kctl -n "${namespace}" get deployment/qwen-vllm -o json)"
  gateway_deployment="$(kctl -n "${namespace}" get deployment/fishmesh-gateway -o json)"
  qwen_target_generation="$(jq -r '.metadata.generation' <<<"${qwen_deployment}")"
  gateway_target_generation="$(jq -r '.metadata.generation' <<<"${gateway_deployment}")"
  qwen_rollout="$(jq -r '.spec.template.metadata.annotations["fishmesh.io/r6i31-rollout"] // empty' <<<"${qwen_deployment}")"
  gateway_rollout="$(jq -r '.spec.template.metadata.annotations["fishmesh.io/r6i31-gateway-rollout"] // empty' <<<"${gateway_deployment}")"
  [[ "${qwen_rollout}" == "${run_id}" && "${gateway_rollout}" == "${run_id}-gateway" ]] || {
    echo "rendered rollout annotations do not match arm ${run_id}" >&2
    exit 1
  }
  wait_deployment_ready qwen-vllm "${qwen_target_generation}"
  wait_deployment_ready fishmesh-gateway "${gateway_target_generation}"
  kctl -n "${namespace}" rollout status deployment/fishmesh-gateway --timeout=5m | tee "${output_dir}/rollout-gateway.txt"
  kctl -n "${namespace}" get deploy,pods -l app.kubernetes.io/name -o wide >"${output_dir}/pods.txt"
  kctl -n "${namespace}" get configmap fishmesh-gateway-config -o yaml >"${output_dir}/configmap.yaml"
  local generation endpoint
  generation="$(qwen_generation)"
  [[ -n "${generation}" ]] || { echo "missing qwen generation" >&2; exit 1; }
  printf '%s\n' "${generation}" >"${output_dir}/qwen-generation.txt"
  start_port_forward "${output_dir}/port-forward.log"
  endpoint="http://127.0.0.1:${local_port}"
  curl --silent --fail "${endpoint}/metrics" >"${output_dir}/gateway-metrics-before.txt"
  if [[ "${treatment}" == "kv-aware" ]]; then
    wait_kv_replay "${endpoint}"
  fi
  "${client_tmp_dir}/fishmesh-client" bench \
    --endpoint "${endpoint}" \
    --metrics-endpoint "${endpoint}/metrics" \
    --plan "${plan_path}" \
    --run-id "${run_id}" \
    --treatment "${treatment}" \
    --run-nonce "${run_id}-nonce" \
    --cache-generation "${generation}" \
    --workload-seed "${seed}" \
    --output-dir "${output_dir}/bench" \
    --metrics-interval 250ms |& tee "${output_dir}/bench-client.log"
  curl --silent --fail "${endpoint}/metrics" >"${output_dir}/gateway-metrics-after.txt"
  stop_port_forward
  kctl -n "${namespace}" get pods -l app.kubernetes.io/name=qwen-vllm -o name | while read -r pod; do
    kctl -n "${namespace}" logs "${pod}" --tail=300 >"${output_dir}/${pod#pod/}.log"
  done
  [[ "$(jq -r '.requested == 21 and .succeeded == 21 and .failed == 0' "${output_dir}/bench/report.json")" == "true" ]] || {
    echo "arm ${run_id} did not complete 21/21" >&2
    exit 1
  }
  if [[ "${treatment}" == "kv-aware" ]]; then
    [[ "$(jq -r '[.scenarios[].kv_statuses.available // 0] | add == 21' "${output_dir}/bench/report.json")" == "true" ]] || {
      echo "KV arm ${run_id} lacks 21 available samples" >&2
      exit 1
    }
  fi
}

assert_preflight
mkdir -p "${artifact_root}/runs"
kctl -n "${namespace}" get deploy,pods -l app.kubernetes.io/name -o wide >"${artifact_root}/preflight-pods.txt"
kctl -n "${namespace}" get configmap fishmesh-gateway-config -o yaml >"${artifact_root}/preflight-configmap.yaml"
git rev-parse HEAD >"${artifact_root}/git-sha.txt"
git status --short >"${artifact_root}/git-status.txt"
client_tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/fishmesh-client-r6i31.XXXXXX")"
go build -o "${client_tmp_dir}/fishmesh-client" "${repo_root}/cmd/fishmesh-client"
experiment_started=1

run_arm r1-la deploy/experiments/r6i31-conversation-ladder/load-aware 20260831 load-aware
run_arm r1-kv deploy/experiments/r6i31-conversation-ladder/kv-aware 20260831 kv-aware
run_arm r2-kv deploy/experiments/r6i31-conversation-ladder/kv-aware 20260901 kv-aware
run_arm r2-la deploy/experiments/r6i31-conversation-ladder/load-aware 20260901 load-aware

"${client_tmp_dir}/fishmesh-client" compare \
  --baseline "${artifact_root}/runs/r1-la/bench/requests.jsonl" \
  --baseline "${artifact_root}/runs/r2-la/bench/requests.jsonl" \
  --treatment "${artifact_root}/runs/r1-kv/bench/requests.jsonl" \
  --treatment "${artifact_root}/runs/r2-kv/bench/requests.jsonl" \
  --baseline-report "${artifact_root}/runs/r1-la/bench/report.json" \
  --baseline-report "${artifact_root}/runs/r2-la/bench/report.json" \
  --treatment-report "${artifact_root}/runs/r1-kv/bench/report.json" \
  --treatment-report "${artifact_root}/runs/r2-kv/bench/report.json" \
  --bootstrap 20000 \
  --seed 20260831 \
  --output-dir "${artifact_root}/compare" |& tee "${artifact_root}/compare.log"
touch "${artifact_root}/ALL_ARMS_VALID"
echo "formal ladder experiment complete: ${artifact_root}"
