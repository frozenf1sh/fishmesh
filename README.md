# FishMesh

> A small, Kubernetes-native Go data plane for KV-aware routing of self-hosted LLM inference.

[简体中文](README_CN.md)

FishMesh routes OpenAI-compatible HTTP/SSE requests to self-hosted vLLM Pods. Its primary product surface is a
standalone Lite Gateway that combines EndpointSlice discovery, fresh backend load, vLLM Render/KVEvents state and
explicit degradation. A separate Standard surface integrates the same routing policy with the llm-d EPP runtime.

FishMesh is designed for small self-hosted pools. It is not a replacement for model execution, Envoy, Gateway API,
flow control or a general-purpose inference platform.

## Product surfaces

| Surface | Status | Purpose |
| --- | --- | --- |
| Lite Gateway | Primary, implemented | Direct Go data plane between an OpenAI client and vLLM Pods |
| KV-aware routing | Implemented as an explicit overlay | Reuse fresh per-Pod prefix evidence; degrade when evidence is unavailable |
| Standard llm-d integration | Adapter and local contract tests | Reuse the llm-d Gateway/EPP runtime in platform-owned deployments |
| `fishmesh-client` | Maintained | Conversation, benchmark and comparison commands with reproducible reports |

## Request path

```text
OpenAI client
  -> fishmesh-gateway
       -> bounded request body
       -> vLLM Render API -> token IDs
       -> per-Pod KV index <- vLLM KVEvents
       -> eligibility + fresh load + KV locality
       -> selected vLLM Pod
       -> HTTP/SSE response
```

The checked-in baseline defaults to `load-aware`. The `kv-aware` overlay is explicit and requires EndpointSlice,
tokenization and replay-valid KV state. `load-balanced` is the local fallback; `round-robin` and `session-key` are
kept for controlled compatibility or ablation experiments, not as the default product policy.

## What is implemented

- OpenAI-compatible HTTP/SSE proxying, cancellation propagation, stream outcome classification and TTFT metrics.
- EndpointSlice watch/list, Ready filtering, periodic relist, freshness checks and explicit Service fallback.
- Per-backend queue/running observations with validity and age, bounded admission, connection limits and EWMA circuits.
- KV-aware prefix matching from vLLM Render/KVEvents/replay, with typed policy, reason, cache source and fallback state.
- Prometheus metrics, `X-FishMesh-*` provenance headers, probes, graceful shutdown and least-privilege RBAC.
- A pinned llm-d Router v0.9.0 adapter and `fishmesh-epp` composition root with local contract tests.

The fallback chain is deliberate: an ineligible or stale backend is excluded; unavailable KV state degrades to
`load-aware`, then to local `load-balanced`. A real available signal with zero cached tokens is recorded as a miss,
not confused with unavailable state.

## Latest evidence: R6I-31 v3

The headline result below is one defined long-context profile, not a general performance claim. It compares
`load-aware-v1` and `kv-aware-v1` across two independent replicates. Each arm completed 42 requests (21 per
replicate); the workload used three concurrent growing conversations, a shared 16 KiB system prefix, approximately
18.4 KiB of conversation turns and observed prompt lengths from 6,837 to 31,479 tokens.

| Metric, pooled across the two replicates | `load-aware-v1` | `kv-aware-v1` |
| --- | ---: | ---: |
| Success | 42/42 | 42/42 |
| TTFT P50 | 1,291.07 ms | 1,150.52 ms |
| TTFT P95 | 3,973.91 ms | 1,771.68 ms |
| TTFT P99 | 4,471.19 ms | 2,031.37 ms |
| Accepted QPS | 1.303 | 1.941 |
| KV evidence available | — | 42/42 |
| Cached-prefix coverage | 0% | 100% |
| Cached-prefix P50 | 0 tokens | 15,056 tokens |
| Cached-prefix P95 | 0 tokens | 27,376 tokens |

The pooled TTFT P95 difference is **-55.42%**. A 20,000-bootstrap 95% confidence interval is
**[-67.28%, -31.52%]**. The result is specific to this long-context, low-concurrency, prefix-reuse workload.
It does not establish a universal KV-aware improvement.

- [R6I-31 experiment report](docs/experiments/2026-08-16-r6i31-conversation-ladder-32k.md)
- [Pooled source data](docs/benchmarks/r6i31-conversation-ladder-28k/data.json)
- [Reviewed run artifacts](artifacts/bench/r6i31-conversation-ladder-28k-v3/)

![R6I-31 TTFT percentiles](docs/benchmarks/r6i31-conversation-ladder-28k/ttft-percentiles.svg)

![R6I-31 Gateway capacity](docs/benchmarks/r6i31-conversation-ladder-28k/gateway-capacity.svg)

![R6I-31 KV evidence](docs/benchmarks/r6i31-conversation-ladder-28k/kv-evidence.svg)

