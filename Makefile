VERSION ?= r6c-lite-r1

.PHONY: all build test vet ci manifest manifest-experiments image act-list act

all: test

build:
	go build ./...

test:
	go test -race ./...

vet:
	go vet ./...

# manifest 只检查当前交付面:Lite、baseline、Standard 集成、推理、系统、监控资产与验证 overlay。
# 历史实验与冻结模块(analyst/experiments)移入 manifest-experiments,保留可追踪性,
# 但不进入默认 CI 门禁,避免每个提交都维护不再演进的实验 yaml。
manifest:
	kubectl kustomize deploy/monitoring >/dev/null
	kubectl kustomize deploy/lite-exact >/dev/null
	kubectl kustomize deploy/baseline/base >/dev/null
	kubectl kustomize deploy/integrated/llmd-config >/dev/null
	kubectl kustomize deploy/inference >/dev/null
	kubectl kustomize deploy/system >/dev/null
	kubectl kustomize deploy/validation >/dev/null

# 冻结模块与历史实验 overlay 的可追踪性检查;需要回归时才手动执行。
manifest-experiments:
	kubectl kustomize deploy/analyst/base >/dev/null
	kubectl kustomize deploy/analyst/gateway-metrics >/dev/null
	kubectl kustomize deploy/analyst/observability >/dev/null
	kubectl kustomize deploy/experiments/endpoint-slice >/dev/null
	kubectl kustomize deploy/experiments/backend-snapshot >/dev/null
	kubectl kustomize deploy/experiments/bounded-affinity >/dev/null
	kubectl kustomize deploy/experiments/bounded-affinity-smoke-config >/dev/null
	kubectl kustomize deploy/experiments/bounded-affinity-smoke >/dev/null
	kubectl kustomize deploy/experiments/exact-kv-signal >/dev/null
	kubectl kustomize deploy/experiments/r6d-load-only >/dev/null
	kubectl kustomize deploy/experiments/r6d-bounded-affinity >/dev/null

ci: test vet build manifest

image:
	./scripts/build-and-load-gateway-image.sh $(VERSION)

# act does not automatically read Docker's active context. OrbStack exposes this socket
# on macOS; an explicitly supplied DOCKER_HOST keeps local Action execution consistent.
act-list:
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.orbstack/run/docker.sock} act --list

act:
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.orbstack/run/docker.sock} act -j verify
