# Experiment artifacts

This directory is evidence, not a scratch directory. Raw JSONL, failed runs and
reruns are retained together so reports cannot silently select only the best
execution.

The historical `connection-matrix/` files predate the run metadata contract.
Recovered cluster logs are stored under `published/` with a `manifest.json`
whose `provenance_quality` is `partial-historical-recovery`: the Job spec and
raw output are authoritative, but the Gateway configuration that was active at
that historical instant can no longer be reconstructed from the live cluster.
The first recovery batch called the merged container log `requests.jsonl.gz`;
because Kubernetes combines stdout and stderr, analysis must use the validated
`records.jsonl.gz` beside it. New recoveries name the exact stream
`container.log.gz` and the validated stream `records.jsonl.gz`.

New loadgen output begins with a `run_metadata` record. A publishable experiment
must additionally retain:

- Git revision and image digests;
- vLLM version and arguments;
- cluster/node/GPU/driver profile;
- policy configuration and execution order;
- every raw run, including failures and retries;
- the analysis command or script that produced report tables.

Use `scripts/archive-live-experiment.sh` only to recover a completed Job that is
still present in the cluster. Future experiment orchestration will capture the
full pre-run configuration rather than relying on post-hoc recovery.
