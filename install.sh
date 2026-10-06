#!/usr/bin/env bash
# Keel installer. Turns a Linux server into a Keel control plane:
#   curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash
#
# Idempotent: re-running it is the upgrade. Secrets are generated once and kept in
# $KEEL_DIR/.env (0600). Progress goes to stderr; with KEEL_JSON=1, stdout carries exactly one
# JSON object ({"ok":true,...} or {"ok":false,"error":...}). Every failure exits non-zero with
# an `error:` line and, when there is one, a `fix:` line.
#
# Optional environment (all of it is non-interactive except a Tailscale login without a key):
#   KEEL_TAILSCALE_AUTHKEY  tskey-auth-... to join the tailnet without a browser login
#   KEEL_ADDR               Bind to this IP instead of the tailnet IP; skips Tailscale (LAN, CI)
#   KEEL_PUBLIC_IP          This server's public IPv4 (default: detected). Names the default
#                           https://<service>-<id>.<ip>.sslip.io domains of exposed services
#   KEEL_VERSION            Image tag of keel-web / keel-worker / keel-functions (default latest)
#   KEEL_WEB_PORT           Dashboard port on KEEL_ADDR (default 80)
#   KEEL_JSON=1             Machine-readable result on stdout
#   KEEL_DIR                State directory (default /opt/keel)
#   KEEL_REF                Git ref to fetch compose.yml and scripts from (default main)
#   KEEL_SRC                Use this local checkout instead of fetching (development, CI)
#   KEEL_IMAGE_PREFIX       Image registry/namespace (default ghcr.io/thallesp)
#   KEEL_PULL=0             Do not pull images (use images already present locally)
# Values you pass are saved in $KEEL_DIR/.env and reused by later runs.
#
# See README.md, "Install", for what this touches and how to uninstall.
set -Eeuo pipefail

