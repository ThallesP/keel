# Logs and traces — sinks and providers

> Where container logs go and where the Logs tab and the Observability page read them from (decided 2026-09-20), where the Observability page reads OpenTelemetry traces from (2026-10-03, [Traces](#traces)), and how services get spans there (2026-10-04, [Getting spans in](#getting-spans-in)). Read this before touching `convex/logs.ts`, `convex/traces.ts`, `convex/otlp.ts`, `convex/tracing*.ts`, `convex/logSinks.ts`, `convex/logProviders/*`, `convex/traceProviders/*` or `apps/worker/src/logs.ts`. The worker itself is in [`workers.md`](./workers.md).

## Decision

Logs are a per-organization **sink** with a matching **provider**: one per Keel organization, shared by every project in it (per project until 2026-10-04, see [One sink per organization](#one-sink-per-organization)). The sink is the write side: the per-node worker streams every container line of every project there. The provider is the read side: `logs.tail` queries it for the Logs tab. Both sides agree on one event shape (below). The tab never knows which provider answered; it gets `{ source, lines, replicas }` either way.

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

So the worker does `GET /containers/<id>/logs?follow=1` per `svc-*` container (`apps/worker/src/logs.ts`), the same tail-the-daemon's-file approach Vector, Fluent Bit and Promtail take on a node. Followers start on `container start` events and on every config poll (30s), end with the container's stream (EOF on exit), and resume from a per-container `since` saved in the node's state volume. A container with no resume point yet is read from the moment its organization's sink was connected (`since` in `worker.config`, the `logSinks` row's creation time): lines written between a container's start and the worker's next config poll are not skipped, and a container that was already running before the connect does not replay its history. A `svc-*` container starting for a project the worker has no sink for makes it fetch the config right away instead of waiting for the next poll. That `since` advances only once the sink accepted the batch holding the line, so a restart re-reads what was still undelivered from Docker's file instead of skipping it; a restart mid-outage finishes reading exited containers too (Swarm keeps the last few per service). Lines batch per sink (1s or 500 lines) and are sent in order. A sink that stays down backs up its own queue and never another's; at 20k queued lines its followers stop reading until it drains, and Docker's file is the buffer. Nothing is dropped on the worker's side; a batch the sink rejects as malformed (4xx) is the one exception.

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
logSinks: { organizationId, sink }  // by_organization, at most one row per organization
                                    // (rows from before 2026-10-04: { projectId, sink }, see below)
axiomClients: { redirectUri, clientId }                    // by_redirect; DCR client per callback URL
axiomSignIns: { organizationId, clientId, state, verifier, redirectUri }  // by_state, by_organization; 10 min, single use
axiomPending: { organizationId, token, orgs }              // by_organization; 10 min, single use
```

`convex/logSinks.ts`:

Every public function acts on the caller's organization; none takes a project.

- `get()` — public. Kind, domain, dataset, traces dataset (null on sinks from before traces), Axiom org name, last four of the token. Never the token.
- `beginAxiomSignIn` / `signInAxiom` / `chooseAxiomOrg` / `cancelAxiomSignIn` / `pendingOrgs` — Sign in with Axiom, below.
- `connectAxiom(domain, dataset, traces?, token)` — public action, token paste. No UI any more; kept for scripts and agents (`convex run`) and as the fallback if the OAuth client stops working. Validates region (`api.axiom.co` / `api.eu.axiom.co`; a full origin is accepted only with `KEEL_ALLOW_LOCAL_SINKS=1`, for the mock), creates the dataset if missing (`POST /v2/datasets`, 409 ignored), runs `['ds'] | limit 1` to prove the token can query, then saves. A bad token fails here, nothing is stored.
- `disconnect()` — every project back to Docker. Shipped data stays in Axiom.
- `sinkOf(ctx, organizationId)` — the organization's row, for the read side and `worker.config`. `forNode` / `forEnvironment` / `save` — internal.

`convex/logs.ts tail(nodeId, tail)` — the one read entry point. Looks up the sink of the node's organization and dispatches. `convex/worker.ts config` — what the worker polls: one entry per project with its organization's sink, the project's service ids and when the sink was connected. Per project because the worker routes per project; equal sinks share one queue in the worker, so the shape did not change when sinks became per organization.

`convex/logs.ts recent(environmentId, search?, tail, range?)` — the Observability page's stream: last lines across every service of the environment, `where message contains` for search, within `range` (`15m | 1h | 24h | 7d`, `convex/timeRange.ts`). `around(environmentId, at)` — every service's lines within 30s either side of `at`, oldest first: a line's surrounding context. Axiom only; Docker has no cross-service query.

Token scope: the worker only ingests, the control plane queries. One API token with ingest + query on the dataset covers both; it is stored once in `logSinks` and reaches the worker through `GET /worker/config`, which is bearer-protected by `KEEL_WORKER_TOKEN`.

## Sign in with Axiom

Axiom has no app console for third-party OAuth clients, but the authorization server behind its MCP server does Dynamic Client Registration (`mcp.axiom.co/.well-known/oauth-authorization-server` → `authorization.axiom.co`, `/oauth2/{register,authorize,token}`). Its tokens are accepted by `api.axiom.co` (verified 2026-09-29). Two dead ends first: the 2026-09-20 note here said "no OAuth at all", and the CLI's `login.axiom.co` client (`axiom auth login`) only accepts loopback redirects (`redirect_uri is not permitted for this client`).

1. Observability page of any project → Sign in with Axiom → `beginAxiomSignIn(<origin>/axiom/callback)`. First time for that callback URL, Keel registers a public client (`client_name: Keel`, `token_endpoint_auth_method: none`) and caches its id in `axiomClients`. Then it makes verifier + state (server-side: the browser may be on plain http, where `crypto.subtle` does not exist), stores them in `axiomSignIns` under the caller's organization, and returns the authorize URL. The browser keeps only the project slug and tab to come back to.
2. Axiom redirects to `/axiom/callback?code&state` → `signInAxiom(state, code)`: takes the row (must belong to the caller's organization), exchanges the code, lists `/v2/orgs`.
3. Axiom's consent page has the user pick an org, and the user token names it in an undocumented `axiomDefaultOrg` claim (an org id, e.g. `ramp-vcrw`; seen 2026-10-03). The token itself still works in every org the user is in. That org, or the only one: provision now. Neither (claim missing or not in `/v2/orgs`): the user token waits in `axiomPending` and the page shows Keel's own org picker → `chooseAxiomOrg`. The user token expires 5 minutes after issue.
4. Provision, with the user token + `x-axiom-org-id`: list the org's datasets and create whichever of `keel-logs` and `keel-traces` is missing, mint one API token (named `keel-<Keel organization slug>`) with `ingest:create` + `query:read` on those two datasets only (`POST /v2/tokens`), prove it can query both, save it as the organization's sink. The user token is dropped; only the scoped token is stored. Signing in again replaces the sink; that is how a sink from before traces gets its traces dataset. The token it replaces stays valid in Axiom (revoke it there).

### One sink per organization

Axiom is set up once per Keel organization, from any project's Observability page, and every project of the organization ships there, including ones created later (decided 2026-10-04). Disconnect turns it off for all of them, so it lives on the rail's Settings page behind a confirmation, not on Observability. The datasets have fixed names, `keel-logs` and `keel-traces`, not one pair per project or per Keel organization: each Axiom org's plan caps its datasets (`license.maxDatasets` in `/v2/orgs`: 3 on the free Personal plan), and past it Axiom answers a create with a bare `400 Bad Request` (confirmed 2026-10-03 on an org at its cap). Provisioning checks the cap first and names the org's datasets and how many to delete; the sign-in token cannot delete datasets (403), so the user does that in Axiom. Lines carry `service_id` and every log query filters on the environment's services, so logs stay per project and environment. Traces are not scoped yet, see [Traces](#traces).

Sinks made before 2026-10-04 were per project (`{ projectId, sink }`, some on `keel-<slug>` datasets). There is no migration step: until a sign-in or Disconnect replaces them, the newest of an organization's projects' rows stands in for its sink (`sinkOf`), so every project ships there and the older rows are neither read nor written. Sign-in and Disconnect delete them. Two one-time effects of deploying this over such an install, accepted: a project that had no sink of its own starts shipping at the next config poll from that row's connect time (`since`), so containers already running re-read what Docker still holds of their output since then; and a Sign in with Axiom in progress during the upgrade (its `axiomSignIns` / `axiomPending` row, at most 10 minutes old, names a project instead of an organization) makes the push fail schema validation until the row expires.

**Keel must be served over https** (or `http://localhost`): DCR refuses a plain-http IP redirect URI (`invalid_redirect_uri`). Dev: Tailscale Serve (`scripts/dev-https.sh`). An installed Keel reached by tailnet IP needs the same before Sign in with Axiom works; `connectAxiom` (token paste) is the fallback.

Region: org `defaultEdgeDeployment` containing `eu-` → `api.eu.axiom.co`, else `api.axiom.co`. Only US verified.

A new dataset has no fields, and APL rejects `where service_id …` with 400 `invalid field` until the first line lands; the tail queries read that as "no lines yet".

Mock for local testing: `KEEL_ALLOW_LOCAL_SINKS=1` plus `KEEL_AXIOM_AUTH_URL` and `KEEL_AXIOM_API_URL` (full origins; ignored without the first flag) point the flow and every API call at a mock.

## UI

Rail → Observability (`?view=observability`, `apps/web/src/components/canvas/observability/`; old `?view=logs` links land there), drawn over the canvas (which stays mounted, `inert`). Logs and traces are one page, no tabs, ClickStack-style (see [Traces](#traces) for the parts that read spans). Without an Axiom sink it is a gate (`axiom-gate.tsx`): a blurred fake stream behind a card, "Set up Axiom", one line of copy, and a Sign in with Axiom button in Axiom's brand orange with its logo mark (or the org picker while one is pending).

With a sink (`explorer.tsx`): header with All / Requests / Logs, a filter (searches lines and request names), the range. Nothing about the connection itself: the Axiom org and datasets and **Disconnect…** (every project back to Docker, no traces; shipped data stays in Axiom; asks first) are on the rail's Settings page (`settings.tsx`), which offers Sign in with Axiom when there is no sink. Then the request numbers and charts, then one stream (`stream.tsx`): log lines and requests (root spans) interleaved, newest first, mono 11px, never wrapping. A line is tagged with its service and, if it names a trace, a `trace` link; a request is a `req` row with status, duration and span count. Each kind is the latest N (300 lines, 100 requests), so the stream stops at the later of the two cut-offs and says how far back it goes. Polls every 10s while visible; a new range or filter keeps the previous data up, dimmed. The first load shows the app's spinner (`Loader`). With nothing to show, the page is a drawing (`lamp.tsx`): an empty range (no requests, no lines, no filter) is a hanging lamp, lit, now and then flickering like a bad bulb, with a moth fluttering around it, always facing the light: "Nothing in the last hour but a moth.", how to get requests in, **Copy agent prompt**, and a faint caption quoting the Harvard Mark II log book (9 September 1947, the moth taped next to "First actual case of bug being found"). A load that fails with nothing to show is the same lamp, out, the moth resting on the shade, the error, and "Trying again every 10 seconds" (the poll keeps going). Reduced motion keeps them still. Logs without requests keep the slim "No requests" banner above the lines, a filter with no match stays a line of text, and a partial failure is a line above the data. Every row opens full screen:

- a request, or a line that names a trace → the trace (`&trace=<id>`, `trace.tsx`): spans and the trace's lines in one waterfall, the clicked line selected;
- any other line → its context (`&around=<ms>`, `log-context.tsx`): every service's lines from 30s either side with the line highlighted, and the requests that started in that minute, each one click from its trace.

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

OpenTelemetry traces live next to the logs, in the same sink. Axiom wants a dedicated dataset per OTel signal ("You must use a different, dedicated dataset for each OTel component"), so an Axiom sink carries a second dataset, `traces` (`keel-traces`, shared by the org's projects like `keel-logs`), covered by the same scoped token. Sinks connected before 2026-10-03 have none; their Observability page shows logs with a "Traces are off" banner that runs Sign in with Axiom again.

Spans get there through Keel's OTLP relay ([Getting spans in](#getting-spans-in)), tagged with the Keel service they came from; every request query is scoped by that tag to the environment's services.

**Axiom's span rows.** One row per span: `_time` (start, RFC3339 to the nanosecond), `trace_id`, `span_id`, `parent_span_id`, `name`, `kind`, `duration` (nanoseconds, or a Go duration string such as `"5ms"` in tabular results: seen 2026-10-04), `error` (bool), `status.code` / `status.message`, `service.name`, `scope.name`, `attributes.*` for semantic-convention attributes with everything else in the `attributes.custom` map, `resource.*` the same way (`resource.deployment.environment.name` is a column; `keel.service_id` and `keel.environment_id` land in the `resource.custom` map), `events`. A field no span has carried yet does not exist and APL rejects it with 400 `invalid field`, so optional ones go through `ensure_field()` and an empty dataset reads as "no traces yet".

**A request is a root span** (`isempty(parent_span_id)`), as in Axiom's own trace dashboards: rate, error rate and p50/p95/p99 count roots only; aggregating every span would mix each request's children in.

**Logs ↔ traces.** Container lines carry no trace context of their own, but an OpenTelemetry-instrumented logger writes the active trace and span into each line: JSON (`"trace_id"`, `"traceId"`, `"otelTraceID"`, `"trace.id"` …), logfmt (`trace_id=…`) or a W3C `traceparent`. That is the whole correlation, done on the read side (`observability/correlate.ts traceRef`): a line opens the trace it names, and a trace's lines are the environment's lines that contain its id (`where message contains "<trace id>"`, over the spans' time ± 5s). Nothing changes in the worker or the event shape, and it works on lines shipped before. In the waterfall a line hangs under the span it names (else under the root), among that span's children by time, as a point; HyperDX does the same with OTel log records' `SpanId`. If this gets slow on big datasets, the worker can extract `trace_id`/`span_id` into fields at ship time (additive to the event shape). Lines that name no trace link to requests by time only (the context view).

`convex/traces.ts` (default runtime, no Docker fallback: traces need a store):

- `overview(environmentId, range, search?, nodeId?)` — `range` is `15m | 1h | 24h | 7d` (~30 buckets each: 30s, 2m, 1h, 6h). Three parallel APL queries over the root spans of the environment's services, or of `nodeId` alone (`keel traces <service>`): totals, `summarize … by bin(_time, …)`, the latest 100; then one for span/error counts of those traces. Services are matched on `tostring(ensure_field("resource.custom", typeof(dynamic))["keel.service_id"])`; spans without it (sent by hand straight to Axiom) are not shown. `search` filters root span name or `service.name` and scopes every number. Each request carries `local` (`deployment.environment.name == "local"`, a `keel run`).
- `get(environmentId, traceId, at?)` — every span of one trace (max 2000) and the lines naming it (max 500), oldest first. `at` (a moment inside the trace: the root's start, or the clicked line's time) narrows the spans' window to the hour before it (a long trace opened from a late line still gets its root), and the lines' window always stretches to it (the clicked line is always found); a pasted `&trace=` link searches the last week. Works without a traces dataset (lines only).
- `around(environmentId, at)` — the environment's requests that started within 30s either side of `at`; empty without a traces dataset.

`convex/traceProviders/types.ts` is the contract every trace provider returns (`TraceOverview`, `Trace`, `Span`: times in epoch ms, durations in ms, both fractional). `traceProviders/axiom.ts` parses rows defensively (flat dotted keys or nested objects, numbers or Go/.NET duration strings, `custom` maps folded back into the attribute list), so a change in Axiom's serialisation does not blank the page. A ClickHouse provider is a `traceProviders/clickhouse.ts` plus one branch in `traces.ts`.

**UI** (`observability/charts.tsx`, `trace.tsx`; the page is under [UI](#ui)): above the stream, a KPI row (requests + rate, error rate, p50, p95, p99) and two hand-drawn SVG charts sharing one hover (requests per bucket with the failed share stacked on top in the danger tone; latency p50/p95/p99 as one blue ramp, colour-vision checked); with no request in range, a card with **Copy agent prompt** ([Getting spans in](#getting-spans-in)); requests from `keel run` carry a `local` tag in the stream; on a sink without a traces dataset, a banner to sign in again. The trace view: spans indented under their parent as bars on the trace's timeline (the spans' extent; lines outside it sit on its edge), errors in the danger tone with a dot, lines as points with a `log` tag; on the right the selected span's attributes, events (exceptions with their stack traces) and resource, or the selected line in full with its JSON/logfmt fields.

Verified 2026-10-03: the APL (`ensure_field`, `isempty`, `countif`, `percentile`, `bin`, `in`, `contains`) parses on real Axiom (read-only queries against an existing logs dataset; only `invalid field` for the span fields that dataset lacks). End to end against a mock that flattens OTLP/JSON spans the way the rows above describe: Sign in with Axiom creates both datasets and one token scoped to both; an empty traces dataset shows the setup card; 700 synthetic spans across `api` and `worker` give bucket sums equal to the totals in every range, a filter scopes them, a failed deploy opens as a 5-span waterfall across both services with its exception event; a project with a pre-traces sink shows the reconnect card. Same day, the merged page against the mock with correlated data (pino JSON and logfmt lines carrying `trace_id`/`span_id`, plus uncorrelated access-log and checkpoint noise): the stream interleaves requests and lines; a "deploy failed" stderr line opens its 3-span trace with its 5 lines under their spans and itself selected, fields parsed; a checkpoint line opens its ±30s context with that minute's requests, one of which opens its trace; the kind filter, cut-off note and old `?view=logs` links work. Not checked against real spans in Axiom yet: the exact serialisation of `duration` and `events` in tabular results is assumed from Axiom's docs and its CLI skill.

### Getting spans in

Decided 2026-10-04: no Keel SDK. A service is instrumented with the standard OpenTelemetry SDK of its language, configured only by the standard `OTEL_*` variables, and Keel sets those. Getting the code there is a coding agent's job: Keel hands out a prompt to paste into Claude Code, Cursor, Codex…, and the prompt has the agent prove its work locally before anything ships. Nothing Keel-specific lands in the app, so the same code works against any OTLP backend.

**The prompt** (`convex/tracingPrompt.ts`, served by `tracing.prompt`): one text for the dashboard's **Copy agent prompt** (Observability's empty state, and the Tracing section of a service's Settings tab) and `keel tracing prompt [service]`. It names the service and project when it can and never an endpoint or a key. Rules: official SDK plus auto-instrumentation; configuration from the variables only; traces only over OTLP/HTTP protobuf; start only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; load before the app (`--import`, ESM loader hook); on SIGTERM flush and exit; server spans named `METHOD route` with `http.route`; JSON log lines carrying `trace_id`/`span_id` (that is what [Logs ↔ traces](#traces) reads). Then per-stack notes (Node, Next.js, Bun, Python, Go) and the test loop: `keel run <service> -- <dev command>`, a few requests, `keel traces <service> --since 15m --json` until they show with `"local": true`, route names and child spans, then a log line with the same trace id. Last, the human's step: `keel tracing enable <service>` and `keel redeploy <service>`. Changes to the text are tested the same way: hand it to a fresh agent in an uninstrumented repo and read what it trips on (see Verified below).

**The relay** (`convex/otlp.ts`, `POST <convex site>/otlp/v1/traces` in `http.ts`). Bearer = the environment's ingest key (`otlpKeys`: `keel_otlp_<24 random bytes, base64url>`, one per environment, made by an action on first use, never rotated yet). The key leads to environment → project → organization → `sinkOf` → the traces dataset, looked up per request, so a new or replaced sink needs no redeploy and apps never see the sink's token. The body (protobuf or JSON, gzip or not) is forwarded unchanged to `https://<domain>/v1/traces` with `X-Axiom-Dataset`; 4 MiB cap. Statuses follow OTLP/HTTP: unknown key 401, other content type 415; Axiom 429/502/503/504 pass through and other 5xx or no answer become 503 (exporters retry those); Axiom 4xx becomes 400 (exporters drop). An organization with nowhere to put traces gets 200 and the spans are dropped, so an exporter does not log a failure every 5 seconds; `keel traces` says why nothing shows (`TRACES_OFF`). The relay does not read the body, so `keel.service_id` is the sender's claim: a process holding one environment's key could tag spans as a service of another environment in the same organization, and they would show there. Accepted for now, because the organization is the boundary: its members reach every environment's key through `keel run`, and the dataset is the organization's. Checking each resource against the key's environment means decoding protobuf and gzip per request; it belongs in the worker receiver, which can set the `keel.*` attributes from the key instead of trusting them.

Why the control plane and not the worker receiver planned here before: a laptop running `keel run` can reach the Convex site URL (the CLI already uses it) but no worker, and one endpoint for both paths kept this small. The cost is that deployed services' spans pass through Convex HTTP actions. When that matters, the worker gets an OTLP receiver on the overlay and the injected endpoint changes; apps do not change.

**The switch** (`convex/tracing.ts`). `desired.tracing` on a service, staged like a variable (`tracing.enable(nodeId, on)` sets it and `dirty`; it needs a sink with traces and makes the ingest key). At apply, `nodesInternal.applyInput` adds (`withTracing`), after the service's own variables and never over one of them:

```
OTEL_EXPORTER_OTLP_ENDPOINT=<KEEL_OTLP_URL, else CONVEX_SITE_URL + /otlp>
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<ingest key>
OTEL_SERVICE_NAME=<node name>
OTEL_RESOURCE_ATTRIBUTES=keel.service_id=<node id>,keel.environment_id=<env id>,deployment.environment.name=<env name>
OTEL_TRACES_EXPORTER=otlp  OTEL_METRICS_EXPORTER=none  OTEL_LOGS_EXPORTER=none
```

Metrics and logs exporters are off because the relay only takes traces (container logs come from stdout through the worker). An install's site URL is `http://<KEEL_ADDR>:3211`, which containers reach over the host's tailnet route; a dev deployment's is loopback, so dev sets `KEEL_OTLP_URL=http://<tailnet ip>:3211/otlp`. The service's own variables win key by key, with one tie: the ingest key only goes to Keel's endpoint, so a service that sets `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` itself gets no Keel headers (`keel run` does the same). `tracing.forNode` is the Settings tab's view of the same set (key masked, `overridden` where the service's own variables replace it). Turning it off and shipping removes them.

**Local runs.** `tracing.localEnv(nodeId)` returns the same set with `deployment.environment.name=local` and no endpoint; `keel run` adds `<its convex site URL>/otlp`, the address that machine already reaches. A local run is otherwise the deployed service (same service id), so its requests show on the environment's Observability page and in `keel traces`, tagged `local`.

Verified 2026-10-04 against real Axiom (org `ramp`, dev box): a probe span sent straight to Axiom showed where the `keel.*` attributes land (above); the scoping APL runs on `keel-traces` and returns nothing, not a 400, on a dataset without `resource.custom`. Through the relay: bad or missing key 401, `text/plain` 415, OTLP/JSON plain, gzip and via the ts.net HTTPS name all 200 and all three spans in `keel-traces`. `keel traces` lists them for the right service and project only (a span tagged with another service id stays hidden), with `local`.

The prompt, tested 2026-10-04 the way it is meant to be used: an uninstrumented Node app (plain `node:http`, pino, a `fetch` to itself standing in for a database), a fresh agent given only the pasted text and a logged-in `keel`, nobody answering. A first read (Claude Sonnet, read-only) flagged gaps that went into the text: route names for hand-written routers, the ESM loader hook, exiting after the SIGTERM flush, `fs`/`dns`/`net` noise, `@opentelemetry/api`. Then two agents worked it end to end in parallel, each on its own copy and service: Claude Sonnet 5.5 (`web`) and Codex GPT-5.6-Luna (`api`). Both instrumented the app (preload with the loader hook, the SDK only when the endpoint is set, `http.route` and span renames in the hand-written router, pino untouched), ran it with `keel run`, and saw their requests in `keel traces --json` within one poll: `"local": true`, `GET /users/:id` with 3 spans (server → outgoing fetch → internal server), 404 and 500 statuses, pino lines with the same `trace_id`, a clean SIGTERM exit. On the Observability page both services' requests interleave with `local` tags and open as waterfalls whose resource carries `keel.service_id`. Their remaining notes went into the text too (an unmatched path keeps the bare method; list `@opentelemetry/instrumentation`; run the real command rather than an npm script) and into `keel run`: without a terminal it now gives the command its own process group and signals the whole group, since stopping `keel run -- npm run dev` used to leave `node` running.

Deployed the same day (single-node Swarm on the dev box): Claude's instrumented app as `web`, tracing on, `keel ship web`. The task's env held the eight variables with `deployment.environment.name=production`; the container reached the relay at `http://<tailnet ip>:3211` from the overlay (401 without a key); requests made inside it showed in `keel traces web` with `"local": false`; and the 404 trace opened with its 3 spans and the 2 pino lines the worker shipped from the container, joined by trace id. `keel tracing disable web` + ship took all eight variables off the spec, enable + ship put them back.

