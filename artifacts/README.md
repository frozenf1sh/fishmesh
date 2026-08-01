# Experiment artifacts

This directory documents the evidence contract; it is not a Git-backed artifact
store. Raw JSONL, failed runs and reruns are retained together locally or in an
external immutable store so reports cannot silently select only the best
execution. `artifacts/published/`, raw JSONL, compressed logs and cluster dumps
are ignored by Git.

Repository history may contain only the generator, schemas, declarative
experiment configuration, analysis code and reviewed conclusions. Never commit
kubeconfigs, tokens, model files, node dumps containing sensitive inventory,
container images or generated binaries.

Runs invalidated by environment or rollout drift are also retained. Their
`manifest.json` contains `valid: false` and an explicit `exclusion_reason`;
they must never be silently replaced by a successful rerun.

The historical `connection-matrix/` files predate the run metadata contract.
Recovered cluster logs are stored under `published/` with a `manifest.json`
whose `provenance_quality` is `partial-historical-recovery`: the Job spec and
raw output are authoritative, but the Gateway configuration that was active at
that historical instant can no longer be reconstructed from the live cluster.
The first recovery batch called the merged container log `requests.jsonl.gz`;
because Kubernetes combines stdout and stderr, analysis must use the validated
`records.jsonl.gz` beside it. New recoveries name the exact stream
`container.log.gz` and the validated stream `records.jsonl.gz`.

New loadgen output begins with a `run_metadata` record. A reviewable experiment
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