Charts are generated from the checked-in JSON source by
[`scripts/generate-r6i31-charts.py`](scripts/generate-r6i31-charts.py). Older R6H, R6I-6 and R6I-7 results remain
available in [`docs/experiments/`](docs/experiments/) but are not mixed into the headline table above.

## Routing contract

Every decision follows the same bounded sequence:

1. Exclude terminating, stale, open-circuit or otherwise ineligible endpoints.
2. Calculate `matched_prefix_tokens` and `uncached_tokens` from real request state.
3. Combine fresh queue/running observations with uncached prefill cost.
4. Apply an overload guard and a small benefit margin to avoid cache-driven overload or route churn.
5. Select the best eligible endpoint and expose policy, reason, cache source and degradation state.
6. Fall back from KV-aware to load-aware, then to local load-balanced when required evidence is missing.

FishMesh does not claim a novel routing algorithm. The engineering focus is the bounded, observable path from real
engine state to a lightweight streaming data plane, with an llm-d integration for platform-owned gateways.

## Run the Lite surface

Read [`deploy/lite-kv-aware/README.md`](deploy/lite-kv-aware/README.md) before using a cluster. It documents the
K3s, model PVC, offline image and vLLM prerequisites. Set `KUBECONFIG` for the target cluster, then run the normal
code and manifest checks:

```bash
make ci
make image VERSION=r6c-lite-r1
kubectl apply -k deploy/lite-kv-aware
kubectl -n kubellm rollout status deployment/qwen-vllm --timeout=25m
kubectl -n kubellm rollout status deployment/fishmesh-gateway --timeout=5m
kubectl -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

The Lite demo intentionally sends two different user messages with the same long system prompt and omits
`X-FishMesh-Session-Key`. Inspect the response headers for `X-FishMesh-KV-Status`, `X-FishMesh-Policy`,
`X-FishMesh-Route-Reason` and `X-FishMesh-Cached-Prefix-Tokens`. The first request can legitimately degrade while
replay becomes fresh; a later request should show the available KV path after a valid prewarm.

For the complete request sequence, fallback cases and rollback procedure, use the
[`Lite runbook`](deploy/lite-kv-aware/README.md). The baseline overlay remains the default `load-aware` product
configuration; experiment overlays must be selected explicitly.

## Client and benchmark workflow

`fishmesh-client` is the checked-in external test client. It writes deterministic plans, per-attempt records and
scenario/batch summaries; it does not change routing mode, clear caches, roll Pods or start parallel GPU workloads.

```bash
go run ./cmd/fishmesh-client chat --system 'Answer concisely.'

go run ./cmd/fishmesh-client bench \
  --plan configs/final-pressure.json \
  --output-dir artifacts/bench/final-pressure

go run ./cmd/fishmesh-client compare \
  --baseline artifacts/bench/ab-load-balanced-r1/requests.jsonl \
  --treatment artifacts/bench/ab-kv-aware-r1/requests.jsonl \
  --output-dir artifacts/bench/comparison-r1
```

Each reviewable run should retain its Git revision, image/model details, cluster/GPU profile, policy configuration,
execution order, every attempt including failures and retries, and the command that produced the report. New raw
artifacts are ignored by default; reviewed evidence and conclusions are checked in deliberately. Secrets, prompts,
raw SSE payloads and arbitrary upstream headers must not be written to history or benchmark JSONL.

For the R6I-31 profile, use the checked-in plan and the matching deployment overlays:

```bash
kubectl apply -k deploy/experiments/r6i31-conversation-ladder/kv-aware
go run ./cmd/fishmesh-client bench \
  --plan configs/r6i31-conversation-ladder-28k.json \
  --run-id r6i31-conversation-ladder-28k \
  --output-dir artifacts/bench/r6i31-conversation-ladder-28k
```

Run the `load-aware` arm with the same plan and a fresh rollout. Do not combine runs with different Pod generations,
model arguments, cache state or admission settings. The published R6I-31 report is the reference for the required
generation and replay-validity gates.

## Scope and limitations

The reference environment is K3s `v1.36.3+k3s1`, vLLM `0.23.0` and two vLLM processes sharing one time-sliced
RTX 4060 Laptop GPU. It is suitable for engine compatibility, routing behavior, failure recovery and relative
overhead. It is not evidence for independent GPU failure domains, production scale or horizontal-scaling claims.

The current default estimator is `token-cost`; static TTFT and learned prediction remain research overlays until
their promotion gates pass. The default product policy remains `load-aware`, including when KV state is unavailable
or stale.

## Development and further reading

```bash
go test -race ./...
go vet ./...
go build ./...
make manifest
git diff --check
```

Read the [project charter](docs/design/project-charter.md), [architecture](docs/design/architecture.md),
[code organization rules](docs/design/code-organization.md), [Lite KV-aware ADR](docs/design/decisions/002-lite-kv-aware-routing.md),
[experiment index](docs/experiments/plan.md), [stage index](docs/stages/README.md) and
[current status](docs/notes/project-status.md) for implementation detail and historical decisions.
