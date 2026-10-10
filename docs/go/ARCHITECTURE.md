# Keel in Go — architecture contract

One Go module at the repo root (`github.com/ThallesP/keel`), one binary `keel`. This file is the
contract every package follows. The porting specs it implements live in `docs/go/spec/`.

## The binary

| Command                  | Runs where                                          | What it is                                                                                   |
| ------------------------ | --------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `keel serve`             | control plane, container `keel` (compose)           | HTTP API + WebSocket + embedded dashboard + SQLite + Swarm driver (Docker socket) + jobs     |
| `keel proxy`             | control plane, container `proxy` (compose)          | Embedded Caddy + caddy-l4 + the keel plugin (was `apps/proxy`). No Docker socket, ever. Linux builds without `-tags keel_noproxy` only. |
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
                 client), axiom, realtime (centrifuge), jobs (in-memory scheduler), password
                 (argon2id).
  transport/http Huma handlers + raw routes (worker, otlp, ws, static). Calls app. Never touches
                 adapters directly.
  api/           wire types (request/response bodies, the problem body). Shared by transport/http
                 and the CLI client. Imports stdlib and domain (enums and values sent as is).
  gen/sqlc/      sqlc output. Never edited.
  serve/         wiring for `keel serve`: config from env, construct adapters, inject into app.
  agent/         `keel agent` (node side). Talks to serve over HTTP only.
  mesh/          the agent's optional embedded Tailscale node (tsnet, `KEEL_TS_AUTHKEY`).
  proxy/         `keel proxy`: embedded Caddy + plugin modules.
  cli/           CLI commands, config, output, HTTP client (uses api/).
cmd/keel/        main.go → cli.Execute(ctx, web.Dist()): the dashboard FS rides into `keel serve`
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
  generated into `internal/gen/sqlc/`. Run `sqlc generate` from `internal/adapters/sqlite`.
- IDs: `domain.NewID()`: 20 random lowercase base32 chars. Strings everywhere.
- Times: unix milliseconds (`int64`) in the database, the domain and the JSON API (the dashboard
  compares them with `Date.now()`). The CLI prints RFC 3339.
- Nested Convex objects become columns (`desired_image`, `observed_state`, …) or child tables
  (`endpoints`, `deployment_steps`, `deployment_log`). JSON columns only for small free-form
  lists (`axiom_pending.orgs`).

## Use cases

```go
// app.App holds every port. One file per area: app/projects.go, app/nodes.go, …
func (a *App) RenameNode(ctx context.Context, actor domain.Actor, id string, name string) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, id)          // access rule, returns domain errors
		if err != nil { return err }
		...
		ch.Environment(scope.Project.OrganizationID, scope.Environment.ID)   // what the dashboard must refetch
		return nil
	})
}
```

- `a.read` / `a.write` wrap `Store.Read` / `Store.Write`. `write` publishes the collected
  `Changes` after commit and then runs their `AfterCommit` funcs (a crash in between loses only
  an invalidation; clients refetch everything on reconnect).
- Runtime state lives on `App`, in fields `app.New` sets: the observe debounce and the per-node
  apply queues (`deploy`), the proxy sync state (`ingress`), the auth rate limiters
  (`authLimits`). None of it is a package variable. `New` also defaults `Events` and `Conns` to
  no-ops, `Now` and `Log`, so a spec-only `App` (`keel openapi`) is built the same way.
- `Tx` is the composite of per-area interfaces (`ProjectsTx`, `NodesTx`, …) declared in
  `app/ports_<area>.go` and implemented in `adapters/sqlite/<area>.go`.
- Long work (Docker, Axiom, Caddy) happens outside the transaction: read, release, call out,
  write the outcome. Never hold the write lock across a network call.
