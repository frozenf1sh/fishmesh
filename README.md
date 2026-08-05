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
- why a request used exact cache routing, degraded to load-only or fell back.

Session affinity alone is insufficient. Two unrelated sessions can share a long system prompt and reuse the same
KV blocks, while a restarted Pod may have lost every block associated with an otherwise stable session key.

## Product shape

### Lite mode — primary deliverable

```text
OpenAI client
  -> fishmesh-gateway Service / Deployment
       -> bounded request body
       -> vLLM Render API -> exact token IDs
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
- `bounded-affinity-v1`: SHA-256 session-key storage, Rendezvous Hash, bounded TTL registry and load spillover;
- non-blocking admission, per-backend connection bounds, transport-error EWMA circuits and state garbage collection;
- Prometheus routing/discovery/backend metrics and `X-FishMesh-*` request provenance;
- strict configuration, probes, graceful shutdown, least-privilege RBAC and race-tested request lifecycle;
- a pinned llm-d Router v0.9.0 adapter and `fishmesh-epp` composition root with local contract tests.

The R6A real-signal gate has passed: vLLM Render/KVEvents/replay produced a per-Pod 128-token match for two
different sessions sharing one system prompt, and real eviction, subscriber recovery and Pod replacement removed
stale locality. Exact routing is still not a completed Gateway feature; R6B now implements the tokenization and
KV-cache capability domains before changing the request path.

The current `X-FishMesh-Prefix-Key` header is therefore a compatibility session hint, not proof of prefix-cache
awareness. It will become optional once exact routing is integrated.

## Routing contract

The target policy remains deliberately explainable:

1. exclude terminating, stale, open-circuit or otherwise ineligible endpoints;
2. compute each Pod's real `matched_prefix_tokens` and `uncached_tokens`;
3. estimate queued work and uncached prefill cost from fresh load samples;
4. apply a hard overload guard so cache locality cannot dominate severe pressure;
5. use a small benefit margin/hysteresis to avoid routing churn;
6. degrade from exact-cache-load to load-aware when KV state is unavailable or stale;
7. use an optional session hint only as a tie-breaker or short-term stability signal;
8. expose a typed policy, reason, cache source and degradation state for every decision.

FishMesh does not claim a novel routing algorithm. The engineering contribution is a bounded, observable and
operable path from real engine state to a lightweight streaming data plane, plus a standard llm-d integration.

## Run the current baseline

Verify the repository and cluster:

```bash
make ci

kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm get deploy,pod,svc,endpointslice
```

Forward the deployed Gateway:

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm port-forward svc/fishmesh-gateway 8080:8080
```

Send a streaming request. The header is optional for connectivity; while the current baseline is active it
provides session affinity:

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-FishMesh-Prefix-Key: demo-session' \
  -d '{
    "model": "qwen2.5-0.5b-instruct",
    "messages": [{"role": "user", "content": "Introduce FishMesh briefly."}],
    "stream": true,
    "max_tokens": 64
  }'
```

## Delivery priorities

| Priority | Scope | Decision |
| --- | --- | --- |
| Primary | Gateway, request path, routing, discovery, observation, circuit, admission and transport | Productize |
| New core | tokenization, KVEvents/index and exact-cache-load policy | R6A passed; implement contract-first in R6B |
| Standard | llm-d adapter, `fishmesh-epp`, Gateway/InferencePool deployment | Keep; complete after Lite MVP |
| Development only | simulator and load generator | Freeze features; retain for regression/benchmark |
| Frozen | analyst and Diagnostics context | Security/build fixes only; remove from default image/deploy |
| Excluded | eBPF, Agent actuator, FishMesh CRD/Operator, P/D, shared DB and generic AI Gateway | Outside this MVP |

Removing a module from the product surface does not mean immediately deleting its history. Low-priority code is
first frozen and split out of release artifacts; physical deletion, if useful, is a separate reviewable change.

## Roadmap

1. **R6A — real KV signal gate (complete):** Render/KVEvents/replay, 128-token cross-session match, eviction,
   restart cleanup and explicit invalid/recovery were verified on the existing K3s cluster.
2. **R6B — capability domains:** contract-first tokenization and KV cache packages, bounded index, pure
   cache/load policy, request-path integration and llm-d precise-match translation.
3. **R6C — Lite delivery:** gateway-only image, one-command deployment, security/resources, dashboard, alerts,
   runbook and real rollout/failure acceptance.
4. **R6D — bounded comparison:** Service, FishMesh load-only, FishMesh exact and llm-d precise under cold,
   shared-system-prefix and overload workloads.
5. **R6E — Standard delivery:** complete Gateway/HTTPRoute/InferencePool/EPP deployment and wire-level contracts.

Experiments only decide an implementation, verify acceptance or prevent regression. Existing simulator and
historical experiment artifacts are not independent product tracks.

## Engineering rules

Go changes follow the mandatory [code organization rules](docs/design/code-organization.md). New capabilities are
implemented in this order: contract and values, contract tests, atomic implementation, orchestration, external
adapter, real-cluster validation and stage documentation. Main orchestration functions keep 3–7 same-level steps;
protocol parsing, block indexing and fallback decisions cannot be mixed into one large function.

Production code uses clear Chinese comments to explain invariants, ownership, cancellation, freshness and
degradation. Comments must explain why the behavior exists, not translate the Go syntax.

See the [project charter](docs/design/project-charter.md), [architecture](docs/design/architecture.md),
[implementation plan](docs/design/plan.md), [ADR-002](docs/design/decisions/002-lite-exact-kv-routing.md) and
[current status](docs/notes/project-status.md).

## Verified environment and limitations

The current cluster is K3s `v1.36.3+k3s1` with vLLM `0.23.0` and two vLLM processes sharing one time-sliced
RTX 4060 Laptop GPU. It is suitable for engine compatibility, routing behavior, failure recovery and relative
overhead. It does not represent two independent GPU failure domains and cannot support production-scale or
horizontal-scaling claims.

Raw benchmark output, logs and cluster snapshots remain outside Git. Repository history contains source,
declarative configuration, schemas and reviewed conclusions only.
