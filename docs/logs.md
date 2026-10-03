# Logs and traces — sinks and providers

> Where container logs go and where the Logs tab reads them from (decided 2026-09-20), and where the Traces tab reads OpenTelemetry traces from (2026-10-03, [Traces](#traces)). Read this before touching `convex/logs.ts`, `convex/traces.ts`, `convex/logSinks.ts`, `convex/logProviders/*`, `convex/traceProviders/*` or `apps/worker/src/logs.ts`. The worker itself is in [`workers.md`](./workers.md).

## Decision

Logs are a per-project **sink** with a matching **provider**. The sink is the write side: the per-node worker streams every container line of the project there. The provider is the read side: `logs.tail` queries it for the Logs tab. Both sides agree on one event shape (below). The tab never knows which provider answered; it gets `{ source, lines, replicas }` either way.

Two providers exist:

| Kind                       | Write side                                                                                                                                                               | Read side                                                                | When                                                 |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------ | ---------------------------------------------------- |
| `docker` (default, no row) | nothing shipped                                                                                                                                                          | `docker service logs` from the manager socket (`logProviders/docker.ts`) | zero setup, single user poking at a service          |
| `axiom`                    | worker POSTs NDJSON to `/v1/datasets/<dataset>/ingest` (`apps/worker/src/sinks/axiom.ts`; `/v1/ingest/<dataset>` only on `*.edge.axiom.co`, it is 404 on `api.axiom.co`) | APL over `/v1/datasets/_apl?format=tabular` (`logProviders/axiom.ts`)    | retention, search, dashboards, alerts, metrics later |

Docker is not a real log store: it holds what the node's `json-file` driver kept, every tail is a round-trip to every node running a task, and it is gone with the container. Axiom (or, later, ClickHouse) is where logs live once anyone cares about them.

## Why the worker ships, not a logging driver

Docker has no native "stream all containers on this node" API, and the two ways to get one both cost more than they give:

- A **logging driver** per service (`fluentd`, `gelf`, `syslog`, `awslogs`) is Docker shipping on its own, but it needs a driver that speaks the sink's protocol, and Axiom has none (no syslog, GELF or fluentd intake; only HTTP ingest and OTLP). Even where one exists it puts the sink config in every `ServiceSpec`, so connecting or changing a sink restarts every task, and under an outage the driver either blocks the container's stdout (`mode=blocking`, the default) or drops from a ring buffer (`non-blocking`). `docker service logs` itself keeps working with any driver: the daemon keeps a local cache alongside (dual logging, Docker 20.10+).
- **Vector / Fluent Bit** as the global service would work, but it is a second binary to configure per sink kind, and the worker already has the socket, the labels, the config poll and the resume state.

So the worker does `GET /containers/<id>/logs?follow=1` per `svc-*` container (`apps/worker/src/logs.ts`), the same tail-the-daemon's-file approach Vector, Fluent Bit and Promtail take on a node. Followers start on `container start` events and on every config poll (30s), end with the container's stream (EOF on exit), and resume from a per-container `since` saved in the node's state volume. A container with no resume point yet is read from the moment its project's sink was connected (`since` in `worker.config`, the `logSinks` row's creation time): lines written between a container's start and the worker's next config poll are not skipped, and a container that was already running before the connect does not replay its history. A `svc-*` container starting for a project the worker has no sink for makes it fetch the config right away instead of waiting for the next poll. That `since` advances only once the sink accepted the batch holding the line, so a restart re-reads what was still undelivered from Docker's file instead of skipping it; a restart mid-outage finishes reading exited containers too (Swarm keeps the last few per service). Lines batch per sink (1s or 500 lines) and are sent in order. A sink that stays down backs up its own queue and never another's; at 20k queued lines its followers stop reading until it drains, and Docker's file is the buffer. Nothing is dropped on the worker's side; a batch the sink rejects as malformed (4xx) is the one exception.

## Event shape

Field names are the contract between `apps/worker/src/sinks/types.ts` and every read provider. Change one, change both.

| Field        | Value                                                                                   |
| ------------ | --------------------------------------------------------------------------------------- |
| `_time`      | RFC3339Nano from Docker's `timestamps=1` (Axiom indexes on `_time`)                     |
| `message`    | the line, timestamp stripped                                                            |
| `stream`     | `stdout` \| `stderr`                                                                    |
| `service_id` | Convex node id (the `svc-<id>` service without the prefix); what `logs.tail` filters on |
| `service`    | Swarm service name, `svc-<id>`                                                          |
| `task`       | Swarm task id, one per replica run; the Logs tab tags lines with it                     |
| `replica`    | Swarm slot, from the task name `svc-<id>.<slot>.<task>`                                 |
| `node`       | Swarm node id of the worker                                                             |
| `container`  | short container id                                                                      |

## Schema and functions

```ts
// schema.ts
logSink = v.union(v.object({ kind: "axiom", domain, dataset, traces?, token, org? }))
logSinks: { projectId, sink }  // by_project, at most one row per project
axiomClients: { redirectUri, clientId }                    // by_redirect; DCR client per callback URL
axiomSignIns: { projectId, clientId, state, verifier, redirectUri }  // by_state, by_project; 10 min, single use
axiomPending: { projectId, token, orgs }                   // by_project; 10 min, single use
```

`convex/logSinks.ts`:

- `get(projectId)` — public. Kind, domain, dataset, traces dataset (null on sinks from before traces), org name, last four of the token. Never the token.
- `beginAxiomSignIn` / `signInAxiom` / `chooseAxiomOrg` / `cancelAxiomSignIn` / `pendingOrgs` — Sign in with Axiom, below.
- `connectAxiom(projectId, domain, dataset, traces?, token)` — public action, token paste. No UI any more; kept for scripts and agents (`convex run`) and as the fallback if the OAuth client stops working. Validates region (`api.axiom.co` / `api.eu.axiom.co`; a full origin is accepted only with `KEEL_ALLOW_LOCAL_SINKS=1`, for the mock), creates the dataset if missing (`POST /v2/datasets`, 409 ignored), runs `['ds'] | limit 1` to prove the token can query, then saves. A bad token fails here, nothing is stored.
- `disconnect(projectId)` — back to Docker. Shipped data stays in Axiom.
- `forNode` / `owns` / `save` — internal.

`convex/logs.ts tail(nodeId, tail)` — the one read entry point. Looks up the node's project sink and dispatches. `convex/worker.ts config` — what the worker polls: every sink with the service ids it covers and when it was connected.

`convex/logs.ts recent(environmentId, search?, tail)` — the Observability page's Logs tab: last lines across every service of the environment, `where message contains` for search. Axiom only; Docker has no cross-service query.

Token scope: the worker only ingests, the control plane queries. One API token with ingest + query on the dataset covers both; it is stored once in `logSinks` and reaches the worker through `GET /worker/config`, which is bearer-protected by `KEEL_WORKER_TOKEN`.

## Sign in with Axiom

Axiom has no app console for third-party OAuth clients, but the authorization server behind its MCP server does Dynamic Client Registration (`mcp.axiom.co/.well-known/oauth-authorization-server` → `authorization.axiom.co`, `/oauth2/{register,authorize,token}`). Its tokens are accepted by `api.axiom.co` (verified 2026-09-29). Two dead ends first: the 2026-09-20 note here said "no OAuth at all", and the CLI's `login.axiom.co` client (`axiom auth login`) only accepts loopback redirects (`redirect_uri is not permitted for this client`).

1. Observability page → Sign in with Axiom → `beginAxiomSignIn(projectId, <origin>/axiom/callback)`. First time for that callback URL, Keel registers a public client (`client_name: Keel`, `token_endpoint_auth_method: none`) and caches its id in `axiomClients`. Then it makes verifier + state (server-side: the browser may be on plain http, where `crypto.subtle` does not exist), stores them in `axiomSignIns`, and returns the authorize URL. The browser keeps only the project slug and tab to come back to.
2. Axiom redirects to `/axiom/callback?code&state` → `signInAxiom(state, code)`: takes the row (must belong to the caller), exchanges the code, lists `/v2/orgs`.
3. One org: provision now. Several: the user token waits in `axiomPending` and the page shows an org picker → `chooseAxiomOrg`.
4. Provision, with the user token + `x-axiom-org-id`: create `keel-<project slug>` and `keel-<project slug>-traces` (409 ignored), mint one API token with `ingest:create` + `query:read` on those two datasets only (`POST /v2/tokens`), prove it can query both, save the sink. The user token is dropped; only the scoped token is stored. Signing in again on a connected project replaces the sink; that is how a sink from before traces gets its traces dataset.

**Keel must be served over https** (or `http://localhost`): DCR refuses a plain-http IP redirect URI (`invalid_redirect_uri`). Dev: Tailscale Serve (`scripts/dev-https.sh`). An installed Keel reached by tailnet IP needs the same before Sign in with Axiom works; `connectAxiom` (token paste) is the fallback.

Region: org `defaultEdgeDeployment` containing `eu-` → `api.eu.axiom.co`, else `api.axiom.co`. Only US verified.

A new dataset has no fields, and APL rejects `where service_id …` with 400 `invalid field` until the first line lands; the tail queries read that as "no lines yet".

Mock for local testing: `KEEL_ALLOW_LOCAL_SINKS=1` plus `KEEL_AXIOM_AUTH_URL` and `KEEL_AXIOM_API_URL` (full origins; ignored without the first flag) point the flow and every API call at a mock.

## UI

Rail → Observability (`apps/web/src/components/canvas/observability/`), drawn over the canvas (which stays mounted, `inert`), with two tabs: Traces (`?view=traces`, the rail's default) and Logs (`?view=logs`). Without an Axiom sink each tab is a gate (`axiom-gate.tsx`): a blurred fake of the tab behind a card, "Set up Axiom", one line of copy, and a Sign in with Axiom button in Axiom's brand orange with its logo mark (or the org picker while one is pending). Logs tab (`logs.tsx`): every service's lines interleaved, service name per line, stderr in the warning tone, search box, following toggle, Disconnect (back to Docker, no traces; shipped data stays in Axiom). Polls `logs.recent` every 3s. Traces tab: see [Traces](#traces).

The per-service Logs tab in the bottom panel works either way and shows `· via Axiom` in its meta line when the provider is Axiom.

## Adding a provider (e.g. ClickHouse)

1. `schema.ts`: add a variant to `logSink`.
2. `apps/worker/src/sinks/<kind>.ts`: implement `Sink.send(events)` (`true` once delivered or rejected as malformed, `false` when unreachable so the worker keeps the batch); register it in `sinks/index.ts buildSink`.
3. `convex/logProviders/<kind>.ts`: `<kind>Tail(cfg, serviceId, n): Tail` and a `<kind>Verify(cfg)`.
4. `convex/logs.ts`: one branch in `tail`. `convex/logSinks.ts`: a `connect<Kind>` action.
5. `observability/axiom-gate.tsx`: a way to connect it on the gate.

Nothing else knows the kind.

## Verified (2026-09-20, single node, mock Axiom on `127.0.0.1:4318`)

Connect with a wrong token → `Axiom 403: forbidden`, nothing saved. Connect with the right one → dataset created, row saved. Worker picks the sink up on its next poll (`config applied {"sinks":1,"services":2}`), follows the running containers, and a new nginx node deployed afterwards is followed within 30s (its `container start` event lands before the config poll lists it; the poll catches it). Six nginx requests → 9 events ingested in one batch → Logs tab reads them back via APL with `via Axiom` in the header, `r1` tags, stderr and stdout interleaved by `_time`. Force-restarting the worker resumes both `docker events` and each container tail from the saved `since`: the mock's count did not move until a new request was made, so nothing replayed.

Sign in with Axiom verified 2026-09-29 against a mock (authorize auto-approves, PKCE checked on `/oauth/token`, `/v2/orgs|datasets|tokens`, APL): two orgs → picker → pick → dataset + scoped token minted with the org header, sink saved with only the scoped token; one org → connected straight from the callback; Logs page shows only lines of the environment's services (a foreign `service_id` in the same dataset is filtered out), search narrows via APL, Disconnect returns to the gate. Against real Axiom (2026-09-29, US org, Keel on `https://dev.<tailnet>.ts.net`): DCR, sign-in, org listing, dataset + scoped token creation all worked first try. The worker's ingest path was wrong (`/v1/ingest/<dataset>` → 404 on `api.axiom.co`; the mock had accepted it), fixed to `/v1/datasets/<dataset>/ingest`; nginx requests then showed up in the dataset within seconds, stdout and stderr, with `service`/`replica` set.

Not there yet: retention setting, metrics, the ClickHouse provider, a live stream (the tab still polls `logs.tail` every 3s).

## Traces

OpenTelemetry traces live next to the logs, in the same sink. Axiom wants a dedicated dataset per OTel signal ("You must use a different, dedicated dataset for each OTel component"), so an Axiom sink carries a second dataset, `traces` (`keel-<slug>-traces`), covered by the same scoped token. Sinks connected before 2026-10-03 have none; their Traces tab is a "Turn on traces" card that runs Sign in with Axiom again.

**Read side only, for now.** Spans get into the dataset from whatever exports them: an app's OTel SDK pointed at Axiom (`OTEL_EXPORTER_OTLP_ENDPOINT=https://api.axiom.co`, `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<token>,X-Axiom-Dataset=<traces dataset>`; the empty Traces tab shows exactly this). Keel does not forward OTLP yet. The plan: an OTLP receiver in the worker (apps keep no Axiom token, a sink change needs no redeploy, same shape as log shipping) plus `OTEL_*` and `OTEL_RESOURCE_ATTRIBUTES=keel.service_id=…` set on every service at apply time, which is also what will scope traces to an environment and map them to canvas nodes. Until then the tab shows every trace in the project's dataset.

**Axiom's span rows.** One row per span: `_time` (start, RFC3339 to the nanosecond), `trace_id`, `span_id`, `parent_span_id`, `name`, `kind`, `duration` (nanoseconds), `error` (bool), `status.code` / `status.message`, `service.name`, `scope.name`, `attributes.*` for semantic-convention attributes with everything else in the `attributes.custom` map, `resource.*`, `events`. A field no span has carried yet does not exist and APL rejects it with 400 `invalid field`, so optional ones go through `ensure_field()` and an empty dataset reads as "no traces yet".

**A request is a root span** (`isempty(parent_span_id)`), as in Axiom's own trace dashboards: rate, error rate and p50/p95/p99 count roots only; aggregating every span would mix each request's children in.

`convex/traces.ts` (default runtime, no Docker fallback: traces need a store):

- `overview(environmentId, range, search?)` — `range` is `15m | 1h | 24h | 7d` (~30 buckets each: 30s, 2m, 1h, 6h). Three parallel APL queries over root spans (totals, `summarize … by bin(_time, …)`, the latest 50), then one for span/error counts of those 50 traces. `search` filters root span name or `service.name` and scopes every number.
- `get(environmentId, traceId, at?)` — every span of one trace (max 2000), oldest first. `at` (the root's start, known when opened from the list) narrows the window; a pasted `&trace=` link searches the last week.

`convex/traceProviders/types.ts` is the contract every trace provider returns (`TraceOverview`, `Trace`, `Span`: times in epoch ms, durations in ms, both fractional). `traceProviders/axiom.ts` parses rows defensively (flat dotted keys or nested objects, numbers or Go/.NET duration strings, `custom` maps folded back into the attribute list), so a change in Axiom's serialisation does not blank the tab. A ClickHouse provider is a `traceProviders/clickhouse.ts` plus one branch in `traces.ts`.

**UI** (`observability/traces.tsx`, `charts.tsx`, `trace.tsx`): filter field and range picker in the header; a KPI row (requests + rate, error rate, p50, p95, p99); two hand-drawn SVG charts sharing one hover (requests per bucket with the failed share stacked on top in the danger tone; latency p50/p95/p99 as one blue ramp, colour-vision checked); the latest requests (time, root span name, service, HTTP status or error dot, span count, duration bar). Polls every 15s while visible; a new range or filter keeps the previous numbers up, dimmed. A row opens `&trace=<id>`: a waterfall (spans indented under their parent, bars on the trace's timeline, errors in the danger tone with a dot) and the selected span's attributes, events (exceptions with their stack traces) and resource on the right.

Verified 2026-10-03: the APL (`ensure_field`, `isempty`, `countif`, `percentile`, `bin`, `in`, `contains`) parses on real Axiom (read-only queries against an existing logs dataset; only `invalid field` for the span fields that dataset lacks). End to end against a mock that flattens OTLP/JSON spans the way the rows above describe: Sign in with Axiom creates both datasets and one token scoped to both; an empty traces dataset shows the setup card; 700 synthetic spans across `api` and `worker` give bucket sums equal to the totals in every range, a filter scopes them, a failed deploy opens as a 5-span waterfall across both services with its exception event; a project with a pre-traces sink shows the reconnect card. Not checked against real spans in Axiom yet: the exact serialisation of `duration` and `events` in tabular results is assumed from Axiom's docs and its CLI skill.
