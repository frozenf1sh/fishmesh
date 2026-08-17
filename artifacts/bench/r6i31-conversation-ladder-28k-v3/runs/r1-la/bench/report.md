# FishMesh benchmark report

- Run: `r1-la`
- Cache mode/generation: `steady-warm` / `qwen-vllm-8559c597c8`
- Workload seed: `20260831`
- Requests: 21 (success 21, failed 0)
- TTFT P50/P95: 1295.15 / 4471.19 ms
- Duration P50/P95: 1753.31 / 4579.93 ms
- Prompt-token missing/violations: 0 / 0

- Gateway metrics: admitted 1.291 QPS, completed 1.291 QPS, admission rejects 0.000 QPS, average in-flight 2.547, Little's Law W 1973.05 ms, warmup requests planned/excluded 0
- Gateway memory: RSS start/peak/end 31.99/37.28/34.24 MiB (delta 2.25 MiB), Go heap start/peak/end 3.27/7.25/4.63 MiB (delta 1.35 MiB)

| Scenario | Pattern | Arrival QPS | Target tokens | Actual P50/P95 | Prefix bytes | Requests | Success | TTFT P50 | TTFT P95 | Cached samples | Cached tokens |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| conversation-ladder-28k | same-prefix | 0.000 | 0 | 0/0 | 16384 | 21 | 21 | 1295.15 | 4471.19 | 0 | 0 |
  - Gateway conversation-ladder-28k: accepted 1.291 QPS, completed 1.291 QPS, rejected 0.000 QPS, average in-flight 2.547, Little's Law W 1973.05 ms

Unavailable KV status is reported separately from an available zero-token cache miss. Prompt text and API credentials are not included.
