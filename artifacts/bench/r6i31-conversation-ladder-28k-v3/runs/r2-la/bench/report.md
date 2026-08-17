# FishMesh benchmark report

- Run: `r2-la`
- Cache mode/generation: `steady-warm` / `qwen-vllm-5d94dd9c79`
- Workload seed: `20260901`
- Requests: 21 (success 21, failed 0)
- TTFT P50/P95: 1235.49 / 3973.91 ms
- Duration P50/P95: 1564.87 / 4084.01 ms
- Prompt-token missing/violations: 0 / 0

- Gateway metrics: admitted 1.316 QPS, completed 1.316 QPS, admission rejects 0.000 QPS, average in-flight 2.562, Little's Law W 1946.18 ms, warmup requests planned/excluded 0
- Gateway memory: RSS start/peak/end 32.40/37.30/34.90 MiB (delta 2.50 MiB), Go heap start/peak/end 4.03/6.93/4.89 MiB (delta 0.86 MiB)

| Scenario | Pattern | Arrival QPS | Target tokens | Actual P50/P95 | Prefix bytes | Requests | Success | TTFT P50 | TTFT P95 | Cached samples | Cached tokens |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| conversation-ladder-28k | same-prefix | 0.000 | 0 | 0/0 | 16384 | 21 | 21 | 1235.49 | 3973.91 | 0 | 0 |
  - Gateway conversation-ladder-28k: accepted 1.316 QPS, completed 1.316 QPS, rejected 0.000 QPS, average in-flight 2.562, Little's Law W 1946.18 ms

Unavailable KV status is reported separately from an available zero-token cache miss. Prompt text and API credentials are not included.
