# Base images are overridable so that registries which are unreachable in a given
# environment (for example Docker Hub behind a restrictive network) can be replaced
# by a mirror without editing this file. Defaults match the pinned upstream images.
ARG MIHOMO_IMAGE=metacubex/mihomo:v1.19.32
ARG GO_IMAGE=golang:1.23-alpine
ARG RUNTIME_IMAGE=alpine:3.21
# Module proxy is overridable because proxy.golang.org is not reachable from every
# network. The default stays the canonical public proxy; go.sum still pins checksums.
ARG GOPROXY=https://proxy.golang.org,direct

FROM ${MIHOMO_IMAGE} AS mihomo-bin

FROM ${GO_IMAGE} AS build
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
WORKDIR /src
RUN apk add --no-cache ca-certificates git
# go.sum is required so `go mod download` verifies module checksums instead of
# trusting whatever the proxy returns for the declared versions.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mihomo-studio ./cmd/server

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/mihomo-studio /usr/local/bin/mihomo-studio
# The Mihomo binary is copied from the same pinned image used for runtime validation,
# so candidate `mihomo -t` checks execute the exact binary that will serve traffic.
COPY --from=mihomo-bin /mihomo /usr/local/bin/mihomo
COPY migrations ./migrations
COPY web ./web
RUN mkdir -p /data/snapshots /runtime/active && chown -R app:app /data /runtime /app
USER app
ENV LISTEN_ADDR=:8080 DATABASE_PATH=/data/mihomo-studio.db SNAPSHOT_DIR=/data/snapshots ACTIVE_CONFIG_PATH=/runtime/active/config.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mihomo-studio"]
