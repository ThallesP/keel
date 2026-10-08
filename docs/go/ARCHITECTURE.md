# Keel in Go — architecture contract

One Go module at the repo root (`github.com/ThallesP/keel`), one binary `keel`. This file is the
contract every package follows. The porting specs it implements live in `docs/go/spec/`.

## The binary

| Command                  | Runs where                                          | What it is                                                                                   |
| ------------------------ | --------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `keel serve`             | control plane, container `keel` (compose)           | HTTP API + WebSocket + embedded dashboard + SQLite + Swarm driver (Docker socket) + jobs     |
| `keel proxy`             | control plane, container `proxy` (compose)          | Embedded Caddy + caddy-l4 + the keel plugin (was `apps/proxy`). No Docker socket, ever.      |
| `keel agent`             | every Swarm node, global service `keel-agent`       | Docker events forwarder + log shipper (was `apps/worker`). Read-only Docker socket.          |
| `keel openapi`           | build time                                          | Prints the OpenAPI 3.1 document (`openapi.json` at the repo root is its committed output).   |
| `keel login`, `ship`, …  | anyone's laptop / agent                             | The CLI (was `apps/cli`), contract unchanged: `apps/cli/README.md` → `docs/cli.md`.          |

`serve` and `proxy` are the same binary in two containers on purpose: the process that parses
internet traffic never holds the Docker socket (root on the host). Do not merge them.

## Layers and the dependency rule

```
internal/
  domain/        entities, value objects, pure rules. Imports nothing from this repo. No I/O.
  app/           use cases. Imports domain. Defines the ports (interfaces) it needs.
  adapters/      implementations of app ports: sqlite, swarm (Docker SDK), caddy (proxy admin
                 client), axiom, realtime (centrifuge), jobs (in-memory scheduler).
  transport/http Huma handlers + raw routes (worker, otlp, ws, static). Calls app. Never touches
                 adapters directly.
  api/           wire types (request/response bodies, error codes). Shared by transport/http and
                 the CLI client. Imports nothing but stdlib.
  serve/         wiring for `keel serve`: config from env, construct adapters, inject into app.
  agent/         `keel agent` (node side). Talks to serve over HTTP only.
  proxy/         `keel proxy`: embedded Caddy + plugin modules.
  cli/           CLI commands, config, output, HTTP client (uses api/).
cmd/keel/        main.go → cli.Execute()
```

- `domain` never imports `app`, `adapters`, `transport`, `api`.
- `app` never imports `adapters`, `transport`, `api`, `net/http`, `database/sql`, Docker SDK.
- Only `adapters/sqlite` imports a SQL driver. Only `adapters/swarm` and `agent` import the Docker SDK.
- Interfaces are defined by the consumer (in `app`), small, one file per area.

## Data

- SQLite, pure Go driver `modernc.org/sqlite`, WAL, `foreign_keys=ON`, `busy_timeout=5000`.
  One write connection (writes serialize, `BEGIN IMMEDIATE`), a read pool. File: `$KEEL_DATA_DIR/keel.db`.
- Schema: `internal/adapters/sqlite/migrations/NNNN_name.sql`, applied in order at start, tracked
  in `schema_migrations`. Additive only once released.
- Queries: `sqlc` (`internal/adapters/sqlite/sqlc.yaml`), one `queries/<area>.sql` per area,
  generated into `internal/adapters/sqlite/db/`. Run `sqlc generate` from `internal/adapters/sqlite`.
- IDs: `domain.NewID()`: 20 random lowercase base32 chars. Strings everywhere.
- Times: unix milliseconds (`int64`) in the database, the domain and the JSON API (the dashboard
  compares them with `Date.now()`). The CLI prints RFC 3339.
- Nested Convex objects become columns (`desired_image`, `observed_state`, …) or child tables
  (`endpoints`, `deployment_steps`, `deployment_log`). JSON columns only for small free-form
  lists (`observed_node_ids`).

## Use cases

```go
// app.App holds every port. One file per area: app/projects.go, app/nodes.go, …
func (a *App) RenameNode(ctx context.Context, actor domain.Actor, id string, name string) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)          // access rule, returns domain errors
		if err != nil { return err }
		...
		ch.Environment(scope.Org, scope.Environment.ID)   // what the dashboard must refetch
		return nil
	})
}
```

- `a.read` / `a.write` wrap `Store.Read` / `Store.Write`. `write` publishes the collected
  `Changes` after commit (a crash in between loses only an invalidation; clients refetch
  everything on reconnect).
- `Tx` is the composite of per-area interfaces (`ProjectsTx`, `NodesTx`, …) declared in
  `app/ports_<area>.go` and implemented in `adapters/sqlite/<area>.go`.
- Long work (Docker, Axiom, Caddy) happens outside the transaction: read, release, call out,
  write the outcome. Never hold the write lock across a network call.
- Background work (Convex `scheduler.runAfter`, crons) goes through the `Jobs` port:
  `After(key, delay, fn)` coalesces by key (a pending job with the same key wins), `Every(name,
  interval, fn)`. On start, `serve` runs the recovery pass: observe every service, observe Swarm
  nodes, proxy sync, data migrations. That replaces durable scheduling until workflows land.

## Errors

