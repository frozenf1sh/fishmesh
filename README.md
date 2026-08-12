# FishMesh

> A lightweight Kubernetes-native, KV-cache-aware router for self-hosted LLM inference.

[简体中文](README_CN.md)

FishMesh is evolving into a small-cluster alternative to a full inference-gateway stack. Its target primary
runtime is a single Go HTTP/SSE data plane that discovers vLLM Pods, tracks their real KV-cache locality, combines
locality with current load and failure state, and forwards each OpenAI-compatible request directly to the selected
Pod. The current implemented baseline is described separately below.

For platform environments that already operate an Envoy-compatible Gateway, FishMesh also provides a standard
llm-d EPP integration. The two runtimes share one protocol-neutral routing policy; FishMesh does not reimplement
Envoy `ext_proc`, InferencePool, flow control or model execution.

## The problem

A Kubernetes Service balances connections. It cannot answer request-level questions such as:

- which vLLM Pod is Ready, Serving and backed by fresh discovery state;
- which Pod still owns the longest cached prefix for this prompt;
- whether that cache benefit is worth waiting behind the Pod's queued work;
- when a stream cancellation, Pod rollout or telemetry gap must invalidate local state;
- why a request used KV-aware cache routing, degraded to load-balanced or fell back.

Session affinity alone is insufficient. Two unrelated sessions can share a long system prompt and reuse the same
KV blocks, while a restarted Pod may have lost every block associated with an otherwise stable session key.

## Product shape

### Lite mode — primary deliverable

```text
OpenAI client
  -> fishmesh-gateway Service / Deployment
       -> bounded request body
       -> vLLM Render API -> KV-aware token IDs
       -> per-Pod KV index <- vLLM KVEvents
       -> cache + load + health routing
       -> selected vLLM Pod IP
       -> HTTP/SSE passthrough
```

Lite mode does not require Envoy, Gateway API Inference Extension CRDs, EPP, Redis or a FishMesh Operator. It is
intended for small self-hosted pools where a generic Service is too limited and a full shared gateway platform is
unnecessary.

### Standard mode — ecosystem integration

```text
OpenAI client
  -> Envoy-compatible Gateway
  -> llm-d EPP runtime
       -> llm-d token and precise-prefix producers
       -> FishMesh routing policy
       -> llm-d picker / flow control / response lifecycle
  -> vLLM Pod
```

Standard mode targets shared gateways, multiple pools and platform-owned ingress. FishMesh reuses the standard
runtime instead of maintaining another EPP implementation.

## Current implementation status

The deployed baseline already provides:

- OpenAI-compatible HTTP/SSE proxying, cancellation propagation, stream outcome classification and TTFT metrics;
- EndpointSlice watch/list, Ready filtering, periodic relist, freshness and explicit Service fallback;
- per-backend vLLM queue/running observations with per-field validity and age;
- three routing modes: `load-balanced` for ordinary in-flight balancing, `session-key` for a client-supplied
  session key with bounded stickiness, and `kv-aware` for real KV locality plus known load (their policy
  identifiers are `load-balanced-v1`, `session-key-v1`, and `kv-aware-v1`);
- non-blocking admission, per-backend connection bounds, transport-error EWMA circuits and state garbage collection;
- Prometheus routing/discovery/backend metrics and `X-FishMesh-*` request provenance;
- strict configuration, probes, graceful shutdown, least-privilege RBAC and race-tested request lifecycle;
- a pinned llm-d Router v0.9.0 adapter and `fishmesh-epp` composition root with local contract tests.

The R6A–R6B real-signal and request-path work is complete: vLLM Render/KVEvents/replay produced a per-Pod
cross-session match for a shared system prompt, and the Gateway consumes that state for `kv-aware-v1`.
Real eviction, subscriber recovery and Pod replacement remove stale locality; unknown or stale state explicitly
degrades to load-balanced routing rather than masquerading as a zero-token match.

`X-FishMesh-Session-Key` remains an optional compatibility session hint; the KV-aware demo below deliberately omits it
to demonstrate cache reuse across distinct user messages.

## Routing contract

The target policy remains deliberately explainable:

