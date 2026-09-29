#!/bin/sh
# Runs from nginx's /docker-entrypoint.d before nginx starts. Writes the runtime config the
# SPA reads in src/lib/config.ts.
set -eu

: "${KEEL_CONVEX_URL:?KEEL_CONVEX_URL is required (Convex API origin, e.g. http://100.64.0.1:3210)}"
: "${KEEL_CONVEX_SITE_URL:?KEEL_CONVEX_SITE_URL is required (Convex HTTP actions origin, e.g. http://100.64.0.1:3211)}"

for v in "$KEEL_CONVEX_URL" "$KEEL_CONVEX_SITE_URL"; do
  case "$v" in
    http://* | https://*) ;;
    *) echo "40-keel-config: not an http(s) URL: $v" >&2; exit 1 ;;
  esac
  case "$v" in *[\"\\\<\>\ ]*) echo "40-keel-config: invalid character in URL: $v" >&2; exit 1 ;; esac
done

printf 'window.__KEEL__ = {"convexUrl":"%s","convexSiteUrl":"%s"};\n' \
  "$KEEL_CONVEX_URL" "$KEEL_CONVEX_SITE_URL" > /usr/share/nginx/html/config.js
