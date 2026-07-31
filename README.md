# FishMesh

> An experiment-driven, explainable request scheduler for Kubernetes-hosted LLM inference.

FishMesh is not another production AI Gateway and does not try to replace vLLM,
Envoy, llm-d, NVIDIA Dynamo, or Kubernetes Gateway API Inference Extension. It is
a compact systems project for answering a narrower question with reproducible
evidence:

> When does connection reuse suffice, when is request affinity beneficial, and
> when must load and failure signals override locality?

The project deliberately separates a deterministic request path from a slow,
read-only evidence path. The current Go gateway is an experimental carrier for
the scheduler core. The intended production integration boundary is a future
Gateway API Inference Extension Endpoint Picker (EPP) adapter, so mature open
source software can continue to own ingress, authentication, rate limiting and
general proxy behavior.

## What FishMesh is—and is not

FishMesh currently provides:

- an OpenAI-compatible streaming proxy with correct SSE draining and TTFT
  measurement;
- Kubernetes EndpointSlice discovery with Ready filtering, bounded relist,
  freshness and Service fallback;
- per-backend vLLM observation snapshots with explicit unavailable/degraded
  states;
- deterministic Service, routing-key affinity and least-inflight experiment
  policies;
- a reproducible load generator whose JSONL output records run provenance;
- a read-only evidence-based diagnoser for validating signal contracts.

FishMesh does **not** currently claim:

- exact KV-cache awareness—the affinity experiment consumes a cooperative
  routing key, not vLLM token-block cache events;
- per-backend GPU utilization—the two replicas share one time-sliced RTX 4060,
  so device metrics cannot be attributed reliably to one Pod;
- production high availability—the current cluster has one physical GPU and one
  Gateway replica;
- an autonomous AI agent—the diagnoser is deterministic rules, and an LLM
  narrator remains optional;
- a statistically conclusive performance result—the 2026-08-08 runs are
  exploratory evidence and are being replaced by a repeated, randomized plan.

## Current architecture

```text
OpenAI client
  -> FishMesh experimental Gateway
       -> request/session routing key
       -> eligibility filter
       -> bounded affinity + load spillover (next scheduler milestone)
       -> vLLM backend

EndpointSlice + vLLM metrics + local outcomes
  -> immutable backend snapshot
  -> request scheduler

Prometheus windows + Kubernetes events + node GPU health (planned)
  -> evidence-based diagnoser
  -> read-only recommendation
```

Fast-path signals and slow-path evidence are intentionally different. Queue,
running requests, local in-flight requests and recent transport errors may
participate in routing. Cumulative TTFT histograms, prefix-cache hit-rate trends,
node GPU telemetry and network telemetry are evaluation or safety signals; they
are not mixed into an arbitrary weighted score.

## Verified environment

The local research cluster uses:

| Layer | Pinned baseline |
| --- | --- |
| Kubernetes | K3s `v1.36.3+k3s1`, two nodes over Tailscale |
| Inference runtime | vLLM `0.23.0` OpenAI server |
| Model fixture | Qwen2.5-0.5B-Instruct, offline local PV |
| GPU runtime | NVIDIA driver `610.43.02`, device plugin `v0.19.2` |
| Application | Go `1.26`, standard-library HTTP/SSE, Prometheus client |
| Packaging | Kustomize, multi-stage Docker, distroless runtime |

The inference node has one RTX 4060 Laptop GPU. Kubernetes advertises two GPU
shares through NVIDIA time-slicing so that two vLLM processes can be used for
routing experiments. These shares are not independent GPUs and provide neither
VRAM nor failure isolation. Results from this profile validate routing behavior,
not production multi-GPU scalability.

vLLM `0.11.0` remains locally available only as the historical
`2026-08-08` experiment runtime. New manifests target `0.23.0`; reports must
always state which version produced their artifacts.

## Repository guide

- [`docs/design/plan.md`](docs/design/plan.md): current architecture decisions,
  MVP boundary and roadmap;
- [`docs/experiments/plan.md`](docs/experiments/plan.md): experiment matrix,
  provenance contract and statistical acceptance rules;
- [`docs/experiments/2026-08-08-llm-scheduling.md`](docs/experiments/2026-08-08-llm-scheduling.md):
  exploratory historical results and their limitations;
- [`docs/stages/`](docs/stages/): implementation-stage records;
- [`artifacts/`](artifacts/): retained raw evidence, including failed runs and
  reruns;
- [`deploy/`](deploy/): Kustomize baselines, isolated experiments and validation
  workloads.

## Local verification

```bash
make ci
```

`make ci` runs race-enabled tests, `go vet`, builds all binaries, and renders
the supported Kubernetes overlays.

To inspect the cluster without changing it:

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get deploy,pod,svc,endpointslice
```

To publish a completed Job that still exists in the cluster:

```bash
scripts/archive-live-experiment.sh \
  fishmesh-exp-hot-prefix-hash \
  2026-08-08-hot-prefix-hash-attempt-1
```

Historical recovery is explicitly marked as partial provenance. New benchmark
runs emit a `run_metadata` JSONL record and must capture image digests, Git SHA,
runtime arguments, cluster profile and treatment order before execution.

## Project success criteria

FishMesh is complete as a portfolio MVP when it can reproducibly demonstrate:

1. the transport-only effect of keep-alive;
2. the workload conditions under which affinity helps;
3. the overload point where bounded affinity spills traffic safely;
4. recovery behavior under Pod deletion, stale telemetry and Kubernetes API
   interruption;
5. a same-environment comparison with a mainstream open-source inference
   router;
6. a clean integration path to the Gateway API Inference Extension EPP model.

eBPF request routing, per-backend GPU scoring, autonomous actuation, CRDs and
prefill/decode disaggregation are intentionally outside the MVP until an
experiment proves they solve a measured problem.
