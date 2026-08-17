# FishMesh benchmark report

- Run: `r2-kv`
- Cache mode/generation: `steady-warm` / `qwen-vllm-697576b95d`
- Workload seed: `20260901`
- Requests: 21 (success 21, failed 0)
- TTFT P50/P95: 1150.52 / 1771.68 ms
- Duration P50/P95: 1414.66 / 2065.06 ms
- Prompt-token missing/violations: 0 / 0

- Gateway metrics: admitted 1.941 QPS, completed 1.941 QPS, admission rejects 0.000 QPS, average in-flight 2.320, Little's Law W 1195.36 ms, warmup requests planned/excluded 0
- Gateway memory: RSS start/peak/end 33.98/47.62/36.86 MiB (delta 2.88 MiB), Go heap start/peak/end 4.70/16.22/5.30 MiB (delta 0.60 MiB)

| Scenario | Pattern | Arrival QPS | Target tokens | Actual P50/P95 | Prefix bytes | Requests | Success | TTFT P50 | TTFT P95 | Cached samples | Cached tokens |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| conversation-ladder-28k | same-prefix | 0.000 | 0 | 0/0 | 16384 | 21 | 21 | 1150.52 | 1771.68 | 21 | 307872 |
  - Gateway conversation-ladder-28k: accepted 1.941 QPS, completed 1.941 QPS, rejected 0.000 QPS, average in-flight 2.320, Little's Law W 1195.36 ms

Unavailable KV status is reported separately from an available zero-token cache miss. Prompt text and API credentials are not included.
