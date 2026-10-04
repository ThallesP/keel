# Keel

Working name, not final ("OpenShip" was taken).

Self-hosted deploy platform. Alternative to Coolify, Dokploy and OpenDeploy.
Differentiator: UI/UX and deploy DX.

## Principles

- **Canvas first.** Railway-inspired. Projects are a canvas by default.
- **Multi-server by default.** Scaling past one machine must be effortless. Single-node clusters stay first-class.
- **Zero networking for the user.** We handle it:
  - Tailscale for user containers and control-plane/worker comms.
  - Public HTTP via Cloudflare Tunnel: a Quick Tunnel per exposed service today (temporary `trycloudflare.com` URL, zero setup), a named tunnel with the user's own domain next. Tailscale Funnel was rejected 2026-10-01. See `docs/networking.md`.

## Install and release

- `install.sh` is the product's front door: one idempotent command, re-run = upgrade, `KEEL_JSON=1` for agents. Its contract (options, output, verification) is documented in `README.md`; keep the two in sync.
- Control plane = `deploy/compose.yml` (self-hosted Convex + web). Images `ghcr.io/thallesp/keel-{web,functions,worker}` come from `.github/workflows/images.yml`. `ci.yml` runs `install.sh` end to end; anything that changes install behaviour must keep it green.
- Web gets its Convex URLs at runtime (`/config.js`, `apps/web/src/lib/config.ts`). Never bake deployment URLs into the web build.

## CLI

- `apps/cli` is `keel`, a Go CLI for agents first (Railway CLI is the benchmark). Thin client over the public Convex functions over Convex's HTTP API; it finds an install through the dashboard's `/config.js`, so keep that file's shape.
- Its output contract (JSON envelope like `install.sh`, error codes, exit codes) is in `apps/cli/README.md`. Fields and codes are only ever added.
- It maps Convex errors to codes by their `ConvexError` message (`translate` in `internal/keel/api.go`); rewording one of those messages means updating it there.

## Design docs

- `docs/canvas.md` — v1 canvas UI spec (layout, nodes, edges, bottom panel, ship flow, tokens, React Flow mapping, where to start). Read before any UI work.
- `docs/agents.md` — agents-first layer on top of v1. Do not start before canvas v1 works.
- `docs/workers.md` — worker layer: Docker Swarm as reconciler, Convex as control plane, node join flow, schema, apply/observe actions. Read before any Swarm or node-join code.
- `docs/volumes.md` — persistent data: pin + backup, no distributed storage, two-pass rsync migration between nodes, backup by kind, schema. Read before anything that mounts a volume, schedules a backup, or moves a service between servers.
- `docs/logs.md` — log sinks and providers: per-organization sink (Axiom today, Docker default), worker ships lines, `logs.tail` dispatches on provider, event shape contract, how to add ClickHouse; OpenTelemetry traces in the sink's second dataset (read side, root span = request), joined to log lines by the trace id the line names. Read before touching `convex/logs*`, `convex/traces.ts`, `convex/logProviders`, `convex/traceProviders`, `components/canvas/observability`, or `apps/worker/src/logs.ts`.
- `docs/networking.md` — Tailscale mesh, public ingress via Cloudflare Tunnel (Quick Tunnel per service now, named tunnel next, why Funnel was dropped), zero-inbound-port invariant, why not NetBird/Caddy/Traefik. Read before any ingress, domain, or auth code.
- Mockups: Paper file "_Ship_".
