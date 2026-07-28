#!/usr/bin/env bash
set -euo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image_tag=${1:-dev}
archive_path=$(mktemp -t "fishmesh-${image_tag}.XXXXXX.oci.tar")
remote_archive="/tmp/fishmesh-${image_tag}.oci.tar"
trap 'rm -f "$archive_path"' EXIT

docker buildx build \
  --platform linux/amd64 \
  --tag "fishmesh:${image_tag}" \
  --output "type=oci,dest=${archive_path}" \
  "$repository_root"

scp "$archive_path" "ubuntu:${remote_archive}"
ssh ubuntu "sudo /opt/kubellm/bin/k3s ctr -n k8s.io images import '${remote_archive}' && sudo rm -f '${remote_archive}'"
