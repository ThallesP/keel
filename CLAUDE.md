# Keel

Working name, not final ("OpenShip" was taken).

Self-hosted deploy platform. Alternative to Coolify, Dokploy and OpenDeploy.
Differentiator: UI/UX and deploy DX.

## Principles

- **Canvas first.** Railway-inspired. Projects are a canvas by default.
- **Multi-server by default.** Scaling past one machine must be effortless. Single-node clusters stay first-class.
- **Almost zero networking for the user.** We handle it, except opening ports on the control plane:
  - Tailscale for user containers and control-plane/worker comms.
  - Public traffic via `keel-proxy` (Caddy + caddy-l4, `apps/proxy`) on the control plane only, never a per-worker proxy: HTTPS on 80/443 with automatic certificates (sslip.io default domain or the user's own), raw TCP on any port but 80/443 and UDP on any port, all configured by Convex through the admin API. The user opens 80/443 and the TCP/UDP ports they expose. Cloudflare Quick Tunnel was dropped 2026-10-06, Tailscale Funnel 2026-10-01. See `docs/networking.md`.

## Code rules

Each rule names what enforces it. When an agent gets corrected for something new, add it here at the strongest level that works: impossible in code, then a check, then a test, then this table (the `/correct` skill).

| Rule                                                                                                                                                                                                                                                                    | Enforced by                                                                                       |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| No comments in Go or TypeScript. Say it with a name, a type or a test; design reasons go in `docs/` or the commit message. Directives (`//go:`, `//nolint`, `@ts-expect-error`, lint-disable) are exempt. Existing comments are debt to remove.                         | `scripts/check-comments.sh` in CI: per-file counts in `.comments-baseline` only go down           |
| Destructure objects: `const { sink } = data`, not `const sink = data.sink`.                                                                                                                                                                                             | oxlint `prefer-destructuring`                                                                     |
| Effects only sync with outside systems. Derive values during render, reset state by remounting, change state in event handlers.                                                                                                                                         | oxlint `react/set-state-in-effect`, `react/no-deriving-state-in-effects`, `react/exhaustive-deps` |
| No `??` or `?.` on a value that can't be missing. Make the type true at the source instead: the API never sends `null` for an array, and index access is `T \| undefined` (`noUncheckedIndexedAccess`).                                                                 | oxlint `typescript/no-unnecessary-condition` (`--type-aware`)                                     |
| Read the fields a component needs with `select` and destructure: `const { data: ip } = useGetControlPlane({ query: { select: (c) => c.publicIp } })`, not `useGetControlPlane().data?.publicIp`.                                                                        | review                                                                                            |
| Forms are native `<form>`s. Inputs carry their constraints (`type`, `min`, `max`, `required`), `formValues` plus the generated zod schema build the body, and the generated mutation hook's `isPending` is the busy state. No hand-written parse helpers or busy flags. | review                                                                                            |
| No nested ternaries, and no ternary where a lookup table typed by the union says it (`stateTone[e.state]`, `firstProtocol[node.type]`).                                                                                                                                 | oxlint `no-nested-ternary`                                                                        |
| No `in` checks. Narrow with a discriminant field, `instanceof`, or a type that already says which shape it is.                                                                                                                                                          | oxlint `keel/no-in` (`tools/oxlint/keel.js`)                                                      |
| Data a view can't render without comes from the generated `…Suspense` hook and is destructured (`const { data: { variables } } = useListVariablesSuspense(…)`); the panel's `Suspense` and `ErrorBoundary` own loading and failure.                                     | review                                                                                            |
| Domain rules live in Go, not in components: the API returns what the UI shows (`suggestions`, a key's suggested name `as`) instead of the dashboard rebuilding it from lists with Sets and `flatMap`s.                                                                  | review                                                                                            |
| A module-level constant earns its place: used in two or more places, or a regex or table built once. A value used once goes inline, where its key already names it (`refetchInterval: 3_000`).                                                                          | review                                                                                            |
| A file exports one component. A small component used only by that file may stay next to it; anything else gets its own file.                                                                                                                                            | review                                                                                            |
| Generated code is never edited and lives apart: `apps/web/src/gen` (Kubb's `api/`, TanStack Router's `route-tree.ts`, Varlock's `env.ts`; gitignored) and `internal/gen` (sqlc's `sqlc/`, `Code generated` header).                                                     | CI `openapi.json is current`; `bun run api:generate` on dev, build, check-types                   |

## Install and release

- `install.sh` is the product's front door: one idempotent command, re-run = upgrade, `KEEL_JSON=1` for agents. Its contract (options, output, verification) is documented in `README.md`; keep the two in sync.
- One binary, `keel` (`cmd/keel`, `internal/`, `docs/go/ARCHITECTURE.md`), one image `ghcr.io/thallesp/keel` (root `Dockerfile`, built by `.github/workflows/images.yml`). Control plane = `deploy/compose.yml`: `keel serve` (API, embedded dashboard, SQLite in the `keel-data` volume, Swarm driver; manages the `keel-agent` global service itself) and `keel proxy` (the public edge, its own container, never the Docker socket), both from that image. `ci.yml` runs Go and web checks and `install.sh` end to end; anything that changes install behaviour must keep it green.
- Upgrading a Convex-era install: before `keel serve` first starts, `install.sh` exports the old deployment (old `keel-functions` image, old backend) and imports it with `keel import-convex`; any failure stops it before the new services start (`KEEL_SKIP_CONVEX_IMPORT=1` starts empty knowingly). It never deletes `keel_convex-data`, the snapshot, `compose.convex.yml` or the Convex secrets in `.env`.
- The dashboard and the API share one origin; `keel serve` serves `/config.js` (runtime config). Never bake deployment URLs into the web build.

## CLI

- The CLI is the same binary (`internal/cli`: one file per noun; `internal/cli/client` is its HTTP client), for agents first (Railway CLI is the benchmark). Thin client over Keel's HTTP API (`/api`, `openapi.json`, session token as bearer, device login for `keel login`); it finds an install through the dashboard URL (`/api/meta`), so keep that response's shape.
- Its output contract (JSON envelope like `install.sh`, error codes, exit codes) is in `docs/cli.md`. Fields and codes are only ever added.
- Error codes come from the server: every API error is `application/problem+json` with a `code` from the CLI's vocabulary (`domain.Code*`); the CLI uses it as is and only computes the `fix` (`withFix` in `internal/cli/client/client.go`). A new domain code is a new CLI code: add it to `internal/cli/output` and `docs/cli.md`. Messages stay the Convex-era strings verbatim.

## Design docs

- `docs/canvas.md` — v1 canvas UI spec (layout, nodes, edges, bottom panel, ship flow, tokens, React Flow mapping, where to start). Read before any UI work.
- `docs/agents.md` — agents-first layer on top of v1. Do not start before canvas v1 works.
- `docs/workers.md` — worker layer: Docker Swarm as reconciler, `keel serve` as control plane, the `keel-agent` global service on every node, node join flow, schema, apply/observe jobs. Read before any Swarm or node-join code (`internal/app/{swarm,observe,reconcile,events}.go`, `internal/adapters/swarm`, `internal/agent`).
- `docs/volumes.md` — persistent data: pin + backup, no distributed storage, two-pass rsync migration between nodes, backup by kind, schema. Read before anything that mounts a volume, schedules a backup, or moves a service between servers.
- `docs/logs.md` — log sinks and providers: per-organization sink (Axiom today, Docker default), the agent ships lines, `TailNodeLogs` dispatches on provider, event shape contract, how to add ClickHouse; OpenTelemetry traces in the sink's second dataset (root span = request), joined to log lines by the trace id the line names; spans come in through Keel's OTLP relay from services with tracing on and from `keel run`, and apps get instrumented by pasting Keel's agent prompt, not by a Keel SDK. Read before touching `internal/app/{logs,logs_docker,sinks,axiom,traces,traces_parse,otlp,tracing,tracing_prompt,observability}.go`, `internal/adapters/axiom`, `components/canvas/observability`, or `internal/agent/{logs,sink,axiom}.go`.
- `docs/networking.md` — Tailscale mesh, public ingress via keel-proxy (`keel proxy`, `internal/proxy`; why Caddy + caddy-l4 over Traefik/HAProxy/Envoy, host-namespace listeners from an overlay container, endpoints, sync, certificate reports), why Cloudflare Tunnel and Funnel were dropped. Read before any ingress, domain, or auth code.
- Mockups: Paper file "_Ship_".