log() { printf '\033[1m==>\033[0m %s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
json_str() { local s=${1//\\/\\\\}; s=${s//\"/\\\"}; printf '"%s"' "$s"; }
die() {
  printf 'error: %s\n' "$1" >&2
  [ -n "${2:-}" ] && printf 'fix:   %s\n' "$2" >&2
  [ "${KEEL_JSON:-}" = 1 ] && printf '{"ok":false,"error":%s,"fix":%s}\n' "$(json_str "$1")" "$(json_str "${2:-}")"
  exit 1
}
trap 'die "install.sh failed at line $LINENO: $BASH_COMMAND" "re-run with the same command; it is safe to repeat"' ERR

rand_hex() { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# Saved value of KEY in the state file, or empty. Values are single-quoted so the file can be
# sourced by a shell (the admin key contains `|`); docker compose strips the quotes too.
saved() { [ -f "$ENV_FILE" ] && sed -n "s/^$1='\{0,1\}\([^']*\)'\{0,1\}$/\1/p" "$ENV_FILE" | tail -1 || true; }

compose() { docker compose -p keel --env-file "$ENV_FILE" -f "$KEEL_DIR/compose.yml" "$@"; }

functions() {
  docker run --rm --network host \
    -e CONVEX_SELF_HOSTED_URL="http://$KEEL_ADDR:3210" -e CONVEX_SELF_HOSTED_ADMIN_KEY \
    -e SITE_URL -e BETTER_AUTH_SECRET -e KEEL_WORKER_TOKEN -e KEEL_PUBLIC_IP \
    "$KEEL_IMAGE_PREFIX/keel-functions:$KEEL_VERSION" "$@"
}

preflight() {
  [ "$(id -u)" = 0 ] || die "must run as root" "curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash"
  [ "$(uname -s)" = Linux ] || die "Keel runs on Linux (got $(uname -s))"
  case "$(uname -m)" in
    x86_64 | amd64 | aarch64 | arm64) ;;
    *) die "unsupported CPU architecture $(uname -m)" "use an amd64 or arm64 server" ;;
  esac
  command -v curl >/dev/null || die "curl is required" "apt-get install -y curl (or your distro's equivalent)"
  local mem_kb
  mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
  [ "$mem_kb" -ge 1900000 ] || warn "less than 2 GB RAM; the control plane may run out of memory"
}

ensure_docker() {
  if ! command -v docker >/dev/null; then
    log "installing Docker (get.docker.com)"
    curl -fsSL https://get.docker.com | sh >&2
  fi
  docker info >/dev/null 2>&1 || die "the Docker daemon is not running" "systemctl enable --now docker"
  docker compose version >/dev/null 2>&1 || die "the Docker Compose plugin is missing" "install docker-compose-plugin, or reinstall Docker from https://get.docker.com"
}

ensure_tailscale() {
  if [ -n "$KEEL_ADDR_OVERRIDE" ]; then
    KEEL_ADDR=$KEEL_ADDR_OVERRIDE
    log "using KEEL_ADDR=$KEEL_ADDR (Tailscale skipped)"
    return
  fi
  if ! command -v tailscale >/dev/null; then
    log "installing Tailscale (tailscale.com/install.sh)"
    curl -fsSL https://tailscale.com/install.sh | sh >&2
  fi
  if [ -z "$(tailscale ip -4 2>/dev/null | head -1)" ]; then
    if [ -n "${KEEL_TAILSCALE_AUTHKEY:-}" ]; then
      log "joining the tailnet with KEEL_TAILSCALE_AUTHKEY"
      local keyfile
      keyfile=$(mktemp)
      printf %s "$KEEL_TAILSCALE_AUTHKEY" >"$keyfile"
      tailscale up --auth-key="file:$keyfile" >&2 || { rm -f "$keyfile"; die "tailscale up failed" "check the auth key at https://login.tailscale.com/admin/settings/keys"; }
      rm -f "$keyfile"
    else
      log "log this server into Tailscale: open the URL below (waits up to 15 minutes)"
      tailscale up --timeout=15m >&2 ||
        die "Tailscale login did not complete" "re-run with KEEL_TAILSCALE_AUTHKEY=tskey-auth-... (https://login.tailscale.com/admin/settings/keys)"
    fi
  fi
  KEEL_ADDR=$(tailscale ip -4 | head -1)
  [ -n "$KEEL_ADDR" ] || die "no tailnet IPv4 address" "check 'tailscale status'"
  log "tailnet address $KEEL_ADDR"
}

# The address the internet reaches this server on: what exposed services' domains and ports
# point at (docs/networking.md). Behind NAT it is the router's, which is the one to forward.
# Not fatal: without it Keel still runs, only Expose asks for it.
detect_public_ip() {
  if [ -n "$KEEL_PUBLIC_IP" ]; then
    log "public IP $KEEL_PUBLIC_IP"
    return
  fi
  local url
  for url in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    KEEL_PUBLIC_IP=$(curl -4fsS --max-time 5 "$url" 2>/dev/null | tr -d '[:space:]' || true)
    if [[ "$KEEL_PUBLIC_IP" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
      log "public IP $KEEL_PUBLIC_IP (detected; set KEEL_PUBLIC_IP to override)"
      return
    fi
  done
  KEEL_PUBLIC_IP=""
  warn "could not detect this server's public IPv4; set KEEL_PUBLIC_IP and re-run to expose services"
}

fetch() { # <repo path> <dest> <mode>
  if [ -n "${KEEL_SRC:-}" ]; then
    install -m "$3" "$KEEL_SRC/$1" "$2"
  else
    curl -fsSL "https://raw.githubusercontent.com/ThallesP/keel/$KEEL_REF/$1" -o "$2.tmp" ||
      die "could not download $1 from ref $KEEL_REF" "check KEEL_REF and network access to raw.githubusercontent.com"
    chmod "$3" "$2.tmp" && mv "$2.tmp" "$2"
  fi
}

write_state() {
  mkdir -p "$KEEL_DIR/scripts"
  chmod 700 "$KEEL_DIR"
  fetch deploy/compose.yml "$KEEL_DIR/compose.yml" 644
  fetch scripts/bootstrap-swarm.sh "$KEEL_DIR/scripts/bootstrap-swarm.sh" 755
  fetch scripts/deploy-worker.sh "$KEEL_DIR/scripts/deploy-worker.sh" 755

  INSTANCE_SECRET=$(saved INSTANCE_SECRET); INSTANCE_SECRET=${INSTANCE_SECRET:-$(rand_hex)}
  BETTER_AUTH_SECRET=$(saved BETTER_AUTH_SECRET); BETTER_AUTH_SECRET=${BETTER_AUTH_SECRET:-$(rand_hex)}
  KEEL_WORKER_TOKEN=$(saved KEEL_WORKER_TOKEN); KEEL_WORKER_TOKEN=${KEEL_WORKER_TOKEN:-$(rand_hex)}
  CONVEX_SELF_HOSTED_ADMIN_KEY=${CONVEX_SELF_HOSTED_ADMIN_KEY:-$(saved CONVEX_SELF_HOSTED_ADMIN_KEY)}
  save_state
}

save_state() {
  (
    umask 077
    cat >"$ENV_FILE.tmp" <<EOF
# Keel install state, written by install.sh. Secrets: keep this file private (0600).
# KEEL_ADDR is derived (tailnet IP) unless KEEL_ADDR_OVERRIDE is set.
KEEL_ADDR='$KEEL_ADDR'
KEEL_ADDR_OVERRIDE='$KEEL_ADDR_OVERRIDE'
KEEL_PUBLIC_IP='$KEEL_PUBLIC_IP'
KEEL_VERSION='$KEEL_VERSION'
KEEL_WEB_PORT='$KEEL_WEB_PORT'
KEEL_IMAGE_PREFIX='$KEEL_IMAGE_PREFIX'
INSTANCE_SECRET='$INSTANCE_SECRET'
BETTER_AUTH_SECRET='$BETTER_AUTH_SECRET'
KEEL_WORKER_TOKEN='$KEEL_WORKER_TOKEN'
CONVEX_SELF_HOSTED_ADMIN_KEY='$CONVEX_SELF_HOSTED_ADMIN_KEY'
EOF
    mv "$ENV_FILE.tmp" "$ENV_FILE"
  )
}

# keel-proxy joins the Swarm overlay, so Swarm and the network come before `compose up`.
start_swarm() {
  log "initialising Docker Swarm and the keel overlay network"
  TAILSCALE_IP=$KEEL_ADDR bash "$KEEL_DIR/scripts/bootstrap-swarm.sh" --swarm-only >&2
}

start_control_plane() {
  if [ "$KEEL_PULL" = 1 ]; then
    log "pulling images ($KEEL_IMAGE_PREFIX, tag $KEEL_VERSION)"
    compose pull --quiet >&2 || die "could not pull the control-plane images" "check KEEL_VERSION and access to ghcr.io"
    docker pull -q "$KEEL_IMAGE_PREFIX/keel-functions:$KEEL_VERSION" >/dev/null
    docker pull -q "$KEEL_IMAGE_PREFIX/keel-worker:$KEEL_VERSION" >/dev/null
  fi
  log "starting the control plane (Convex backend, web, proxy)"
  compose up -d --wait --wait-timeout 180 --remove-orphans >&2 ||
    die "the control plane did not become healthy" "docker compose -p keel logs backend web"

  if [ -z "$CONVEX_SELF_HOSTED_ADMIN_KEY" ]; then
    CONVEX_SELF_HOSTED_ADMIN_KEY=$(compose exec -T backend ./generate_admin_key.sh | grep '|' | tail -1 || true)
    [ -n "$CONVEX_SELF_HOSTED_ADMIN_KEY" ] || die "could not generate the Convex admin key" "docker compose -p keel logs backend"
    save_state
  fi
  functions check >&2 ||
    die "the Convex backend rejected the stored admin key" "delete CONVEX_SELF_HOSTED_ADMIN_KEY from $ENV_FILE and re-run"

  log "pushing Keel functions and settings to Convex, then migrating and syncing the proxy"
  functions deploy >&2 || die "pushing functions failed" "see the output above; the backend needs outbound access to registry.npmjs.org"
}

start_workers() {
  log "starting the per-node worker"
  TAILSCALE_IP=$KEEL_ADDR KEEL_URL="http://$KEEL_ADDR:3211" \
    KEEL_WORKER_IMAGE="$KEEL_IMAGE_PREFIX/keel-worker:$KEEL_VERSION" \
    bash "$KEEL_DIR/scripts/bootstrap-swarm.sh" >&2
}

check_health() {
  log "checking health"
  curl -fsS "http://$KEEL_ADDR:3210/version" >/dev/null || die "Convex API is not answering on $KEEL_ADDR:3210" "docker compose -p keel logs backend"
  curl -fsS "$SITE_URL/config.js" | grep -q "$KEEL_ADDR" || die "the dashboard is not serving its config on $SITE_URL" "docker compose -p keel logs web"
  curl -fsS -o /dev/null -H @- "http://$KEEL_ADDR:3211/worker/config" <<<"Authorization: Bearer $KEEL_WORKER_TOKEN" ||
    die "Convex does not accept the worker token" "re-run install.sh; it re-sends KEEL_WORKER_TOKEN"
  local _ replicas=""
  for _ in $(seq 1 60); do
    replicas=$(docker service ls --filter name=keel-worker --format '{{.Replicas}}' | head -1)
    [ -n "$replicas" ] && [ "${replicas%%/*}" = "${replicas##*/}" ] && [ "${replicas##*/}" != 0 ] && return 0
    sleep 2
  done
  die "keel-worker is not running (replicas: ${replicas:-none})" "docker service ps keel-worker --no-trunc"
}

main() {
  KEEL_DIR=${KEEL_DIR:-/opt/keel}
  ENV_FILE="$KEEL_DIR/.env"
  KEEL_REF=${KEEL_REF:-main}
  KEEL_PULL=${KEEL_PULL:-1}
  # An explicit KEEL_ADDR sticks across runs; otherwise the tailnet IP is re-read every run.
  KEEL_ADDR_OVERRIDE=${KEEL_ADDR:-$(saved KEEL_ADDR_OVERRIDE)}
  KEEL_ADDR=""
  KEEL_VERSION=${KEEL_VERSION:-$(saved KEEL_VERSION)}; KEEL_VERSION=${KEEL_VERSION:-latest}
  KEEL_WEB_PORT=${KEEL_WEB_PORT:-$(saved KEEL_WEB_PORT)}; KEEL_WEB_PORT=${KEEL_WEB_PORT:-80}
  KEEL_IMAGE_PREFIX=${KEEL_IMAGE_PREFIX:-$(saved KEEL_IMAGE_PREFIX)}; KEEL_IMAGE_PREFIX=${KEEL_IMAGE_PREFIX:-ghcr.io/thallesp}
  KEEL_PUBLIC_IP=${KEEL_PUBLIC_IP:-$(saved KEEL_PUBLIC_IP)}

  preflight
  ensure_docker
  ensure_tailscale
  detect_public_ip
  SITE_URL="http://$KEEL_ADDR"; [ "$KEEL_WEB_PORT" = 80 ] || SITE_URL="$SITE_URL:$KEEL_WEB_PORT"
  write_state
  export CONVEX_SELF_HOSTED_ADMIN_KEY SITE_URL BETTER_AUTH_SECRET KEEL_WORKER_TOKEN KEEL_PUBLIC_IP
  start_swarm
  start_control_plane
  start_workers
  check_health

  printf '\n\033[1mKeel is running.\033[0m\n\n  Dashboard  %s\n  Convex     http://%s:3210\n  State      %s (secrets, 0600)\n\n' \
    "$SITE_URL" "$KEEL_ADDR" "$ENV_FILE" >&2
  printf 'Open the dashboard from any device on your tailnet and sign up.\nUpgrade: re-run the install command.\n' >&2
  printf '\nExposed services are served from %s: let ports 80 and 443 (TCP) through\nits firewall or router, plus each TCP/UDP port you expose.\n' "${KEEL_PUBLIC_IP:-this server}" >&2
  if [ "${KEEL_JSON:-}" = 1 ]; then
    printf '{"ok":true,"url":%s,"convexUrl":%s,"convexSiteUrl":%s,"version":%s,"stateDir":%s,"publicIp":%s}\n' \
      "$(json_str "$SITE_URL")" "$(json_str "http://$KEEL_ADDR:3210")" "$(json_str "http://$KEEL_ADDR:3211")" \
      "$(json_str "$KEEL_VERSION")" "$(json_str "$KEEL_DIR")" "$(json_str "$KEEL_PUBLIC_IP")"
  fi
}

# Everything runs from here, so a truncated download (curl | bash) executes nothing.
main "$@"
