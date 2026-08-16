#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${FISHMESH_KUBECONFIG:-${KUBECONFIG:-${HOME}/.kube/fishmesh.yaml}}"
namespace="${FISHMESH_NAMESPACE:-kubellm}"
artifact_root="${FISHMESH_ARTIFACT_ROOT:-${repo_root}/artifacts/bench/r6i27-long-context-rolling-v2}"
local_port="${FISHMESH_BENCH_PORT:-18080}"
plan_path="${FISHMESH_PLAN_PATH:-${repo_root}/configs/r6i27-long-context-rolling.json}"
overlay_root="${FISHMESH_OVERLAY_ROOT:-deploy/experiments/r6i27-long-context-rolling}"
lb_overlay_dir="${FISHMESH_LB_OVERLAY_DIR:-load-balanced-active}"
kv_overlay_dir="${FISHMESH_KV_OVERLAY_DIR:-kv-aware-active}"
run_prefix="${FISHMESH_RUN_PREFIX:-r6i27}"
rollout_placeholder="${FISHMESH_ROLLOUT_PLACEHOLDER:-r6i27-rollout-PLACEHOLDER}"
gateway_rollout_placeholder="${FISHMESH_GATEWAY_ROLLOUT_PLACEHOLDER:-r6i27-gateway-rollout-PLACEHOLDER}"
client_tmp_dir=""
port_forward_pid=""
experiment_started=0
restore_attempted=0

r1_lb="${run_prefix}-r1-lb"
r1_kv="${run_prefix}-r1-kv"
r2_kv="${run_prefix}-r2-kv"
r2_lb="${run_prefix}-r2-lb"

declare -a baseline_runs=(
  "${artifact_root}/${r1_lb}/requests.jsonl"
  "${artifact_root}/${r2_lb}/requests.jsonl"
)
declare -a treatment_runs=(
  "${artifact_root}/${r1_kv}/requests.jsonl"
  "${artifact_root}/${r2_kv}/requests.jsonl"
)
declare -a baseline_reports=(
  "${artifact_root}/${r1_lb}/report.json"
  "${artifact_root}/${r2_lb}/report.json"
)
declare -a treatment_reports=(
  "${artifact_root}/${r1_kv}/report.json"
  "${artifact_root}/${r2_kv}/report.json"
)

kctl() {
  kubectl --kubeconfig "${kubeconfig}" "$@"
}

stop_port_forward() {
  if [[ -n "${port_forward_pid}" ]]; then
    kill "${port_forward_pid}" 2>/dev/null || true
    wait "${port_forward_pid}" 2>/dev/null || true
    port_forward_pid=""
  fi
}

restore_baseline() {
  if [[ "${restore_attempted}" == "1" || "${experiment_started}" == "0" ]]; then
    return
  fi
  restore_attempted=1
  stop_port_forward
  echo "==> restoring declarative load-balanced baseline"
  if ! kctl apply -k "${repo_root}/deploy/experiments/r6i22-final/load-balanced"; then
    echo "baseline apply failed; inspect the cluster before changing workloads" >&2
    return
  fi
  kctl -n "${namespace}" rollout status deployment/qwen-vllm --timeout=20m
  kctl -n "${namespace}" rollout status deployment/fishmesh-gateway --timeout=5m
  kctl -n "${namespace}" get deploy,pods -l app.kubernetes.io/name -o wide
}

cleanup() {
  local status=$?
  restore_baseline || status=$?
  if [[ -n "${client_tmp_dir}" ]]; then
    rm -rf "${client_tmp_dir}"
  fi
  exit "${status}"
}
trap cleanup EXIT INT TERM

