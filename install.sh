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
#   KEEL_ACME_EMAIL         Email for certificates; adds ZeroSSL after Let's Encrypt
#   KEEL_ACME_CA            Testing only: ACME directory that replaces every certificate issuer
#                           (Let's Encrypt staging). Not saved: pass it on every run that wants it
#   KEEL_VERSION            Tag of the keel image (default latest)
#   KEEL_WEB_PORT           Dashboard port on KEEL_ADDR (default 80)
#   KEEL_JSON=1             Machine-readable result on stdout
#   KEEL_DIR                State directory (default /opt/keel)
#   KEEL_REF                Git ref to fetch compose.yml and the Swarm script from (default main)
#   KEEL_SRC                Use this local checkout instead of fetching (development, CI)
#   KEEL_IMAGE_PREFIX       Image registry/namespace (default ghcr.io/thallesp)
#   KEEL_PULL=0             Do not pull images (use images already present locally)
# Values you pass are saved in $KEEL_DIR/.env and reused by later runs.
#
# See README.md, "Install", for what this touches and how to uninstall.
set -Eeuo pipefail

WARNINGS=()
log() { printf '\033[1m==>\033[0m %s\n' "$*" >&2; }
warn() { WARNINGS+=("$*"); printf 'warning: %s\n' "$*" >&2; }
json_str() {
  local s=${1//\\/\\\\}
  s=${s//\"/\\\"}; s=${s//$'\n'/\\n}; s=${s//$'\r'/\\r}; s=${s//$'\t'/\\t}
  s=$(printf %s "$s" | tr -d '\001-\010\013\014\016-\037')
  printf '"%s"' "$s"
}
json_warnings() {
  local w out=""
  for w in ${WARNINGS[@]+"${WARNINGS[@]}"}; do out+="${out:+,}$(json_str "$w")"; done
  printf '[%s]' "$out"
}
die() {
  printf 'error: %s\n' "$1" >&2
  [ -n "${2:-}" ] && printf 'fix:   %s\n' "$2" >&2
  [ "${KEEL_JSON:-}" = 1 ] && printf '{"ok":false,"error":%s,"fix":%s,"warnings":%s}\n' "$(json_str "$1")" "$(json_str "${2:-}")" "$(json_warnings)"
  exit 1
}
trap 'die "install.sh failed at line $LINENO: $BASH_COMMAND" "re-run with the same command; it is safe to repeat"' ERR

rand_hex() { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# Saved value of KEY in the state file, or empty. Values are single-quoted so the file can be
# sourced by a shell; docker compose strips the quotes too.
saved() { [ -f "$ENV_FILE" ] && sed -n "s/^$1='\{0,1\}\([^']*\)'\{0,1\}$/\1/p" "$ENV_FILE" | tail -1 || true; }

compose() { docker compose -p keel --env-file "$ENV_FILE" -f "$KEEL_DIR/compose.yml" "$@"; }

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
  if docker volume inspect keel_convex-data >/dev/null 2>&1; then
    die "this server runs the Convex-era Keel (volume keel_convex-data), which does not upgrade to this one" \
      "move your services off it, then remove it: docker compose -p keel down; docker service rm keel-worker; docker volume rm keel_convex-data"
  fi
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
# Not fatal: without it Keel still runs, only Expose asks for it. Detected again on every run (a
# homelab's IP changes, a VPS gets rebuilt) unless KEEL_PUBLIC_IP_OVERRIDE is set.
detect_public_ip() {
  if [ -n "$KEEL_PUBLIC_IP_OVERRIDE" ]; then
    KEEL_PUBLIC_IP=$KEEL_PUBLIC_IP_OVERRIDE
    log "public IP $KEEL_PUBLIC_IP"
    return
  fi
  local url last=$KEEL_PUBLIC_IP
  for url in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    KEEL_PUBLIC_IP=$(curl -4fsS --max-time 5 "$url" 2>/dev/null | tr -d '[:space:]' || true)
    if [[ "$KEEL_PUBLIC_IP" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
      log "public IP $KEEL_PUBLIC_IP (detected; set KEEL_PUBLIC_IP to override)"
      return
    fi
  done
  KEEL_PUBLIC_IP=$last
  if [ -n "$last" ]; then
    warn "could not detect this server's public IPv4; keeping $last from the last run"
  else
    warn "could not detect this server's public IPv4; set KEEL_PUBLIC_IP and re-run to expose services"
  fi
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

  KEEL_WORKER_TOKEN=$(saved KEEL_WORKER_TOKEN); KEEL_WORKER_TOKEN=${KEEL_WORKER_TOKEN:-$(rand_hex)}
  save_state
}

save_state() {
  (
    umask 077
    cat >"$ENV_FILE.tmp" <<EOF
# Keel install state, written by install.sh. Secrets: keep this file private (0600).
# KEEL_ADDR is derived (tailnet IP) unless KEEL_ADDR_OVERRIDE is set; KEEL_PUBLIC_IP likewise
# (detected) unless KEEL_PUBLIC_IP_OVERRIDE is. KEEL_SITE_URL is derived from both KEEL_ADDR
# and KEEL_WEB_PORT.
KEEL_ADDR='$KEEL_ADDR'
KEEL_ADDR_OVERRIDE='$KEEL_ADDR_OVERRIDE'
KEEL_PUBLIC_IP='$KEEL_PUBLIC_IP'
KEEL_PUBLIC_IP_OVERRIDE='$KEEL_PUBLIC_IP_OVERRIDE'
KEEL_ACME_EMAIL='$KEEL_ACME_EMAIL'
KEEL_VERSION='$KEEL_VERSION'
KEEL_WEB_PORT='$KEEL_WEB_PORT'
KEEL_IMAGE_PREFIX='$KEEL_IMAGE_PREFIX'
KEEL_SITE_URL='$KEEL_SITE_URL'
KEEL_WORKER_TOKEN='$KEEL_WORKER_TOKEN'
EOF
    mv "$ENV_FILE.tmp" "$ENV_FILE"
  )
}

# keel-proxy joins the Swarm overlay, so Swarm and the network come before `compose up`.
start_swarm() {
  log "initialising Docker Swarm and the keel overlay network"
  TAILSCALE_IP=$KEEL_ADDR bash "$KEEL_DIR/scripts/bootstrap-swarm.sh" >&2
}

pull_images() {
  [ "$KEEL_PULL" = 1 ] || return 0
  log "pulling $KEEL_IMAGE_PREFIX/keel:$KEEL_VERSION"
  compose pull --quiet >&2 || die "could not pull the control-plane images" "check KEEL_VERSION and access to ghcr.io"
}

start_control_plane() {
  log "starting the control plane (keel serve, keel proxy)"
  compose up -d --wait --wait-timeout 180 --remove-orphans >&2 ||
    die "the control plane did not become healthy" "docker compose -p keel logs keel proxy"
}

# keel serve creates and updates keel-agent (the per-node agent, a Swarm global service) itself.
check_health() {
  log "checking health"
  local meta
  meta=$(curl -fsS "$SITE_URL/api/meta") || die "the control plane is not answering on $SITE_URL" "docker compose -p keel logs keel"
  [[ "$meta" == *"\"$SITE_URL\""* ]] || die "the control plane does not know its URL ($SITE_URL)" "docker compose -p keel logs keel"
  curl -fsS -o /dev/null "$SITE_URL/" ||
    die "the dashboard is not served on $SITE_URL" "docker compose -p keel logs keel"
  curl -fsS -o /dev/null -H @- "$SITE_URL/worker/config" <<<"Authorization: Bearer $KEEL_WORKER_TOKEN" ||
    die "the control plane does not accept the worker token" "re-run install.sh; it passes KEEL_WORKER_TOKEN to the control plane again"
  local _ replicas=""
  for _ in $(seq 1 60); do
    replicas=$(docker service ls --filter name=keel-agent --format '{{.Replicas}}' | head -1)
    [ -n "$replicas" ] && [ "${replicas%%/*}" = "${replicas##*/}" ] && [ "${replicas##*/}" != 0 ] && return 0
    sleep 2
  done
  die "keel-agent is not running (replicas: ${replicas:-none})" "docker service ps keel-agent --no-trunc"
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
  # Same for KEEL_PUBLIC_IP: an explicit one sticks, a detected one is detected again.
  KEEL_PUBLIC_IP_OVERRIDE=${KEEL_PUBLIC_IP:-$(saved KEEL_PUBLIC_IP_OVERRIDE)}
  KEEL_PUBLIC_IP=$(saved KEEL_PUBLIC_IP)
  KEEL_ACME_EMAIL=${KEEL_ACME_EMAIL:-$(saved KEEL_ACME_EMAIL)}

  preflight
  ensure_docker
  ensure_tailscale
  detect_public_ip
  SITE_URL="http://$KEEL_ADDR"; [ "$KEEL_WEB_PORT" = 80 ] || SITE_URL="$SITE_URL:$KEEL_WEB_PORT"
  # compose reads it from .env, but a KEEL_SITE_URL in the caller's environment would win there.
  KEEL_SITE_URL=$SITE_URL
  write_state
  start_swarm
  pull_images
  start_control_plane
  check_health

  printf '\n\033[1mKeel is running.\033[0m\n\n  Dashboard  %s\n  State      %s (secrets, 0600)\n\n' "$SITE_URL" "$ENV_FILE" >&2
  printf 'Open the dashboard from any device on your tailnet and sign up.\nUpgrade: re-run the install command.\n' >&2
  printf '\nExposed services are served from %s: let ports 80 and 443 (TCP) through\nits firewall or router, plus each TCP/UDP port you expose.\n' "${KEEL_PUBLIC_IP:-this server}" >&2
  if [ "${KEEL_JSON:-}" = 1 ]; then
    printf '{"ok":true,"url":%s,"apiUrl":%s,"version":%s,"stateDir":%s,"publicIp":%s,"warnings":%s}\n' \
      "$(json_str "$SITE_URL")" "$(json_str "$SITE_URL")" "$(json_str "$KEEL_VERSION")" \
      "$(json_str "$KEEL_DIR")" "$(json_str "$KEEL_PUBLIC_IP")" "$(json_warnings)"
  fi
}

# Everything runs from here, so a truncated download (curl | bash) executes nothing.
main "$@"
