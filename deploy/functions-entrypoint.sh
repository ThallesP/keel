#!/bin/sh
# Entry point of the keel-functions image.
#   check   exit 0 iff CONVEX_SELF_HOSTED_ADMIN_KEY is accepted by the backend
#   deploy  set the deployment env (SITE_URL, BETTER_AUTH_SECRET, KEEL_WORKER_TOKEN), then push
#           the functions. Idempotent; install.sh runs it on every install and upgrade.
set -eu

: "${CONVEX_SELF_HOSTED_URL:?CONVEX_SELF_HOSTED_URL is required}"
: "${CONVEX_SELF_HOSTED_ADMIN_KEY:?CONVEX_SELF_HOSTED_ADMIN_KEY is required}"
# The CLI refuses to run with cloud and self-hosted settings at once.
unset CONVEX_DEPLOYMENT CONVEX_DEPLOY_KEY 2>/dev/null || true

convex() { node node_modules/convex/bin/main.js "$@"; }

case "${1:-deploy}" in
  check)
    convex env list >/dev/null
    ;;
  deploy)
    for name in SITE_URL BETTER_AUTH_SECRET KEEL_WORKER_TOKEN; do
      eval "value=\${$name:-}"
      [ -n "$value" ] || { echo "keel-functions: $name is required" >&2; exit 1; }
    done
    # Values go through a 0600 file, never argv.
    envfile=$(mktemp)
    trap 'rm -f "$envfile"' EXIT
    printf 'SITE_URL=%s\nBETTER_AUTH_SECRET=%s\nKEEL_WORKER_TOKEN=%s\n' \
      "$SITE_URL" "$BETTER_AUTH_SECRET" "$KEEL_WORKER_TOKEN" > "$envfile"
    convex env set --from-file "$envfile" --force
    # _generated is committed; typechecking belongs to CI, not to every install.
    convex deploy --typecheck disable --codegen disable
    ;;
  *)
    echo "usage: keel-functions [deploy|check]" >&2
    exit 2
    ;;
esac