1. exclude terminating, stale, open-circuit or otherwise ineligible endpoints;
2. compute each Pod's real `matched_prefix_tokens` and `uncached_tokens`;
3. estimate queued work and uncached prefill cost from fresh load samples;
4. apply a hard overload guard so cache locality cannot dominate severe pressure;
5. use a small benefit margin/hysteresis to avoid routing churn;
6. degrade from kv-aware to load-balanced when KV state is unavailable or stale;
7. use an optional session hint only as a tie-breaker or short-term stability signal;
8. expose a typed policy, reason, cache source and degradation state for every decision.

FishMesh does not claim a novel routing algorithm. The engineering contribution is a bounded, observable and
operable path from real engine state to a lightweight streaming data plane, plus a standard llm-d integration.

## Five-minute Lite demo

This demo assumes the Lite prerequisites in [`deploy/lite-kv-aware/README.md`](deploy/lite-kv-aware/README.md): a K3s
cluster, the model PV and an importable Gateway image. It first proves the ordinary `load-balanced` default, then
temporarily enables the explicit KV-aware overlay. The optional `session-key` mode is shown by the session-key
experiment overlay. Standard mode / llm-d integration is intentionally deferred.

Verify the repository and the load-balanced baseline:

```bash
make ci

kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get deployment
```

Install the KV-aware overlay and wait for its real KV signal path. The checked-in overlay uses `r6d2-r1`.

```bash
make image VERSION=r6d2-r1
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/lite-kv-aware
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deploy/qwen-vllm --timeout=10m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status deploy/fishmesh-gateway --timeout=5m
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

In a second terminal, make two streaming requests with the same long system prompt and different user messages.
Do **not** send `X-FishMesh-Session-Key`; save response headers separately.

```bash
SYSTEM_PROMPT="$(printf 'FishMesh demo policy: answer concisely, state assumptions, preserve streaming semantics, and never reveal hidden reasoning. %.0s' {1..32})"

curl -sS -D /tmp/fishmesh-first.headers -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [
      {"role": "system", "content": "'"${SYSTEM_PROMPT}"'"},
      {"role": "user", "content": "first request"}],
    "stream": true,
    "max_tokens": 64
  }'

curl -sS -D /tmp/fishmesh-second.headers -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [
      {"role": "system", "content": "'"${SYSTEM_PROMPT}"'"},
      {"role": "user", "content": "second request"}],
    "stream": true,
    "max_tokens": 64
  }'

grep -iE 'x-fishmesh-(kv-status|policy|route-reason|cached-prefix-tokens)' \
  /tmp/fishmesh-first.headers /tmp/fishmesh-second.headers
```

The first request may correctly show `match-unavailable` with `kv-aware-load-fallback-v1` while replay becomes fresh.
After a valid prewarm, the second should show `available`, `kv-aware-v1`, `kv-aware`, and a
non-zero `X-FishMesh-Cached-Prefix-Tokens`. `available` with zero cached tokens is a real zero match; it is not the
same state as unavailable. Restore the default when finished:
`kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/baseline/base`.

The reference cluster runs the `deploy/monitoring` Prometheus + Grafana stack. It has a live Gateway scrape target,
loaded rules and a provisioned `FishMesh Gateway` dashboard. It intentionally has no Alertmanager/notification
receiver, so rules are evaluated and visible but are not delivered externally. See
[`deploy/monitoring/README.md`](deploy/monitoring/README.md) and [`docs/notes/runbook.md`](docs/notes/runbook.md).

## Local client and final pressure test

`fishmesh-client` is the only checked-in external test client; it is not part of the Gateway image.
It streams text while printing the fixed `X-FishMesh-*` decision-header allowlist, TTFT and total duration to stderr.
Keep the port-forward above running, then start a normal multi-turn conversation. If `--history` is omitted, the
client creates a private timestamped file under `~/.local/state/fishmesh/`; pass `--history` when you want to resume
the same conversation later:

```bash
go run ./cmd/fishmesh-client chat \
  --system 'Answer concisely.'
```

```bash
go run ./cmd/fishmesh-client chat \
  --history ~/.local/state/fishmesh/chat.json