- Background work (Convex `scheduler.runAfter`, crons) goes through the `Jobs` port:
  `After(key, delay, fn)` coalesces by key (a pending job with the same key wins; a negative
  delay runs at once), `Every(name, interval, fn)` runs `fn` on each tick, never overlapping
  itself. `Stop` starts nothing new, waits for running jobs and cancels them at its deadline.
- The recovery pass is a job too: once its listener serves, `serve` queues
  `Jobs.After("recover", 0, a.Recover)`. `Recover` runs three parts, each recovering its own
  panic: deploy (the full observe sweep and server count, re-arm deployment timeouts, re-queue
  applies that never reached Swarm, `keel-agent` when `KEEL_AGENT_IMAGE` is set), ingress (move
  default domains to `KEEL_PUBLIC_IP`, proxy sync, the 2-minute resync) and observability (purge
  expired Axiom sign-ins, then every minute). That replaces durable scheduling until workflows
  land.

## Errors

`domain.Error{Code, Message}`. `Code` is the CLI's code vocabulary (`domain.Code*`), `Message` is the
human sentence (kept identical to the Convex `ConvexError` messages). Transport maps codes to
HTTP status and writes RFC 9457 `application/problem+json` with the extra field `code`:

| Code                                                                                       | Status |
| ------------------------------------------------------------------------------------------ | ------ |
| `NOT_AUTHENTICATED`                                                                        | 401    |
| `NO_ORGANIZATION`, `FORBIDDEN`                                                             | 403    |
| `NOT_FOUND`, `PROJECT_NOT_FOUND`, `SERVICE_NOT_FOUND`, `VARIABLE_NOT_FOUND`, `DEPLOYMENT_NOT_FOUND` | 404    |
| `NAME_TAKEN`, `NOTHING_TO_SHIP`, `DEPLOYMENT_RUNNING`, `CONFLICT`, `TRACES_OFF`            | 409    |
| `AUTHORIZATION_PENDING`                                                                    | 428    |
| `RATE_LIMITED` (with `Retry-After`)                                                        | 429    |
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
- Raw (non-Huma) routes: `POST /worker/events`, `GET /worker/config`, `POST /proxy/events`
  (bearer `KEEL_WORKER_TOKEN`), `POST /otlp/v1/traces` (environment ingest key). They skip
  session resolution and read the bearer once (`bearerToken`); the worker and proxy routes
  answer plain text (`writeText`).
- `GET /api/meta` is what the CLI discovers an install with.

### Worker protocol

What `keel agent` and `keel proxy` speak to `serve`. Both ship in the same image as `serve`, so
the shapes move together.

- Auth: `Authorization: Bearer <KEEL_WORKER_TOKEN>`, compared as SHA-256 digests in constant time
  (`workerAuthorized`). No token configured rejects everything: `401 unauthorized`.
- `POST /worker/events`: a JSON array of Docker events, nothing else (no single object, no
  NDJSON). Each element decodes into `Type` and `Actor.Attributes` (`name`,
  `com.docker.swarm.service.name`); a body that is not an array or an element without `Type` is
  `400 bad json`, over 256 KiB `413 too large`. The agent posts `[]` with `X-Keel-Resync: 1` each
  time its Docker event stream (re)connects, then one one-element array per event in order, with
  the resync header again after a failed post. `200 ok` → `IngestWorkerEvents`.
- `GET /worker/config`: `{"sinks":[{"serviceIds":[…],"sink":{kind, domain, dataset, traces?,
  token, org?},"since":<ms>}]}`, one entry per organization sink with every deployable node of
  that organization and when the sink was connected. The agent reads `kind`, `domain`, `dataset` and
  `token` of the sink. Polled every 30 s.
- `POST /proxy/events`: `{event: "cert_obtained"|"cert_failed", name, error?}`, at most 256 KiB;
  anything else is `400 bad report`.

## Realtime

