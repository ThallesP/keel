#!/usr/bin/env bash
# One-time Swarm bootstrap for the control-plane box.
# See docs/workers.md, "Cluster bootstrap". Idempotent: safe to re-run.
# install.sh runs this before the control plane starts, because keel-proxy (deploy/compose.yml)
# joins the `keel` overlay. The per-node agent (keel-agent) is not deployed here: `keel serve`
# creates and updates it.
set -euo pipefail

TAILSCALE_IP="${TAILSCALE_IP:-$(tailscale ip -4)}"

if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" != "active" ]; then
  # Advertise and listen on the tailnet only, so no Swarm port is reachable from the public internet.
  docker swarm init \
    --advertise-addr "$TAILSCALE_IP" \
    --listen-addr "$TAILSCALE_IP:2377" \
    --data-path-addr "$TAILSCALE_IP" \
    --default-addr-pool 10.200.0.0/16 \
    --default-addr-pool-mask-length 24
fi

if ! docker network inspect keel >/dev/null 2>&1; then
  # MTU below WireGuard's 1280 so overlay packets don't fragment over Tailscale.
  docker network create -d overlay --attachable \
    --opt com.docker.network.driver.mtu=1200 \
    keel
fi

echo "swarm ready, manager advertised on $TAILSCALE_IP"