`domain.Error{Code, Message}`. `Code` is the CLI's code vocabulary (`api.Code*`), `Message` is the
human sentence (kept identical to the Convex `ConvexError` messages). Transport maps codes to
HTTP status and writes RFC 9457 `application/problem+json` with the extra field `code`:

| Code                                                                                       | Status |
| ------------------------------------------------------------------------------------------ | ------ |
| `NOT_AUTHENTICATED`                                                                        | 401    |
| `NO_ORGANIZATION`, `FORBIDDEN`                                                             | 403    |
| `NOT_FOUND`, `PROJECT_NOT_FOUND`, `SERVICE_NOT_FOUND`, `VARIABLE_NOT_FOUND`, `DEPLOYMENT_NOT_FOUND` | 404    |
| `NAME_TAKEN`, `NOTHING_TO_SHIP`, `DEPLOYMENT_RUNNING`, `CONFLICT`, `TRACES_OFF`            | 409    |
| `AUTHORIZATION_PENDING`                                                                    | 428    |
| `INVALID_INPUT`                                                                            | 422    |
| `UNAVAILABLE`                                                                              | 503    |
| `SERVER_ERROR` (anything else; message is generic, the cause is logged)                    | 500    |

## HTTP API

- Huma v2 on `net/http` (`humago`), everything JSON under `/api`. OpenAPI at `/api/openapi.json`.
- `operationId` is camelCase verb+noun (`listProjects`, `createService`, `shipEnvironment`): the
  dashboard's hooks are generated from it (`useListProjects`). Tags = area.
- Auth: cookie `keel_session` (dashboard, HttpOnly, SameSite=Lax, Secure on https) or
  `Authorization: Bearer <session token>` (CLI). Middleware resolves `domain.Actor`; handlers
  pass it to `app`. Same origin for dashboard, API and WebSocket: no CORS.
- Raw (non-Huma) routes, paths unchanged from Convex so deployed workers and services keep
  working across the switch: `POST /worker/events`, `GET /worker/config`, `POST /proxy/events`
  (bearer `KEEL_WORKER_TOKEN`), `POST /otlp/v1/traces` (environment ingest key).
- `GET /config.js` stays (runtime config for the dashboard); `GET /api/meta` is what the CLI
  discovers an install with.

## Realtime

- One WebSocket per tab: `GET /api/ws`, centrifuge server, `centrifuge` JS client.
- Connect auth = the same session. The server subscribes the connection to `org:<organizationId>`
  (server-side subscription); a client never picks channels.
- Publications: `{"type":"invalidate","topics":["/api/environments/<id>", "/api/nodes/<id>", …]}`.
  A topic is a URL path prefix. The dashboard invalidates every TanStack query whose key (the
  request path, see below) starts with a topic. Reconnect = invalidate everything.
- `Changes` helpers name the topics so handlers never spell paths: `ch.Projects(org)`,
  `ch.Environment(org, envID)` (canvas, summary, deployments of it), `ch.Node(org, nodeID)`,
  `ch.Organization(org)` (members, invitations, sink).

## Dashboard

- `apps/web` stays a Vite + React app. `kubb.config.ts` reads `../../openapi.json` and generates
  `src/api/gen/` (types, zod schemas, fetch client, TanStack Query hooks).
- Query keys: the first element is the resolved request path (`/api/environments/abc/canvas`),
  so invalidation topics match by prefix.
- `src/lib/realtime.tsx`: the WebSocket provider. Components only call generated hooks.
- Production: `apps/web/embed.go` embeds `dist/`; `keel serve` serves it with an SPA fallback.
  Dev: Vite proxies `/api`, `/worker`, `/otlp`, `/config.js` to `keel serve`.

## Env (serve)

| Var                                   | Meaning                                                                        |
| ------------------------------------- | ------------------------------------------------------------------------------ |
| `KEEL_LISTEN` (`:8080`)               | HTTP listen address (compose publishes it on the tailnet IP)                   |
| `KEEL_DATA_DIR` (`/data`)             | SQLite file and other state                                                    |
| `KEEL_SITE_URL`                       | Dashboard URL as users open it (device-login links, Secure cookies on https)   |
| `KEEL_WORKER_TOKEN`                   | Bearer for agent and proxy routes                                              |
| `KEEL_PUBLIC_IP`, `KEEL_ACME_CA`, `KEEL_ACME_EMAIL` | Ingress, as before (docs/networking.md)                          |
| `KEEL_OTLP_URL`                       | OTLP relay URL injected into traced services                                   |
| `KEEL_PROXY_ADMIN` (`/run/keel-proxy/admin.sock`) | keel-proxy admin socket                                            |
| `DOCKER_HOST`                         | Docker socket (default `unix:///var/run/docker.sock`)                          |
| `KEEL_AXIOM_AUTH_URL`, `KEEL_AXIOM_API_URL`, `KEEL_ALLOW_LOCAL_SINKS` | Axiom overrides for tests, as before            |

## Testing

- `domain`: plain table tests. `app`: against the real SQLite adapter on a temp file (fast, no
  mocks) plus hand-written fakes for Swarm/Proxy/Axiom ports. Adapters: real SQLite; Swarm against
  a Docker-in-Docker Swarm in CI, never the dev box's shared Swarm.
- `go vet ./...`, `gofmt`, `go test ./...` must pass. `bun run check-types` + `bun run build` in
  `apps/web` must pass.