```

For the final pressure test, `bench` reads a deterministic matrix of prompt lengths, quantities, batches, same-prefix,
different-prefix and mixed-prefix scenarios. It writes `plan.json`, every request attempt as `requests.jsonl`, and
automatic `report.json`/`report.md` summaries grouped by scenario and batch. Prompt text, raw SSE and API keys are never
written to these artifacts.

```bash
go run ./cmd/fishmesh-client bench \
  --plan configs/final-pressure.json \
  --output-dir artifacts/bench/final-pressure
```

Omitting `--plan` uses the built-in final matrix. Batches are sequential, requests inside a batch are bounded by the
declared concurrency, and batch pauses give KVEvents/replay time to settle. The client never changes routing mode,
clears cache, rolls Pods or starts another workload.

The client reads an optional `FISHMESH_API_KEY` only for the outgoing authorization header. It never writes that key,
prompts, raw SSE payloads or arbitrary upstream headers into history or benchmark JSONL. It does not switch routing
modes, clear cache, roll Pods or start parallel GPU workloads. `chat` and `request` color the important diagnostic
values on a terminal by default; use `--color never` for plain output or `--color always` when intentionally piping
ANSI output. Benchmark JSONL is always plain.

## Delivery priorities

| Priority | Scope | Decision |
| --- | --- | --- |
| Primary | Gateway, request path, routing, discovery, observation, circuit, admission and transport | Productize |
| New core | tokenization, KVEvents/index and kv-aware policy | Complete; Lite KV-aware overlay is available |
| Standard | llm-d adapter, `fishmesh-epp`, Gateway/InferencePool deployment | Keep; complete after Lite MVP |
| Final test tool | `fishmesh-client bench` | Maintain the matrix and report contract |
| Historical | simulator, loadgen, analyst and one-time probes | Removed from the default tree; conclusions remain in stage notes/Git history |
| Excluded | eBPF, Agent actuator, FishMesh CRD/Operator, P/D, shared DB and generic AI Gateway | Outside this MVP |

Historical implementation code is kept recoverable through Git history, while the default tree contains only the
Gateway product, the final client and the active Lite deployment surface.

## Roadmap

1. **R6A–R6C (complete):** real Render/KVEvents/replay, bounded capability domains, KV-aware request path,
   gateway-only Lite image and real rollout/failure acceptance. Monitoring assets are present but not live-validated.
2. **R6D (complete):** bounded Service/load-balanced/KV-aware profiles, including controlled c=1 prefix segments; results
   are correctness/profile evidence only.
3. **Lite release closeout (in progress):** user demo, release artifacts and upgrade/rollback guidance.
4. **R6E — Standard delivery (deferred):** Gateway/HTTPRoute/InferencePool/EPP deployment and wire-level contracts.

Experiments only decide an implementation or verify acceptance; they are not independent product tracks.

## Engineering rules

Go changes follow the mandatory [code organization rules](docs/design/code-organization.md). New capabilities are
implemented in this order: contract and values, contract tests, atomic implementation, orchestration, external
adapter, real-cluster validation and stage documentation. Main orchestration functions keep 3–7 same-level steps;
protocol parsing, block indexing and fallback decisions cannot be mixed into one large function.

Production code uses clear Chinese comments to explain invariants, ownership, cancellation, freshness and
degradation. Comments must explain why the behavior exists, not translate the Go syntax.

See the [project charter](docs/design/project-charter.md), [architecture](docs/design/architecture.md),
[implementation plan](docs/design/plan.md), [ADR-002](docs/design/decisions/002-lite-kv-aware-routing.md) and
[current status](docs/notes/project-status.md).

## Verified environment and limitations

The current cluster is K3s `v1.36.3+k3s1` with vLLM `0.23.0` and two vLLM processes sharing one time-sliced
RTX 4060 Laptop GPU. It is suitable for engine compatibility, routing behavior, failure recovery and relative
overhead. It does not represent two independent GPU failure domains and cannot support production-scale or
horizontal-scaling claims.

Raw benchmark output, logs and cluster snapshots remain outside Git. Repository history contains source,
declarative configuration, schemas and reviewed conclusions only.