assert_safe_rollout_strategy() {
  local strategy
  strategy="$(kctl -n "${namespace}" get deployment/qwen-vllm -o json | jq -r '[.spec.strategy.type, (.spec.strategy.rollingUpdate.maxSurge|tostring), (.spec.strategy.rollingUpdate.maxUnavailable|tostring)] | @tsv')"
  if [[ "${strategy}" != $'RollingUpdate\t0\t1' ]]; then
    echo "refusing experiment: qwen-vllm strategy is ${strategy}, expected RollingUpdate/0/1" >&2
    exit 1
  fi
}

wait_qwen_rollout() {
  local attempt state total updated ready available
  for attempt in $(seq 1 240); do
    state="$(kctl -n "${namespace}" get deployment/qwen-vllm -o json)"
    total="$(jq -r '.status.replicas // 0' <<<"${state}")"
    updated="$(jq -r '.status.updatedReplicas // 0' <<<"${state}")"
    ready="$(jq -r '.status.readyReplicas // 0' <<<"${state}")"
    available="$(jq -r '.status.availableReplicas // 0' <<<"${state}")"
    if (( total > 2 )); then
      echo "refusing to continue: qwen-vllm observed ${total} Pods; GPU-safe rollout requires at most 2" >&2
      exit 1
    fi
    if [[ "${updated}" == "2" && "${ready}" == "2" && "${available}" == "2" ]]; then
      return
    fi
    sleep 5
  done
  kctl -n "${namespace}" describe deployment/qwen-vllm >&2 || true
  exit 1
}

qwen_generation() {
  kctl -n "${namespace}" get pods -l app.kubernetes.io/name=qwen-vllm -o json |
    jq -r '[.items[] | select(.status.phase == "Running" and .metadata.deletionTimestamp == null) | .metadata.ownerReferences[]? | select(.kind == "ReplicaSet") | .name] | unique | sort | join(",")'
}

start_port_forward() {
  local run_id="$1" log_path="$2"
  stop_port_forward
  kctl -n "${namespace}" port-forward service/fishmesh-gateway "${local_port}:8080" >"${log_path}" 2>&1 &
  port_forward_pid=$!
  for _ in $(seq 1 60); do
    if ! kill -0 "${port_forward_pid}" 2>/dev/null; then
      cat "${log_path}" >&2 || true
      echo "port-forward exited for ${run_id}" >&2
      exit 1
    fi
    if curl --silent --fail "http://127.0.0.1:${local_port}/readyz" >/dev/null; then
      return
    fi
    sleep 1
  done
  cat "${log_path}" >&2 || true
  echo "timed out waiting for Gateway port-forward for ${run_id}" >&2
  exit 1
}

wait_kv_replay() {
  local endpoint="$1" run_id="$2" metrics valid
  for _ in $(seq 1 120); do
    metrics="$(curl --silent --fail "${endpoint}/metrics" || true)"
    valid="$(awk '/^fishmesh_gateway_kv_cache_instance_valid\{/{if ($NF == 1) count++} END {print count + 0}' <<<"${metrics}")"
    if [[ "${valid}" -ge 2 ]]; then
      echo "KV replay ready for ${run_id}: valid_backends=${valid}"
      return
    fi
    sleep 2
  done
  printf '%s\n' "${metrics}" | grep -E 'fishmesh_gateway_kv_cache_(instance_valid|status)' >&2 || true
  echo "timed out waiting for KV replay for ${run_id}" >&2
  exit 1
}

apply_arm() {
  local overlay="$1" run_id="$2"
  echo "==> applying ${overlay} for ${run_id}; rolling qwen and Gateway"
  kctl kustomize "${repo_root}/${overlay}" |
    sed "s/${rollout_placeholder}/${run_id}/g; s/${gateway_rollout_placeholder}/${run_id}/g" |
    kctl apply -f -
  wait_qwen_rollout
  kctl -n "${namespace}" rollout status deployment/fishmesh-gateway --timeout=5m
}

