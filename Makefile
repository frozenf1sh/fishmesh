VERSION ?= r6c-lite-r1

.PHONY: all build test vet ci manifest manifest-standard manifest-validation image act-list act

all: test

build:
	go build ./...

test:
	go test -race ./...

vet:
	go vet ./...

# manifest 只检查当前 Lite 交付面；Standard/llm-d 与集群验证清单需要显式调用可选目标。
manifest:
	kubectl kustomize deploy/monitoring >/dev/null
	kubectl kustomize deploy/lite-kv-aware >/dev/null
	kubectl kustomize deploy/baseline/base >/dev/null
	kubectl kustomize deploy/inference >/dev/null
	kubectl kustomize deploy/system >/dev/null

# Standard/llm-d 暂挂，不进入默认产品门禁。
manifest-standard:
	kubectl kustomize deploy/integrated/llmd-config >/dev/null

# 集群 smoke 与模型预热是人工验收入口，不进入普通代码 CI。
manifest-validation:
	kubectl kustomize deploy/validation >/dev/null

ci: test vet build manifest

image:
	./scripts/build-and-load-gateway-image.sh $(VERSION)

# act does not automatically read Docker's active context. OrbStack exposes this socket
# on macOS; an explicitly supplied DOCKER_HOST keeps local Action execution consistent.
act-list:
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.orbstack/run/docker.sock} act --list

act:
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.orbstack/run/docker.sock} act -j verify
