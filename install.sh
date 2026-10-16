#!/usr/bin/env bash
# Keel installer. Turns a Linux server into a Keel control plane:
#   curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash
#
# Idempotent: re-running it is the upgrade. Secrets are generated once and kept in
# $KEEL_DIR/.env (0600). Progress goes to stderr; with KEEL_JSON=1, stdout carries exactly one
# JSON object ({"ok":true,...} or {"ok":false,"error":...}). Every failure exits non-zero with
# an `error:` line and, when there is one, a `fix:` line.
#
# Optional environment (all of it is non-interactive except a Tailscale login without a key, and
# turning on HTTPS for the tailnet when it is off and someone is at the terminal):
#   KEEL_TAILSCALE_AUTHKEY  tskey-auth-... to join the tailnet without a browser login
#   KEEL_ADDR               Bind to this IP instead of the tailnet IP; skips Tailscale and
#                           HTTPS (LAN, CI)
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
  order_docker_after_tailscale
}

# keel publishes on the tailnet IP only, so Docker must not restore it before tailscale0 has that
# address (a reboot used to leave it Exited, #35). The wait gets a timeout: tailscale-wait-online
# is a oneshot with none, and a tailnet that never comes up would otherwise hold Docker forever.
order_docker_after_tailscale() {
  if ! systemctl cat tailscale-online.target >/dev/null 2>&1; then
    log "no tailscale-online.target (older Tailscale or no systemd): Docker is not ordered after Tailscale"
    return
  fi
  mkdir -p /etc/systemd/system/docker.service.d /etc/systemd/system/tailscale-wait-online.service.d
  printf '[Unit]\nWants=tailscale-online.target\nAfter=tailscale-online.target\n' \
    >/etc/systemd/system/docker.service.d/10-wait-tailscale.conf
  printf '[Service]\nTimeoutStartSec=90s\n' \
    >/etc/systemd/system/tailscale-wait-online.service.d/10-keel-timeout.conf
  systemctl daemon-reload
}

# This server's MagicDNS name when the tailnet has HTTPS certificates on, else empty.
cert_domain() {
  tailscale status --json 2>/dev/null | sed -n '/"CertDomains": \[/{n;s/^[[:space:]]*"\([^"]*\)".*/\1/p;q;}'
}

# Someone at the terminal can turn HTTPS on now; an agent (KEEL_JSON=1) gets the link as a warning
# and the next run picks it up.
wait_for_https() {
  [ "${KEEL_JSON:-}" != 1 ] && [ -t 2 ] && (: </dev/tty) 2>/dev/null || return 1
  log "turn on HTTPS for your tailnet (once, for every server in it): open https://login.tailscale.com/admin/dns and select Enable HTTPS"
  log "waiting up to 15 minutes; press Enter to continue without HTTPS"
  local _
  for _ in $(seq 1 180); do
    [ -n "$(cert_domain)" ] && return 0
    read -r -t 5 _ </dev/tty && return 1
  done
  return 1
}

# The dashboard over HTTPS on this server's MagicDNS name, through `tailscale serve` (tailnet only).
# Browsers need a secure context (crypto.subtle, clipboard) and Axiom refuses http redirect URIs.
ensure_https() {
  SITE_URL="http://$KEEL_ADDR"; [ "$KEEL_WEB_PORT" = 80 ] || SITE_URL="$SITE_URL:$KEEL_WEB_PORT"
  [ -z "$KEEL_ADDR_OVERRIDE" ] || return 0
  local host
  host=$(cert_domain)
  if [ -z "$host" ] && wait_for_https; then
    host=$(cert_domain)
  fi
  if [ -z "$host" ]; then
    warn "HTTPS is off for this tailnet, so the dashboard stays on $SITE_URL and Sign in with Axiom will not work. Turn on HTTPS Certificates at https://login.tailscale.com/admin/dns, then re-run install.sh"
    return
  fi
  tailscale serve --bg --https=443 "http://$KEEL_ADDR:$KEEL_WEB_PORT" >/dev/null ||
    die "tailscale serve could not put the dashboard on https://$host" "tailscale serve status"
  SITE_URL="https://$host"
  log "dashboard on $SITE_URL (tailscale serve)"
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
  KEEL_REGISTRY_PASSWORD=$(saved KEEL_REGISTRY_PASSWORD); KEEL_REGISTRY_PASSWORD=${KEEL_REGISTRY_PASSWORD:-$(rand_hex)}
  save_state
  mkdir -p "$KEEL_DIR/registry"
  chmod 700 "$KEEL_DIR/registry"
}

