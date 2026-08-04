# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/fishmesh-gateway ./cmd/fishmesh-gateway \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/fishmesh-epp ./cmd/fishmesh-epp \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/fishmesh-loadgen ./cmd/fishmesh-loadgen \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/fishmesh-analyst ./cmd/fishmesh-analyst \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/fishmesh-simulator ./cmd/fishmesh-simulator

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/fishmesh-gateway /usr/local/bin/fishmesh-gateway
COPY --from=build /out/fishmesh-epp /usr/local/bin/fishmesh-epp
COPY --from=build /out/fishmesh-loadgen /usr/local/bin/fishmesh-loadgen
COPY --from=build /out/fishmesh-analyst /usr/local/bin/fishmesh-analyst
COPY --from=build /out/fishmesh-simulator /usr/local/bin/fishmesh-simulator
USER nonroot:nonroot