run_arm() {
  local run_id="$1" mode="$2" seed="$3" treatment="$4" overlay generation endpoint
  local output_dir="${artifact_root}/${run_id}"
  if [[ -e "${output_dir}" ]]; then
    echo "refusing to overwrite existing artifact directory: ${output_dir}" >&2
    exit 1
  fi
  mkdir -p "${output_dir}"
  if [[ "${mode}" == "lb" ]]; then
    overlay="${overlay_root}/${lb_overlay_dir}"
  else
    overlay="${overlay_root}/${kv_overlay_dir}"
  fi
  apply_arm "${overlay}" "${run_id}"
  generation="$(qwen_generation)"
  if [[ -z "${generation}" ]]; then
    echo "could not identify qwen ReplicaSet generation for ${run_id}" >&2
    exit 1
  fi
  kctl -n "${namespace}" get deploy,pods -l app.kubernetes.io/name -o wide >"${output_dir}/cluster.txt"
  kctl -n "${namespace}" get deployment/qwen-vllm -o json >"${output_dir}/qwen-deployment.json"
  kctl -n "${namespace}" get deployment/fishmesh-gateway -o json >"${output_dir}/gateway-deployment.json"
  printf '%s\n' "${generation}" >"${output_dir}/qwen-generation.txt"
  endpoint="http://127.0.0.1:${local_port}"
  start_port_forward "${run_id}" "${output_dir}/port-forward.log"
  if [[ "${mode}" == "kv" ]]; then
    wait_kv_replay "${endpoint}" "${run_id}"
  fi
  echo "==> benchmark ${run_id}: seed=${seed}, generation=${generation}"
  "${client_tmp_dir}/fishmesh-client" bench \
    --endpoint "${endpoint}" \
    --metrics-endpoint "${endpoint}/metrics" \
    --plan "${plan_path}" \
    --run-id "${run_id}" \
    --treatment "${treatment}" \
    --run-nonce "${run_id}-nonce" \
    --cache-generation "${generation}" \
    --workload-seed "${seed}" \
    --output-dir "${output_dir}" \
    --metrics-interval 250ms
  stop_port_forward
  kctl -n "${namespace}" get deploy,pods -l app.kubernetes.io/name -o wide >"${output_dir}/cluster-after.txt"
}

mkdir -p "${artifact_root}"
if [[ -e "${artifact_root}/comparison.md" ]]; then
  echo "refusing to overwrite existing result: ${artifact_root}" >&2
  exit 1
fi
assert_safe_rollout_strategy
experiment_started=1
client_tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/fishmesh-client-${run_prefix}.XXXXXX")"
go build -o "${client_tmp_dir}/fishmesh-client" "${repo_root}/cmd/fishmesh-client"

# Each arm gets unique qwen and Gateway rollout annotations. Replicate 2
# reverses treatment order to reduce time and GPU-state drift.
run_arm "${r1_lb}" lb 20260817 pure-load-balance
run_arm "${r1_kv}" kv 20260817 kv-aware-rolling
run_arm "${r2_kv}" kv 20260818 kv-aware-rolling
run_arm "${r2_lb}" lb 20260818 pure-load-balance

baseline_args=()
treatment_args=()
baseline_report_args=()
treatment_report_args=()
for path in "${baseline_runs[@]}"; do baseline_args+=(--baseline "${path}"); done
for path in "${treatment_runs[@]}"; do treatment_args+=(--treatment "${path}"); done
for path in "${baseline_reports[@]}"; do baseline_report_args+=(--baseline-report "${path}"); done
for path in "${treatment_reports[@]}"; do treatment_report_args+=(--treatment-report "${path}"); done
"${client_tmp_dir}/fishmesh-client" compare \
  "${baseline_args[@]}" \
  "${treatment_args[@]}" \
  "${baseline_report_args[@]}" \
  "${treatment_report_args[@]}" \
  --bootstrap 20000 \
  --seed 20260817 \
  --output-dir "${artifact_root}"

echo "experiment complete: ${artifact_root}/comparison.md"