save_state() {
  (
    umask 077
    cat >"$ENV_FILE.tmp" <<EOF
# Keel install state, written by install.sh. Secrets: keep this file private (0600).
# KEEL_ADDR is derived (tailnet IP) unless KEEL_ADDR_OVERRIDE is set; KEEL_PUBLIC_IP likewise
# (detected) unless KEEL_PUBLIC_IP_OVERRIDE is. KEEL_SITE_URL is derived: the MagicDNS name over
# HTTPS when the tailnet has HTTPS on, else KEEL_ADDR and KEEL_WEB_PORT.
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
KEEL_REGISTRY_PASSWORD='$KEEL_REGISTRY_PASSWORD'
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

# keel serve writes the registry's CA, certificate, password file and config into
# $KEEL_DIR/registry on start (docs/registry.md). It reissues the certificate when the tailnet IP
# changed; zot then gets a restart so it serves the new one.
start_control_plane() {
  log "starting the control plane (keel serve, keel proxy, registry)"
  local cert=$KEEL_DIR/registry/tls.crt before=""
  [ -f "$cert" ] && before=$(sha256sum "$cert")
  compose up -d --wait --wait-timeout 180 --remove-orphans >&2 ||
    die "the control plane did not become healthy" "docker compose -p keel logs keel proxy registry"
  if [ -n "$before" ] && [ "$before" != "$(sha256sum "$cert")" ]; then
    compose restart registry >&2
  fi
}

# Docker on this server trusts the registry's CA, so Swarm tasks scheduled here can pull from it.
# Other servers need the same file (docs/registry.md).
trust_registry() {
  install -D -m 644 "$KEEL_DIR/registry/ca.crt" "/etc/docker/certs.d/$KEEL_ADDR:5000/ca.crt"
}

# curl against the dashboard URL. Over HTTPS it dials the tailnet IP directly (MagicDNS may not
# resolve on this server) and retries while Tailscale fetches the first certificate.
site() {
  local path=$1 host=${SITE_URL#https://}
  shift
  if [ "$host" = "$SITE_URL" ]; then
    curl -fsS "$@" "$SITE_URL$path"
    return
  fi
  curl -fsS --resolve "$host:443:$KEEL_ADDR" --retry 18 --retry-delay 5 --retry-all-errors "$@" "$SITE_URL$path"
}

# keel serve creates and updates keel-agent (the per-node agent, a Swarm global service) itself.
check_health() {
  log "checking health"
  local meta
  meta=$(site /api/meta) || die "the control plane is not answering on $SITE_URL" "docker compose -p keel logs keel; tailscale serve status"
  [[ "$meta" == *"\"$SITE_URL\""* ]] || die "the control plane does not know its URL ($SITE_URL)" "docker compose -p keel logs keel"
  site / -o /dev/null ||
    die "the dashboard is not served on $SITE_URL" "docker compose -p keel logs keel"
  site /worker/config -o /dev/null -H @- <<<"Authorization: Bearer $KEEL_WORKER_TOKEN" ||
    die "the control plane does not accept the worker token" "re-run install.sh; it passes KEEL_WORKER_TOKEN to the control plane again"
  curl -fsS -o /dev/null --retry 10 --retry-delay 1 --retry-all-errors --cacert "$KEEL_DIR/registry/ca.crt" \
    -K - "https://$KEEL_ADDR:5000/v2/" <<<"user = \"keel:$KEEL_REGISTRY_PASSWORD\"" ||
    die "the registry is not answering on $KEEL_ADDR:5000" "docker compose -p keel logs registry"
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

  local previous_url
  previous_url=$(saved KEEL_SITE_URL)

  preflight
  ensure_docker
  ensure_tailscale
  detect_public_ip
  ensure_https
  # compose reads it from .env, but a KEEL_SITE_URL in the caller's environment would win there.
  KEEL_SITE_URL=$SITE_URL
  write_state
  start_swarm
  pull_images
  start_control_plane
  trust_registry
  check_health
  if [ -n "$previous_url" ] && [ "$previous_url" != "$SITE_URL" ]; then
    warn "the dashboard moved from $previous_url to $SITE_URL: sign in there, and run keel login $SITE_URL again wherever the CLI used the old URL"
  fi

  printf '\n\033[1mKeel is running.\033[0m\n\n  Dashboard  %s\n  Registry   %s (keel registry login)\n  State      %s (secrets, 0600)\n\n' \
    "$SITE_URL" "$KEEL_ADDR:5000" "$ENV_FILE" >&2
  printf 'Open the dashboard from any device on your tailnet and sign up.\nUpgrade: re-run the install command.\n' >&2
  printf '\nExposed services are served from %s: let ports 80 and 443 (TCP) through\nits firewall or router, plus each TCP/UDP port you expose.\n' "${KEEL_PUBLIC_IP:-this server}" >&2
  if [ "${KEEL_JSON:-}" = 1 ]; then
    printf '{"ok":true,"url":%s,"apiUrl":%s,"version":%s,"stateDir":%s,"publicIp":%s,"registry":%s,"warnings":%s}\n' \
      "$(json_str "$SITE_URL")" "$(json_str "$SITE_URL")" "$(json_str "$KEEL_VERSION")" \
      "$(json_str "$KEEL_DIR")" "$(json_str "$KEEL_PUBLIC_IP")" "$(json_str "$KEEL_ADDR:5000")" "$(json_warnings)"
  fi
}

# Everything runs from here, so a truncated download (curl | bash) executes nothing.
main "$@"
