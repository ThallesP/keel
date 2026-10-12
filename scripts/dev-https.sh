#!/usr/bin/env bash
# Dev: serve this box over HTTPS at its MagicDNS name, e.g. https://dev.<tailnet>.ts.net.
# Tailnet-only (`tailscale serve`, not funnel). Idempotent: re-run after a node rename or on a
# new box; the serve config itself survives reboots. See docs/networking.md, "Dashboard over HTTPS".
#
#   https://<node>  -> Vite (127.0.0.1:3001), which proxies /api, /worker, /otlp and /proxy to
#                      `keel serve` (127.0.0.1:3400)
#
# One origin for the dashboard and the API, so nothing else needs a certificate. `keel serve` has
# to know the URL people open (device-login links, Secure cookies): start it with the
# KEEL_SITE_URL this script prints.
set -euo pipefail

status=$(tailscale status --json)
host=$(jq -r '.Self.DNSName | rtrimstr(".")' <<<"$status")
jq -e '.Self.CapMap | has("https")' <<<"$status" >/dev/null || {
  echo "HTTPS certificates are off for this tailnet. Enable them at https://login.tailscale.com/admin/dns" >&2
  exit 1
}

sudo tailscale serve --bg --https=443 http://127.0.0.1:3001

echo "https://$host"
echo "Start keel serve with KEEL_SITE_URL=https://$host" >&2
