# FishMesh benchmark report

- Run: `r1-kv`
- Cache mode/generation: `steady-warm` / `qwen-vllm-5db85c557f`
- Workload seed: `20260831`
- Requests: 21 (success 21, failed 0)
- TTFT P50/P95: 1168.09 / 1804.62 ms
- Duration P50/P95: 1419.23 / 2066.92 ms
- Prompt-token missing/violations: 0 / 0

- Gateway metrics: admitted 1.942 QPS, completed 1.942 QPS, admission rejects 0.000 QPS, average in-flight 2.390, Little's Law W 1230.69 ms, warmup requests planned/excluded 0
- Gateway memory: RSS start/peak/end 35.80/49.61/37.80 MiB (delta 2.00 MiB), Go heap start/peak/end 3.42/18.70/7.95 MiB (delta 4.52 MiB)

| Scenario | Pattern | Arrival QPS | Target tokens | Actual P50/P95 | Prefix bytes | Requests | Success | TTFT P50 | TTFT P95 | Cached samples | Cached tokens |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| conversation-ladder-28k | same-prefix | 0.000 | 0 | 0/0 | 16384 | 21 | 21 | 1168.09 | 1804.62 | 21 | 307872 |
  - Gateway conversation-ladder-28k: accepted 1.942 QPS, completed 1.942 QPS, rejected 0.000 QPS, average in-flight 2.390, Little's Law W 1230.69 ms

Unavailable KV status is reported separately from an available zero-token cache miss. Prompt text and API credentials are not included.
