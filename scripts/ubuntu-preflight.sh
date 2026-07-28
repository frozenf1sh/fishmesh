#!/usr/bin/env bash
set -euo pipefail

section() {
  printf '\n=== %s ===\n' "$1"
}

section "host"
hostnamectl || true
uname -a
id

section "storage and memory"
df -h /
free -h

section "tailscale"
if command -v tailscale >/dev/null 2>&1; then
  tailscale status || true
  tailscale ip -4 || true
else
  echo "tailscale: not installed"
fi

section "nvidia driver and GPU"
if command -v nvidia-smi >/dev/null 2>&1; then
  nvidia-smi --query-gpu=name,driver_version,memory.total,compute_cap --format=csv,noheader || true
  nvidia-smi || true
else
  echo "nvidia-smi: unavailable"
fi

section "cuda and runtimes"
command -v nvcc || true
nvcc --version 2>/dev/null || true
command -v docker || true
docker --version 2>/dev/null || true
command -v containerd || true
containerd --version 2>/dev/null || true
command -v k3s || true

section "kernel prerequisites"
lsmod | grep -E 'nvidia|overlay|br_netfilter' || true
sysctl net.ipv4.ip_forward 2>/dev/null || true
sysctl net.bridge.bridge-nf-call-iptables 2>/dev/null || true
