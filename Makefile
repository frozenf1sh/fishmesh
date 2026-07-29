.PHONY: all build test vet ci manifest image act-list act

all: test

build:
	go build ./...

test:
	go test -race ./...

vet:
	go vet ./...

manifest:
	kubectl kustomize deploy/baseline/base >/dev/null
	kubectl kustomize deploy/analyst/base >/dev/null
	kubectl kustomize deploy/analyst/gateway-metrics >/dev/null
	kubectl kustomize deploy/analyst/observability >/dev/null

ci: test vet build manifest

image:
	./scripts/build-and-load-fishmesh-image.sh

# act does not automatically read Docker's active context. OrbStack exposes this socket
# on macOS; an explicitly supplied DOCKER_HOST keeps local Action execution consistent.
act-list:
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.orbstack/run/docker.sock} act --list

act:
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.orbstack/run/docker.sock} act -j verify
