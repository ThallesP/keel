#!/usr/bin/env bash
# Build the worker image and create-or-update the `keel-worker` global service: one
# container per Swarm node (apps/worker) that forwards `docker events` to Convex and streams
# container logs to the project's log sink. Idempotent; run from the manager. Called at the end
# of scripts/bootstrap-swarm.sh.
#
# Inputs, from the environment or from infra/worker/.env.local (gitignored):
#   KEEL_URL           Convex HTTP actions URL. Local dev: http://<tailnet-ip>:3211
#                          (defaults to that). Production: https://<deployment>.convex.site
#   KEEL_WORKER_TOKEN  Bearer token. Generated on first run, written to .env.local (0600)
#                          and stored in the Convex deployment (`convex env set`). Never printed.
#                          To rotate: delete it from .env.local and re-run.
#   KEEL_REGISTRY      Where to push the image so other nodes can pull it. Empty (default)
#                          builds locally only, fine for a single-node cluster. Multi-node:
#                          the tailnet registry, e.g. 100.x.y.z:5000 (docs/workers.md, Registry).
# The token reaches the containers as a Swarm secret named by content hash, so a change swaps
# it on `service update`. The image tag is the content hash of apps/worker so an unchanged
# worker is a no-op update.
# Local dev against the anonymous Convex deployment: export CONVEX_AGENT_MODE=anonymous first.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ENV_FILE="$ROOT/infra/worker/.env.local"
SERVICE=keel-worker

# shellcheck source=/dev/null
[ -f "$ENV_FILE" ] && set -a && . "$ENV_FILE" && set +a
: "${KEEL_URL:=http://${TAILSCALE_IP:-$(tailscale ip -4)}:3211}"
: "${KEEL_REGISTRY:=}"
if [ -z "${KEEL_WORKER_TOKEN:-}" ]; then
  KEEL_WORKER_TOKEN=$(openssl rand -hex 32)
  (umask 077 && printf 'KEEL_URL=%s\nKEEL_WORKER_TOKEN=%s\n' "$KEEL_URL" "$KEEL_WORKER_TOKEN" > "$ENV_FILE")
  # Piped via stdin so the token never appears in argv or shell history.
  (cd "$ROOT/packages/backend" && printf %s "$KEEL_WORKER_TOKEN" | bunx convex env set KEEL_WORKER_TOKEN)
  echo "generated KEEL_WORKER_TOKEN -> $ENV_FILE and Convex env"
fi

HASH=$(cd "$ROOT/apps/worker" && cat Dockerfile package.json tsconfig.json $(find src -type f | sort) | sha256sum | cut -c1-12)
IMAGE="${KEEL_REGISTRY:+$KEEL_REGISTRY/}keel-worker:$HASH"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -q -t "$IMAGE" "$ROOT/apps/worker" >/dev/null
  echo "built $IMAGE"
  [ -n "$KEEL_REGISTRY" ] && docker push -q "$IMAGE" >/dev/null
fi

SECRET="keel-worker-token-$(printf %s "$KEEL_WORKER_TOKEN" | sha256sum | cut -c1-12)"
docker secret inspect "$SECRET" >/dev/null 2>&1 || printf %s "$KEEL_WORKER_TOKEN" | docker secret create "$SECRET" - >/dev/null

# Swarm pulls by digest for multi-node consistency; a local-only image has none, so tell it not to.
RESOLVE=(--with-registry-auth); [ -z "$KEEL_REGISTRY" ] && RESOLVE=(--no-resolve-image)

if ! docker service inspect "$SERVICE" >/dev/null 2>&1; then
  docker service create --detach --quiet --name "$SERVICE" --mode global --network host \
    "${RESOLVE[@]}" \
    --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly \
    --mount type=volume,src=keel-worker-state,dst=/var/lib/keel-worker \
    --secret source="$SECRET",target=keel_worker_token \
    --env KEEL_URL="$KEEL_URL" \
    --restart-condition any --restart-delay 2s --stop-grace-period 10s \
    "$IMAGE" >/dev/null
  echo "created service $SERVICE"
else
  CUR_IMAGE=$(docker service inspect "$SERVICE" --format '{{.Spec.TaskTemplate.ContainerSpec.Image}}' | cut -d@ -f1)
  CUR_SECRET=$(docker service inspect "$SERVICE" --format '{{range .Spec.TaskTemplate.ContainerSpec.Secrets}}{{.SecretName}}{{end}}')
  CUR_URL=$(docker service inspect "$SERVICE" --format '{{range .Spec.TaskTemplate.ContainerSpec.Env}}{{println .}}{{end}}' | sed -n 's/^KEEL_URL=//p')
  if [ "$CUR_IMAGE" = "$IMAGE" ] && [ "$CUR_SECRET" = "$SECRET" ] && [ "$CUR_URL" = "$KEEL_URL" ]; then
    echo "service $SERVICE up to date"
  else
    docker service update --detach --quiet "${RESOLVE[@]}" --image "$IMAGE" \
      --secret-rm "$CUR_SECRET" --secret-add source="$SECRET",target=keel_worker_token \
      --env-add KEEL_URL="$KEEL_URL" "$SERVICE" >/dev/null
    echo "updated service $SERVICE -> $IMAGE"
    # Old secret can go once no task references it; a failure here only means a task is still
    # shutting down, and the next run cleans it up.
    [ "$CUR_SECRET" != "$SECRET" ] && docker secret rm "$CUR_SECRET" >/dev/null 2>&1 || true
  fi
fi

# The old shell forwarder (infra/events-sidecar, replaced by the worker 2026-09-20).
if docker service inspect keel-events >/dev/null 2>&1; then
  docker service rm keel-events >/dev/null && echo "removed legacy service keel-events"
  for c in $(docker config ls --format '{{.Name}}' | grep '^keel-events-' || true); do docker config rm "$c" >/dev/null 2>&1 || true; done
  for s in $(docker secret ls --format '{{.Name}}' | grep '^keel-events-token-' || true); do docker secret rm "$s" >/dev/null 2>&1 || true; done
fi
