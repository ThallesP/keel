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
#   KEEL_SKIP_CONVEX_IMPORT=1  Upgrading a Convex-era install: start without its data instead of
#                           exporting and importing it (the data stays in keel_convex-data)
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
# sourced by a shell (the Convex-era admin key contains `|`); docker compose strips the quotes too.
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
  # The Convex-era compose file: it starts the old backend to export its data (and a rollback).
  if [ -f "$KEEL_DIR/compose.yml" ] && [ ! -f "$KEEL_DIR/compose.convex.yml" ] && grep -q convex-backend "$KEEL_DIR/compose.yml"; then
    cp "$KEEL_DIR/compose.yml" "$KEEL_DIR/compose.convex.yml"
  fi
  fetch deploy/compose.yml "$KEEL_DIR/compose.yml" 644
  fetch scripts/bootstrap-swarm.sh "$KEEL_DIR/scripts/bootstrap-swarm.sh" 755
  rm -f "$KEEL_DIR/scripts/deploy-worker.sh" # Convex era; keel serve deploys the agent now

  KEEL_WORKER_TOKEN=$(saved KEEL_WORKER_TOKEN); KEEL_WORKER_TOKEN=${KEEL_WORKER_TOKEN:-$(rand_hex)}
  # Convex-era secrets: never generated any more, kept as they are when an earlier install wrote
  # them (exporting the old data needs the admin key; a rollback needs all of them).
  INSTANCE_SECRET=$(saved INSTANCE_SECRET)
  BETTER_AUTH_SECRET=$(saved BETTER_AUTH_SECRET)
  CONVEX_SELF_HOSTED_ADMIN_KEY=${CONVEX_SELF_HOSTED_ADMIN_KEY:-$(saved CONVEX_SELF_HOSTED_ADMIN_KEY)}
  save_state
}