- One WebSocket per tab: `GET /api/ws`, centrifuge server, `centrifuge` JS client.
- Connect auth = the same session: `/api/ws` sits behind the HTTP edge's `withActor`, and the
  adapter reads that actor (`transport.ActorFrom`) instead of resolving the token again; signed
  out is refused with `4501`. The server subscribes the connection to `org:<organizationId>`
  (server-side subscription) and labels it with its session id, which is how `DisconnectSession`
  finds it; a client never picks channels.
- Publications: `{"type":"invalidate","topics":["/api/environments/<id>", "/api/nodes/<id>", …]}`.
  A topic is a URL path prefix. The dashboard invalidates every TanStack query whose key (the
  request path, see below) starts with a topic. Reconnect = invalidate everything.
- `Changes` helpers name the topics so handlers never spell paths: `ch.Projects(org)`,
  `ch.Environment(org, envID)` (canvas, summary, deployments of it), `ch.Node(org, envID, nodeID)`,
  `ch.Deployment(org, envID, deploymentID)`, `ch.Organization(org)` (members, invitations, sink).
- Delivery (`internal/adapters/realtime`): publishes to one organization are merged for 100 ms,
  topics deduped and sorted. Close codes the client must honour: `4501` "signed out" (terminal:
  the session is gone, show sign-in), `4001` "membership changed" (reconnect: the new connection
  joins the user's current organization), centrifuge's `3001` shutdown and `3004` server error
  (reconnect). A signed-in user without an organization is connected with no subscription.
- Read-your-writes: a non-GET `/api/*` response carries `Keel-Invalidate: <topic>,<topic>`, the
  topics its committed writes published for the caller's organization (web-data.md §9.3). The
  fetch client invalidates those before resolving the mutation; the socket message follows.

## Dashboard

- `apps/web` stays a Vite + React app. `kubb.config.ts` reads `../../openapi.json` and generates
  `src/gen/api/` (types, zod schemas, fetch client, TanStack Query hooks).
- Query keys: the first element is the resolved request path (`/api/environments/abc/canvas`),
  so invalidation topics match by prefix.
- `src/lib/realtime.tsx`: the WebSocket provider. Components only call generated hooks.
- Production: `apps/web/embed.go` embeds `dist/`; `keel serve` serves it with an SPA fallback.
  Dev: Vite proxies `/api`, `/worker`, `/otlp`, `/proxy` to `keel serve`.

## Env (serve)

| Var                                   | Meaning                                                                        |
| ------------------------------------- | ------------------------------------------------------------------------------ |
| `KEEL_LISTEN` (`:8080`)               | HTTP listen address (compose publishes it on the tailnet IP)                   |
| `KEEL_DATA_DIR` (`/data`)             | SQLite file and other state                                                    |
| `KEEL_SITE_URL`                       | Dashboard URL as users open it (device-login links, Secure cookies, OTLP/report base) |
| `KEEL_WORKER_TOKEN`                   | Bearer for agent and proxy routes                                              |
| `KEEL_PUBLIC_IP`, `KEEL_ACME_CA`, `KEEL_ACME_EMAIL` | Ingress, as before (docs/networking.md)                          |
| `KEEL_OTLP_URL`                       | OTLP relay URL injected into traced services                                   |
| `KEEL_PROXY_SOCKET` (`/run/keel-proxy/admin.sock`) | keel-proxy admin socket (the edge stays its own container). The default is `caddy.DefaultSocket`, shared by `keel serve` and `keel proxy` (flag `--socket`, then this var, then the default) |
| `DOCKER_HOST`                         | Docker socket (default `unix:///var/run/docker.sock`)                          |
| `KEEL_PROXY_REPORT_URL`               | Where keel-proxy POSTs certificate events (an IP URL the host netns reaches; default `<site>/proxy/events`) |
| `KEEL_AGENT_IMAGE`                    | When set, serve creates/updates the `keel-agent` global service from this image (token as a Swarm secret) |
| `KEEL_AGENT_CONTROL_URL`              | The `KEEL_URL` agents reach serve at (tailnet URL; default `KEEL_SITE_URL`)    |
| `KEEL_AXIOM_AUTH_URL`, `KEEL_AXIOM_API_URL`, `KEEL_ALLOW_LOCAL_SINKS` | Axiom overrides for tests: the two URLs are trimmed and only kept when `KEEL_ALLOW_LOCAL_SINKS=1` (`serve.ConfigFromEnv`) |

## Testing

- `domain`: plain table tests. `app`: against the real SQLite adapter on a temp file (fast, no
  mocks) plus hand-written fakes for Swarm/Proxy/Axiom ports. Adapters: real SQLite; Swarm against
  a Docker-in-Docker Swarm in CI, never the dev box's shared Swarm.
- `go vet ./...`, `gofmt`, `go run ./tools/nocomments` (no comments in Go), `go test ./...` and
  `openapi.json` matching `keel openapi` must pass (`ci.yml`, `make test`). `bun run check-types`
  + `bun run build` in `apps/web` must pass.

## Building an area (implementation guide)

The port is split into areas that are built in parallel, each in its own git worktree, then
merged. Rules that keep the merge mechanical:

- **Own your files.** An area owns `internal/app/<area>*.go`, `internal/app/ports_<area>.go`,
  `internal/adapters/sqlite/<area>.go` + `queries/<area>.sql`, `internal/transport/http/<area>*.go`,
  `internal/api/<area>.go`, plus any adapter package it is assigned. Don't edit other areas' files.
- **Shared files you may append to, never rewrite:** `go.mod`/`go.sum` (`go get`), new migration
  files `migrations/0002_<area>.sql` (additive, only if 0001 truly lacks something; say why in
  your report), `domain/` (new files or new fields; no comments, see `tools/nocomments`).
- **sqlc:** query names start with the area (`AuthGetUser`, `CanvasInsertProject`,
  `DeployListRunning`, `ObsGetSink`, `IngressListHTTP`). ASCII only in `queries/*.sql`. Regenerate
  with `cd internal/adapters/sqlite && ~/go/bin/sqlc generate`; commit the generated
  `internal/gen/sqlc/` files.
- **Routes and operationIds** follow `docs/go/spec/web-data.md` §4 and §10 (the dashboard is
  rewritten against them), and the path prefixes in "Realtime" above. Every write calls the right
  `Changes` helper (web-data.md §10.3).
- **Messages** are the Convex strings from the specs, verbatim. Codes from `domain`.
- **Tests:** domain rules as table tests; use cases against `sqlite.OpenTest`-style temp databases
  with hand-written fakes for Swarm/Proxy/Axiom ports. No network, no real Docker in unit tests.
- **Done means:** `go build ./... && go vet ./... && go test ./...` green, `gofmt -l` and
  `tools/nocomments` empty, work committed on your branch.

Toolchain: `export PATH=$HOME/.local/go/bin:$HOME/go/bin:$PATH` (Go 1.27 via GOTOOLCHAIN=auto).

## Resolved API decisions

Where the specs disagree (critic addenda W1, C1, C2), this table wins.

| Topic | Decision |
| --- | --- |
| Error body | `application/problem+json` with `code` from the CLI vocabulary (`domain.Code*`); `INVALID_INPUT` is 422. Never lowercase codes. The CLI uses `code` as is and computes only `fix`. `CONFLICT` and `UNAVAILABLE` join the CLI's code list (codes are only added). |
| Null reads | A read that returned `null` in Convex (missing, foreign, malformed id, signed out where the Convex query returned null) returns **200** with a named envelope field set to `null`: `{summary}`, `{deployment}`, `{tracing}`, `{project}`, `{invitation}`. Never 404 for those. Writes on missing things fail with the spec's error. |
| Session | `GET /api/me` → `{user: {id,email,name} \| null, organization: {id,name,slug,role} \| null}`, 200 even when signed out. It backs the dashboard's `useSession()` and `keel whoami`. Topic `/api/me` (membership changes). |
| Accounts | `GET /api/auth/sign-up-open` → `{open}`; `POST /api/auth/sign-up {email,password,name,invitationId?}`; `POST /api/auth/sign-in {email,password}`; `POST /api/auth/sign-out`. Sign-up and sign-in set the cookie and also return `{token}` (CI and the CLI use it). `ci.yml` moves to these paths in this PR. |
| Device login | Paths stay under `/api/auth/device`: `POST .../code {client_id}` → RFC 8628 body; `POST .../token {grant_type, device_code, client_id}` → `{access_token, token_type, expires_in}` or 400 `{error: authorization_pending\|slow_down\|expired_token\|access_denied, error_description}` (RFC shape, not a problem); `GET /api/auth/device?user_code=` (signed in; shows and binds the code); `POST .../approve {userCode}`; `POST .../deny {userCode}`. |
| Organization | `GET /api/organization/members`; `GET/POST /api/organization/invitations` (`POST {email, role?}` → `{id,email,role,expiresAt}`); `DELETE /api/organization/invitations/{id}`; public `GET /api/invitations/{id}` → `{invitation: {email, organization} \| null}`. |
| Projects | `GET /api/projects`; `POST /api/projects {name}`; `POST /api/projects/default` → `{slug}`; `GET /api/projects/by-slug/{slug}` → `{project \| null}`. |
| Canvas | `GET /api/environments/{id}/nodes` → `{nodes: NodeView[]}`; `GET /api/environments/{id}/summary` → `{summary \| null}`; `POST /api/environments/{id}/nodes`; `PATCH /api/nodes/{id}` (rename, desired, config, parent); `PUT /api/nodes/{id}/position {x,y}` (one per node); `POST /api/nodes/{id}/duplicate`, `/start`, `/stop`; `DELETE /api/nodes/{id}`. |
| Variables | Key in the body, never in the path: `GET /api/nodes/{id}/variables`; `POST /api/nodes/{id}/variables {key, value, secret, previousKey?}`; `POST /api/nodes/{id}/variables/delete {key}` (no-op when missing, as Convex). The key is validated in the handler so a bad key gets `Key: UPPER_SNAKE_CASE only`. |
| Deployments | `POST /api/environments/{id}/deployments {only?, refresh?}` (Ship / redeploy / retry) → `{id}`; `GET /api/environments/{id}/deployments/latest` → `{deployment \| null}`; `GET /api/deployments/{id}` → `{deployment \| null}`; `GET /api/nodes/{id}/deployments`. JSON uses `id`, never `_id`. |
| Ingress | `POST /api/nodes/{id}/expose {protocol?, domain?, port?, publicPort?}` → `EndpointView`; `POST /api/nodes/{id}/unexpose {protocol?, domain?, publicPort?}` (no selector = all); `GET /api/control-plane` → `{publicIp: string \| null}` (signed in). |
| Traces | One route: `GET /api/environments/{id}/traces?range=&search=&nodeId=`. |
| Env names | `KEEL_SITE_URL` (no `SITE_URL` fallback); `BETTER_AUTH_SECRET` is gone (cookies are opaque random tokens, not signed); `KEEL_LISTEN` `:8080`; `KEEL_DATA_DIR` `/data`; `KEEL_PROXY_SOCKET` and `KEEL_PROXY_REPORT_URL` stay (the edge is a separate container, proxy-ingress.md §12.4 option 1); `DOCKER_HOST`. Agent: `KEEL_URL`, `KEEL_WORKER_TOKEN` (else `/run/secrets/keel_worker_token`), `KEEL_STATE` (`/var/lib/keel-agent/state.json`), `KEEL_CONFIG_POLL_MS`, `KEEL_TS_AUTHKEY`; Docker through `DOCKER_HOST` (moby's `client.FromEnv`), no `DOCKER_SOCKET`. |
