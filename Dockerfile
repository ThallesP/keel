# syntax=docker/dockerfile:1
#
# Keel: one image, one binary. The same image runs `keel serve` (control plane: API, WebSocket,
# embedded dashboard, SQLite, Swarm driver), `keel proxy` (the internet-facing edge) and
# `keel agent` (the per-node Swarm global service); compose and the agent service pick the
# command. Build context: the repo root.
#
#   docker build -t keel --build-arg VERSION=1.2.3 .
#
# Stages: web (bun builds apps/web/dist) -> go (embeds it, -tags embedweb) -> runtime. Plain
# Dockerfile features only (no cache mounts, no $BUILDPLATFORM), so it builds with BuildKit and
# with the legacy builder alike; release images are built natively per architecture.

ARG VERSION=dev

# -- 1. Dashboard ------------------------------------------------------------------------------
# Node is there for the CLIs whose shebang wants it (turbo, vite); bun installs and runs scripts.
FROM node:24-slim AS prune
COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /app
COPY . .
# Only the web app and the workspace packages it depends on, whatever they are at the time:
# out/json = package.json files + pruned lockfile, out/full = their sources.
RUN bun x turbo@2.10.12 prune web --docker

FROM node:24-slim AS web
COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /app
COPY --from=prune /app/out/json/ .
# Lifecycle scripts (the root postinstall runs varlock codegen) need sources not copied yet.
RUN bun install --frozen-lockfile --ignore-scripts
COPY --from=prune /app/out/full/ .
ENV NODE_ENV=production
# varlock generates src/env.ts from .env.schema, while the app still has one.
RUN cd apps/web \
    && if [ -f .env.schema ]; then bun x varlock codegen; fi \
    && bun run build \
    && test -f dist/index.html

# -- 2. Binary ---------------------------------------------------------------------------------
FROM golang:1.27 AS go
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/apps/web/dist ./apps/web/dist
ARG VERSION
RUN go build -tags embedweb -trimpath \
      -ldflags "-s -w -X github.com/ThallesP/keel/internal/cli.Version=${VERSION}" \
      -o /out/keel ./cmd/keel \
    && /out/keel --help > /dev/null

# -- 3. Runtime --------------------------------------------------------------------------------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=go /out/keel /usr/local/bin/keel
# Runs as root on purpose. `keel serve` drives Docker through /var/run/docker.sock, owned by the
# host's root and a docker group whose gid differs per host, so no fixed non-root user can count
# on reaching it (and that socket is root on the host anyway). `keel proxy` setns()es into the
# host network namespace (/proc/1/ns/net) to bind public listeners, which needs CAP_SYS_ADMIN in
# the initial user namespace. `keel agent` reads the Docker socket too. Compose and the agent
# service drop every capability a role does not need (cap_drop: ALL, then cap_add) and set
# no-new-privileges.
LABEL org.opencontainers.image.source="https://github.com/ThallesP/keel" \
      org.opencontainers.image.title="keel" \
      org.opencontainers.image.description="Keel: self-hosted deploys on your own servers"
ENV KEEL_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["keel"]
CMD ["serve"]