save_state() {
  (
    umask 077
    {
      cat <<EOF
# Keel install state, written by install.sh. Secrets: keep this file private (0600).
# KEEL_ADDR is derived (tailnet IP) unless KEEL_ADDR_OVERRIDE is set; KEEL_PUBLIC_IP likewise
# (detected) unless KEEL_PUBLIC_IP_OVERRIDE is. KEEL_SITE_URL is derived from both KEEL_ADDR
# and KEEL_WEB_PORT. The lines after KEEL_WORKER_TOKEN, if any, come from a Convex-era install.
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
      # Only on installs that began on Convex (see write_state and main).
      local key
      for key in INSTANCE_SECRET BETTER_AUTH_SECRET CONVEX_SELF_HOSTED_ADMIN_KEY KEEL_CONVEX_VERSION; do
        [ -z "${!key}" ] || printf "%s='%s'\n" "$key" "${!key}"
      done
    } >"$ENV_FILE.tmp"
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

# ── Upgrading a Convex-era install ──────────────────────────────────────────────────────────
# The Convex control plane kept everything in the keel_convex-data volume. Before `keel serve`
# first starts, the old deployment is exported (old functions image, old backend) and imported
# into keel.db (`keel import-convex`, new image). If anything fails, nothing new starts. The
# volume, the snapshot and compose.convex.yml are never deleted here.
CONVEX_VOLUME=keel_convex-data
CONVEX_IMPORT_NEEDED=false # true: the Convex data is not in the database; reported every run
CONVEX_IMPORT=null         # this run's import report (JSON), for the result
SKIP_IMPORT="or re-run with KEEL_SKIP_CONVEX_IMPORT=1 to start empty (the data stays in $CONVEX_VOLUME)"

convex_not_imported() {
  CONVEX_IMPORT_NEEDED=true
  warn "the data of the Convex-based Keel (accounts, projects, services, variables) is not imported: it stays in the Docker volume $CONVEX_VOLUME, untouched, and the control plane runs without it. Deployed services keep running, but Keel does not manage them and exposed ones lose their public endpoints. To import it, before anyone signs up: docker compose -p keel --env-file $ENV_FILE -f $KEEL_DIR/compose.yml down, docker volume rm keel_keel-data, then re-run install.sh without KEEL_SKIP_CONVEX_IMPORT."
}

keel_data_dir() { docker volume inspect -f '{{.Mountpoint}}' keel_keel-data 2>/dev/null || true; }
drop_keel_db() { [ -z "$1" ] || rm -f "$1/keel.db" "$1/keel.db-wal" "$1/keel.db-shm"; }

# Convex data and no SQLite database yet (or one an interrupted import left): an upgrade that
# nothing has imported.
convex_upgrade_pending() {
  docker volume inspect "$CONVEX_VOLUME" >/dev/null 2>&1 || return 1
  [ ! -f "$KEEL_DIR/convex-import.pending" ] || return 0
  local data
  data=$(keel_data_dir)
  [ -z "$data" ] || [ ! -f "$data/keel.db" ]
}

convex_backend_up() { curl -fsS -o /dev/null --max-time 5 "http://$KEEL_ADDR:3210/version" 2>/dev/null; }

start_convex_backend() {
  if convex_backend_up; then return 0; fi
  [ -f "$KEEL_DIR/compose.convex.yml" ] ||
    die "the Convex backend is not running and its compose file is gone, so its data cannot be exported" "start it (docker start keel-backend-1), $SKIP_IMPORT"
  log "starting the Convex backend to export its data"
  docker compose -p keel --env-file "$ENV_FILE" -f "$KEEL_DIR/compose.convex.yml" up -d --wait --wait-timeout 180 backend >&2 && convex_backend_up ||
    die "the Convex backend did not start, so its data cannot be exported" "docker compose -p keel logs backend; $SKIP_IMPORT"
}

migrate_convex() {
  convex_upgrade_pending || return 0
  if [ "${KEEL_SKIP_CONVEX_IMPORT:-}" = 1 ]; then
    log "KEEL_SKIP_CONVEX_IMPORT=1: starting without the Convex-era data"
    rm -f "$KEEL_DIR/convex-import.json" # a skip after an earlier import is still reported
    rm -f "$KEEL_DIR/convex-import.pending"
    convex_not_imported
    return 0
  fi
  log "this server ran the Convex-based Keel: moving its data over before the new control plane starts"
  [ -n "$CONVEX_SELF_HOSTED_ADMIN_KEY" ] ||
    die "$ENV_FILE has no CONVEX_SELF_HOSTED_ADMIN_KEY to export the Convex-era data with; nothing new was started" "re-run with CONVEX_SELF_HOSTED_ADMIN_KEY=<the old backend's admin key>, $SKIP_IMPORT"
  local dir=$KEEL_DIR/convex-export data report=$KEEL_DIR/convex-import.json
  mkdir -p "$dir"
  chmod 700 "$dir"
  start_convex_backend

  # The old functions image has node and the convex CLI; its entrypoint only deploys.
  log "exporting it to $dir/snapshot.zip"
  rm -f "$dir/snapshot-new.zip"
  CONVEX_SELF_HOSTED_ADMIN_KEY=$CONVEX_SELF_HOSTED_ADMIN_KEY docker run --rm --network host --entrypoint node \
    -e CONVEX_SELF_HOSTED_URL="http://$KEEL_ADDR:3210" -e CONVEX_SELF_HOSTED_ADMIN_KEY -v "$dir:/export" \
    "$KEEL_IMAGE_PREFIX/keel-functions:${KEEL_CONVEX_VERSION:-latest}" \
    node_modules/convex/bin/main.js export --path /export/snapshot-new.zip >&2 && [ -s "$dir/snapshot-new.zip" ] ||
    die "exporting the Convex-era data failed; the old control plane keeps running and nothing new was started" "see the output above (docker compose -p keel logs backend) and re-run; $SKIP_IMPORT"
  mv "$dir/snapshot-new.zip" "$dir/snapshot.zip"

  log "importing it into the new control plane's database"
  docker volume create --label com.docker.compose.project=keel --label com.docker.compose.volume=keel-data keel_keel-data >/dev/null
  data=$(keel_data_dir)
  # A database an interrupted import left behind holds nothing yet: start over.
  if [ -f "$KEEL_DIR/convex-import.pending" ]; then drop_keel_db "$data"; fi
  touch "$KEEL_DIR/convex-import.pending"
  if ! docker run --rm -v keel_keel-data:/data -v "$dir:/export:ro" "$KEEL_IMAGE_PREFIX/keel:$KEEL_VERSION" \
    import-convex /export/snapshot.zip --data-dir /data >"$report.tmp"; then
    drop_keel_db "$data"
    rm -f "$KEEL_DIR/convex-import.pending" "$report.tmp"
    die "importing the Convex-era data failed; the old control plane keeps running and nothing new was started" "see the output above (the snapshot is $dir/snapshot.zip) and re-run; $SKIP_IMPORT"
  fi
  mv "$report.tmp" "$report"
  rm -f "$KEEL_DIR/convex-import.pending"
  # The report as one line, warnings always a list.
  CONVEX_IMPORT=$(sed 's/^[[:space:]]*//' "$report" | tr -d '\n')
  CONVEX_IMPORT=${CONVEX_IMPORT//\"warnings\": null/\"warnings\": []}
  if [[ "$CONVEX_IMPORT" != \{*\} ]]; then
    CONVEX_IMPORT=null
    warn "the import finished without a readable report; see $report"
  elif [[ "$CONVEX_IMPORT" != *'"warnings": []'* ]]; then
    warn "the import skipped some records; see $report"
  fi
}

# Later runs: the database exists, but while nobody has an account in it (sign-up is still
# open), the Convex data is not in it. Says so on every run.
recheck_convex_data() {
  [ "$CONVEX_IMPORT_NEEDED" = false ] && [ "$CONVEX_IMPORT" = null ] || return 0
  # An import already ran into this database: an empty one (nobody had signed up) leaves sign-up open.
  [ ! -f "$KEEL_DIR/convex-import.json" ] || return 0
  docker volume inspect "$CONVEX_VOLUME" >/dev/null 2>&1 || return 0
  if curl -fsS "$SITE_URL/api/auth/sign-up-open" 2>/dev/null | grep -E '"open"[[:space:]]*:[[:space:]]*true' >/dev/null; then
    convex_not_imported
  fi
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
  { curl -fsS -o /dev/null "$SITE_URL/" && curl -fsS -o /dev/null "$SITE_URL/config.js"; } ||
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

# The Convex-era per-node worker reported to Convex, which is gone; keel-agent replaces it. Its
# state volume (keel-worker-state) stays.
retire_worker() {
  docker service inspect keel-worker >/dev/null 2>&1 || return 0
  log "removing the Convex-era keel-worker service (keel-agent replaces it)"
  docker service rm keel-worker >/dev/null || { warn "could not remove the keel-worker service; remove it with: docker service rm keel-worker"; return 0; }
  docker secret ls -q --filter name=keel-worker-token- | xargs -r docker secret rm >/dev/null 2>&1 || true
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
  # The image tag of a Convex-era install (its state file has no KEEL_SITE_URL): which old
  # functions image exports its data. Remembered, since KEEL_VERSION moves on.
  KEEL_CONVEX_VERSION=$(saved KEEL_CONVEX_VERSION)
  if [ -z "$KEEL_CONVEX_VERSION" ] && [ -f "$ENV_FILE" ] && ! grep -q '^KEEL_SITE_URL=' "$ENV_FILE"; then
    KEEL_CONVEX_VERSION=$(saved KEEL_VERSION)
    KEEL_CONVEX_VERSION=${KEEL_CONVEX_VERSION:-latest}
  fi

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
  migrate_convex
  start_control_plane
  check_health
  retire_worker
  recheck_convex_data

  printf '\n\033[1mKeel is running.\033[0m\n\n  Dashboard  %s\n  State      %s (secrets, 0600)\n\n' "$SITE_URL" "$ENV_FILE" >&2
  if [ "$CONVEX_IMPORT_NEEDED" = true ]; then
    printf '\033[1mThe data of the Convex-based Keel is not imported:\033[0m it stays in the Docker volume\n%s (see the warning above). Keep that volume.\n' "$CONVEX_VOLUME" >&2
  elif [ "$CONVEX_IMPORT" != null ]; then
    # The report's users count: none means the old install had no account yet, so sign up.
    next_step='Sign in to the dashboard again; CLI logins keep working.'
    if [[ "$CONVEX_IMPORT" != *'"users":'* ]] || [[ "$CONVEX_IMPORT" == *'"users": 0'* ]]; then
      next_step='It had no account yet: open the dashboard and sign up.'
    fi
    printf '\033[1mThe data of the Convex-based Keel is imported\033[0m (report: %s).\n%s Once the dashboard shows\neverything, remove what the old control plane left behind:\n  sudo rm -r %s %s\n  sudo docker volume rm %s\n' \
      "$KEEL_DIR/convex-import.json" "$next_step" "$KEEL_DIR/convex-export" "$KEEL_DIR/compose.convex.yml" "$CONVEX_VOLUME" >&2
  else
    printf 'Open the dashboard from any device on your tailnet and sign up.\n' >&2
  fi
  printf 'Upgrade: re-run the install command.\n' >&2
  printf '\nExposed services are served from %s: let ports 80 and 443 (TCP) through\nits firewall or router, plus each TCP/UDP port you expose.\n' "${KEEL_PUBLIC_IP:-this server}" >&2
  if [ "${KEEL_JSON:-}" = 1 ]; then
    # convexUrl and convexSiteUrl predate the single control plane; kept (fields are only added),
    # both equal to url now.
    # convexImport: what this run imported from a Convex-era install (keel import-convex's
    # report: imported and skipped per table, warnings), else null.
    printf '{"ok":true,"url":%s,"apiUrl":%s,"convexUrl":%s,"convexSiteUrl":%s,"version":%s,"stateDir":%s,"publicIp":%s,"convexImportNeeded":%s,"convexImport":%s,"warnings":%s}\n' \
      "$(json_str "$SITE_URL")" "$(json_str "$SITE_URL")" "$(json_str "$SITE_URL")" "$(json_str "$SITE_URL")" \
      "$(json_str "$KEEL_VERSION")" "$(json_str "$KEEL_DIR")" "$(json_str "$KEEL_PUBLIC_IP")" \
      "$CONVEX_IMPORT_NEEDED" "$CONVEX_IMPORT" "$(json_warnings)"
  fi
}

# Everything runs from here, so a truncated download (curl | bash) executes nothing.
main "$@"
