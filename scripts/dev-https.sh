#!/usr/bin/env bash
# Dev: serve this box over HTTPS at its MagicDNS name, e.g. https://dev.<tailnet>.ts.net.
# Tailnet-only (`tailscale serve`, not funnel). Idempotent: re-run after a node rename or on a
# new box; the serve config itself survives reboots. See docs/networking.md, "Dashboard over HTTPS".
#
#   https://<node>          -> web (vite, 127.0.0.1:3001)
#   https://<node>:8443     -> Convex API + sync websocket (127.0.0.1:3210)
#   https://<node>:10000    -> Convex HTTP actions, i.e. auth (127.0.0.1:3211)
#
# Convex has to be https too, or the browser blocks it as mixed content. The ports are the three
# Funnel allows, so going public later is `tailscale funnel` on the same ports, same URLs.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
command -v bunx >/dev/null || PATH="$HOME/.bun/bin:$PATH"

status=$(tailscale status --json)
host=$(jq -r '.Self.DNSName | rtrimstr(".")' <<<"$status")
jq -e '.Self.CapMap | has("https")' <<<"$status" >/dev/null || {
  echo "HTTPS certificates are off for this tailnet. Enable them at https://login.tailscale.com/admin/dns" >&2
  exit 1
}

sudo tailscale serve --bg --https=443 http://127.0.0.1:3001
sudo tailscale serve --bg --https=8443 http://127.0.0.1:3210
sudo tailscale serve --bg --https=10000 http://127.0.0.1:3211

env=apps/web/.env
touch "$env"
sed -i '/^VITE_CONVEX_URL=/d; /^VITE_CONVEX_SITE_URL=/d' "$env"
printf 'VITE_CONVEX_URL=https://%s:8443\nVITE_CONVEX_SITE_URL=https://%s:10000\n' "$host" "$host" >>"$env"

# better-auth trusts exactly one origin (convex/auth.ts).
(cd packages/backend && bunx convex env set SITE_URL "https://$host")

echo "https://$host"
