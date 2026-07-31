#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <job-name> <run-id> [kubeconfig]" >&2
  exit 2
fi

job_name=$1
run_id=$2
kubeconfig=${3:-/Users/frozenf1sh/.kube/fishmesh.yaml}

if [[ ! $job_name =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]; then
  echo "invalid Kubernetes Job name: $job_name" >&2
  exit 2
fi
if [[ ! $run_id =~ ^[a-zA-Z0-9._-]+$ ]]; then
  echo "invalid run id: $run_id" >&2
  exit 2
fi
if [[ ! -f $kubeconfig ]]; then
  echo "kubeconfig does not exist: $kubeconfig" >&2
  exit 2
fi

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
artifact_dir="$repository_root/artifacts/published/$run_id"
if [[ -e $artifact_dir ]]; then
  echo "artifact directory already exists: $artifact_dir" >&2
  exit 1
fi

mkdir -p "$artifact_dir"
kubectl --kubeconfig "$kubeconfig" -n kubellm get job "$job_name" -o yaml >"$artifact_dir/job.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubellm logs "job/$job_name" -c loadgen >"$artifact_dir/container.log"

# Kubernetes merges stdout and stderr. Keep the exact log and expose a
# separately validated JSONL stream to analysis code.
jq -Rrc 'fromjson? | select(type == "object")' \
  <"$artifact_dir/container.log" >"$artifact_dir/records.jsonl"

git_sha=$(git -C "$repository_root" rev-parse HEAD)
captured_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
summary=$(grep '"record_type":"summary"' "$artifact_dir/records.jsonl" | tail -n 1)

jq -n \
  --arg run_id "$run_id" \
  --arg job_name "$job_name" \
  --arg captured_at "$captured_at" \
  --arg git_sha "$git_sha" \
  --argjson summary "$summary" \
  '{
    run_id: $run_id,
    job_name: $job_name,
    captured_at: $captured_at,
    repository_git_sha_at_recovery: $git_sha,
    provenance_quality: "partial-historical-recovery",
    warning: "The Job spec and raw logs are authoritative; the historical Gateway configuration was not recoverable post-hoc.",
    summary: $summary
  }' >"$artifact_dir/manifest.json"

gzip -9 "$artifact_dir/container.log" "$artifact_dir/records.jsonl"
echo "$artifact_dir"
