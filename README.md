# FishMesh

> A Kubernetes-native traffic scheduler for self-hosted LLM inference.

FishMesh routes OpenAI-compatible streaming requests across dynamic vLLM
replicas. It preserves request affinity while a preferred backend has capacity,
spills traffic to a less-loaded backend under pressure, and falls back safely
when endpoint discovery becomes unavailable or stale.

The project focuses on the request-serving problems that sit between a generic
Kubernetes Service and a model server: long-lived streaming requests, dynamic
backend membership, overload isolation, routing state lifecycle and observable
failure semantics.

## Why FishMesh

A Kubernetes Service can distribute connections, but it does not understand
request affinity, model-server queue state or the lifecycle of a long-running
LLM stream. Pure affinity can improve reuse for repeated sessions, but it can
also overload one replica and amplify tail latency.

FishMesh implements a bounded policy instead:

1. discover only Ready vLLM endpoints and reject stale discovery state;
2. select a stable preferred backend from a cooperative request/session key;
3. keep affinity while queue and local in-flight limits remain within bounds;
4. spill to a less-loaded backend when the preferred backend crosses a bound;
5. record why every request was kept, spilled or sent through the Service
   fallback.

## Current capabilities

- OpenAI-compatible HTTP/SSE proxying with cancellation, complete stream
  draining and TTFT metrics;
- namespace-scoped EndpointSlice watch/list with Ready filtering, periodic
  relist, freshness and Kubernetes Service fallback;
- per-backend vLLM queue/running observations with explicit availability and
  age;
- `bounded-affinity-v1`: SHA-256 routing-key storage, Rendezvous Hash, bounded
  TTL registry and independent queue/local-inflight spillover thresholds;
- non-blocking admission, per-backend connection limits, transport-error EWMA
  circuits and endpoint-scoped state garbage collection;
- Prometheus metrics and response provenance for routing decisions, discovery
  state, backend observations and spillover reasons;
- bounded configuration parsing, readiness/liveness probes, graceful shutdown,
  least-privilege RBAC and race-tested scheduler/discovery paths;
- a deterministic load generator and K3s validation workloads used to verify
  system behavior.

## Runtime architecture

```text
OpenAI client
  -> FishMesh request lifecycle
       -> endpoint eligibility
       -> bounded-affinity scheduler
       -> per-backend transport
       -> vLLM replica

EndpointSlice -----> immutable endpoint snapshot ----+
vLLM metrics ------> backend observation snapshot ---+-> scheduler
local outcomes ----> in-flight / error state --------+

Prometheus <-------- decisions, failures and latency
```

The current standalone Go Gateway owns the complete request lifecycle so the
scheduler can be developed and tested without an external proxy. It is the
implemented development and demonstration mode, not an attempt to replace a
production gateway.

The production-shaped integration target is an Endpoint Picker/scheduler
extension behind an Envoy-compatible Gateway. Gateway API Inference Extension
now keeps the InferencePool and lightweight EPP APIs, while the full EPP
scheduler has moved to llm-d. FishMesh will therefore validate an llm-d
scheduler plugin or protocol-compatible integration before growing the custom
proxy further:

- [Gateway API Inference Extension](https://github.com/kubernetes-sigs/gateway-api-inference-extension)
- [llm-d Request Scheduler](https://llm-d.ai/docs/architecture/core/router/epp/scheduling)

## Failure contract

| Condition | Current behavior | Next hardening step |
| --- | --- | --- |
| EndpointSlice unavailable but snapshot fresh | continue from bounded snapshot | fault-duration E2E test |
| Snapshot stale or no Ready endpoint | use Kubernetes Service fallback | explicit alert and recovery SLO |
| Preferred backend crosses a load bound | spill without rewriting affinity | admission and saturation tests |
| Partial/missing queue observations | exclude queue from the decision | per-field sample contract |
| Upstream transport errors | short-TTL EWMA circuit; client cancellation excluded | simulator fault E2E |
| Endpoint removed | stop selection; reclaim transport, counters, circuit and metrics | churn soak test |

## Run locally against the K3s cluster

Verify the repository first:

```bash
make ci
```

Inspect the configured cluster:

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml get nodes
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm get deploy,pod,svc,endpointslice
```

Forward the Gateway and send a streaming request:

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml \
  -n kubellm port-forward svc/fishmesh-gateway 8080:8080

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

The response exposes the selected policy, preferred backend, selected backend
and spillover reason through `X-FishMesh-*` headers.

## Engineering scope

FishMesh owns:

- endpoint eligibility and routing-state lifecycle;
- bounded, explainable endpoint selection;
- overload and failure behavior on the request path;
- the observable contract needed to operate and test those components.

FishMesh reuses upstream vLLM for inference and will reuse Gateway API/Envoy or
llm-d for production ingress and request-control integration. Authentication,
tenant billing, general API-gateway features, exact token-block cache indexing,
GPU kernels and model execution are not reimplemented here.

The read-only `fishmesh-analyst` remains a secondary diagnostic prototype. Its
scope is frozen until request-path reliability, standard integration and E2E
operations are complete.

## Roadmap

1. **Request-path reliability:** admission, circuits, state GC and connection
   bounds are implemented; simulator soak coverage remains.
2. **Standard integration:** controlled backend simulator, EPP/llm-d integration
   spike, then one supported integrated runtime path.
3. **Operability:** automated fault E2E tests, dashboards, tracing, multi-arch
   release image and supply-chain metadata.
4. **Comparative validation:** a bounded workload matrix against Service,
   least-loaded and one open-source scheduler.

Experiments may change an engineering decision or validate an acceptance
criterion; they are not an independent product track. See the durable
[project charter](docs/design/project-charter.md), the
[implementation plan](docs/design/plan.md) and the
[experiment policy](docs/experiments/plan.md).

## Verified environment and limitations

The current cluster is K3s `v1.36.3+k3s1` with vLLM `0.23.0` and two vLLM
processes sharing one time-sliced RTX 4060 Laptop GPU. It validates request
lifecycle, discovery, routing and recovery behavior. It does not represent two
independent GPU failure domains and is not used to claim production-scale
performance.

The latest bounded-affinity K3s smoke completed 24/24 requests and exercised
both affinity and local-inflight spillover. Raw benchmark output and cluster
snapshots are retained outside Git; only code, reproducible configuration and
reviewed conclusions belong in repository history.
