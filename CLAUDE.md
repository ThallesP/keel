# Keel

Working name, not final ("OpenShip" was taken).

Self-hosted deploy platform. Alternative to Coolify, Dokploy and OpenDeploy.
Differentiator: UI/UX and deploy DX.

## Principles

- **Canvas first.** Railway-inspired. Projects are a canvas by default.
- **Multi-server by default.** Scaling past one machine must be effortless. Single-node clusters stay first-class.
- **Zero networking for the user.** We handle it:
  - Tailscale for user containers and control-plane/worker comms.
  - Public HTTP via Tailscale Funnel, one tsnet node per service (`<name>.<tailnet>.ts.net`). Custom domains later via Cloudflare Tunnel. See `docs/networking.md`.

## Design docs

- `docs/canvas.md` — v1 canvas UI spec (layout, nodes, edges, bottom panel, ship flow, tokens, React Flow mapping, where to start). Read before any UI work.
- `docs/agents.md` — agents-first layer on top of v1. Do not start before canvas v1 works.
- `docs/workers.md` — worker layer: Docker Swarm as reconciler, Convex as control plane, node join flow, schema, apply/observe actions. Read before any Swarm or node-join code.
- `docs/volumes.md` — persistent data: pin + backup, no distributed storage, two-pass rsync migration between nodes, backup by kind, schema. Read before anything that mounts a volume, schedules a backup, or moves a service between servers.
- `docs/logs.md` — log sinks and providers: per-project sink (Axiom today, Docker default), worker ships lines, `logs.tail` dispatches on provider, event shape contract, how to add ClickHouse. Read before touching `convex/logs*`, `convex/logProviders`, or `apps/worker/src/logs.ts`.
- `docs/networking.md` — Tailscale mesh, per-service Funnel ingress container (own Go proxy on tsnet), tailnet onboarding steps, zero-inbound-port invariant, why not Cloudflare/NetBird/Caddy/Traefik. Read before any ingress, domain, or auth code.
- Mockups: Paper file "*Ship*".
