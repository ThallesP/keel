# Logs — sinks and providers

> Where container logs go and where the Logs tab reads them from. Decided 2026-09-20. Read this before touching `convex/logs.ts`, `convex/logSinks.ts`, `convex/logProviders/*` or `apps/worker/src/logs.ts`. The worker itself is in [`workers.md`](./workers.md).

## Decision

Logs are a per-project **sink** with a matching **provider**. The sink is the write side: the per-node worker streams every container line of the project there. The provider is the read side: `logs.tail` queries it for the Logs tab. Both sides agree on one event shape (below). The tab never knows which provider answered; it gets `{ source, lines, replicas }` either way.

Two providers exist:

| Kind | Write side | Read side | When |
|---|---|---|---|
| `docker` (default, no row) | nothing shipped | `docker service logs` from the manager socket (`logProviders/docker.ts`) | zero setup, single user poking at a service |
| `axiom` | worker POSTs NDJSON to `/v1/datasets/<dataset>/ingest` (`apps/worker/src/sinks/axiom.ts`; `/v1/ingest/<dataset>` only on `*.edge.axiom.co`, it is 404 on `api.axiom.co`) | APL over `/v1/datasets/_apl?format=tabular` (`logProviders/axiom.ts`) | retention, search, dashboards, alerts, metrics later |

Docker is not a real log store: it holds what the node's `json-file` driver kept, every tail is a round-trip to every node running a task, and it is gone with the container. Axiom (or, later, ClickHouse) is where logs live once anyone cares about them.

## Why the worker ships, not a logging driver

Docker has no native "stream all containers on this node" API, and the two ways to get one both cost more than they give:

- A **logging driver** per service (`fluentd`, `gelf`, `awslogs`) would put the sink config in every `ServiceSpec`, make `docker service logs` stop working (the daemon no longer keeps the file), and need a driver that speaks the sink's protocol. Axiom has none.
- **Vector / Fluent Bit** as the global service would work, but it is a second binary to configure per sink kind, and the worker already has the socket, the labels, the config poll and the resume state.

So the worker does `GET /containers/<id>/logs?follow=1` per `svc-*` container (`apps/worker/src/logs.ts`). Followers start on `container start` events and on every config poll (30s), stop on `die`, and resume from a per-container `since` saved in the node's state volume so a worker restart neither replays nor skips. Lines batch per sink (1s or 500 lines) and are sent in order; a slow sink backs up its own queue (capped at 20k lines, oldest dropped) and never another's.

## Event shape

Field names are the contract between `apps/worker/src/sinks/types.ts` and every read provider. Change one, change both.

| Field | Value |
|---|---|
| `_time` | RFC3339Nano from Docker's `timestamps=1` (Axiom indexes on `_time`) |
| `message` | the line, timestamp stripped |
| `stream` | `stdout` \| `stderr` |
| `service_id` | Convex node id (the `svc-<id>` service without the prefix); what `logs.tail` filters on |
| `service` | Swarm service name, `svc-<id>` |
| `task` | Swarm task id, one per replica run; the Logs tab tags lines with it |
| `replica` | Swarm slot, from the task name `svc-<id>.<slot>.<task>` |
| `node` | Swarm node id of the worker |
| `container` | short container id |

## Schema and functions

```ts
// schema.ts
logSink = v.union(v.object({ kind: "axiom", domain, dataset, token, org? }))
logSinks: { projectId, sink }  // by_project, at most one row per project
axiomClients: { redirectUri, clientId }                    // by_redirect; DCR client per callback URL
axiomSignIns: { projectId, clientId, state, verifier, redirectUri }  // by_state, by_project; 10 min, single use
axiomPending: { projectId, token, orgs }                   // by_project; 10 min, single use
```

`convex/logSinks.ts`:

- `get(projectId)` — public. Kind, domain, dataset, org name, last four of the token. Never the token.
- `beginAxiomSignIn` / `signInAxiom` / `chooseAxiomOrg` / `cancelAxiomSignIn` / `pendingOrgs` — Sign in with Axiom, below.
- `connectAxiom(projectId, domain, dataset, token)` — public action, token paste. No UI any more; kept for scripts and agents (`convex run`) and as the fallback if the OAuth client stops working. Validates region (`api.axiom.co` / `api.eu.axiom.co`; a full origin is accepted only with `KEEL_ALLOW_LOCAL_SINKS=1`, for the mock), creates the dataset if missing (`POST /v2/datasets`, 409 ignored), runs `['ds'] | limit 1` to prove the token can query, then saves. A bad token fails here, nothing is stored.
- `disconnect(projectId)` — back to Docker. Shipped data stays in Axiom.
- `forNode` / `owns` / `save` — internal.

`convex/logs.ts tail(nodeId, tail)` — the one read entry point. Looks up the node's project sink and dispatches. `convex/worker.ts config` — what the worker polls: every sink with the service ids it covers.

`convex/logs.ts recent(environmentId, search?, tail)` — the Logs page: last lines across every service of the environment, `where message contains` for search. Axiom only; Docker has no cross-service query.

Token scope: the worker only ingests, the control plane queries. One API token with ingest + query on the dataset covers both; it is stored once in `logSinks` and reaches the worker through `GET /worker/config`, which is bearer-protected by `KEEL_WORKER_TOKEN`.

## Sign in with Axiom

Axiom has no app console for third-party OAuth clients, but the authorization server behind its MCP server does Dynamic Client Registration (`mcp.axiom.co/.well-known/oauth-authorization-server` → `authorization.axiom.co`, `/oauth2/{register,authorize,token}`). Its tokens are accepted by `api.axiom.co` (verified 2026-09-29). Two dead ends first: the 2026-09-20 note here said "no OAuth at all", and the CLI's `login.axiom.co` client (`axiom auth login`) only accepts loopback redirects (`redirect_uri is not permitted for this client`).

1. Logs page → Sign in with Axiom → `beginAxiomSignIn(projectId, <origin>/axiom/callback)`. First time for that callback URL, Keel registers a public client (`client_name: Keel`, `token_endpoint_auth_method: none`) and caches its id in `axiomClients`. Then it makes verifier + state (server-side: the browser may be on plain http, where `crypto.subtle` does not exist), stores them in `axiomSignIns`, and returns the authorize URL. The browser keeps only the project slug to come back to.
2. Axiom redirects to `/axiom/callback?code&state` → `signInAxiom(state, code)`: takes the row (must belong to the caller), exchanges the code, lists `/v2/orgs`.
3. One org: provision now. Several: the user token waits in `axiomPending` and the Logs page shows an org picker → `chooseAxiomOrg`.
4. Provision, with the user token + `x-axiom-org-id`: create `keel-<project slug>` (409 ignored), mint an API token with `ingest:create` + `query:read` on that dataset only (`POST /v2/tokens`), prove it can query, save the sink. The user token is dropped; only the scoped token is stored.

**Keel must be served over https** (or `http://localhost`): DCR refuses a plain-http IP redirect URI (`invalid_redirect_uri`). Dev: Tailscale Serve (`scripts/dev-https.sh`). An installed Keel reached by tailnet IP needs the same before Sign in with Axiom works; `connectAxiom` (token paste) is the fallback.

Region: org `defaultEdgeDeployment` containing `eu-` → `api.eu.axiom.co`, else `api.axiom.co`. Only US verified.

A new dataset has no fields, and APL rejects `where service_id …` with 400 `invalid field` until the first line lands; the tail queries read that as "no lines yet".

Mock for local testing: `KEEL_ALLOW_LOCAL_SINKS=1` plus `KEEL_AXIOM_AUTH_URL` and `KEEL_AXIOM_API_URL` (full origins; ignored without the first flag) point the flow and every API call at a mock.

## UI

Rail → Logs (`?view=logs`, `apps/web/src/components/canvas/logs-page.tsx`), drawn over the canvas (which stays mounted, `inert`). Without an Axiom sink it is a gate: a blurred fake stream behind a card, "Hey, to access this, set up Axiom", Sign in with Axiom (or the org picker while one is pending). With one: every service's lines interleaved, service name per line, stderr in the warning tone, search box, following toggle, Disconnect (back to Docker; shipped data stays in Axiom). Polls `logs.recent` every 3s.

The per-service Logs tab in the bottom panel works either way and shows `· via Axiom` in its meta line when the provider is Axiom.

## Adding a provider (e.g. ClickHouse)

1. `schema.ts`: add a variant to `logSink`.
2. `apps/worker/src/sinks/<kind>.ts`: implement `Sink.send(events)`; register it in `sinks/index.ts buildSink`.
3. `convex/logProviders/<kind>.ts`: `<kind>Tail(cfg, serviceId, n): Tail` and a `<kind>Verify(cfg)`.
4. `convex/logs.ts`: one branch in `tail`. `convex/logSinks.ts`: a `connect<Kind>` action.
5. `logs-page.tsx`: a way to connect it on the gate.

Nothing else knows the kind.

## Verified (2026-09-20, single node, mock Axiom on `127.0.0.1:4318`)

Connect with a wrong token → `Axiom 403: forbidden`, nothing saved. Connect with the right one → dataset created, row saved. Worker picks the sink up on its next poll (`config applied {"sinks":1,"services":2}`), follows the running containers, and a new nginx node deployed afterwards is followed within 30s (its `container start` event lands before the config poll lists it; the poll catches it). Six nginx requests → 9 events ingested in one batch → Logs tab reads them back via APL with `via Axiom` in the header, `r1` tags, stderr and stdout interleaved by `_time`. Force-restarting the worker resumes both `docker events` and each container tail from the saved `since`: the mock's count did not move until a new request was made, so nothing replayed.

Sign in with Axiom verified 2026-09-29 against a mock (authorize auto-approves, PKCE checked on `/oauth/token`, `/v2/orgs|datasets|tokens`, APL): two orgs → picker → pick → dataset + scoped token minted with the org header, sink saved with only the scoped token; one org → connected straight from the callback; Logs page shows only lines of the environment's services (a foreign `service_id` in the same dataset is filtered out), search narrows via APL, Disconnect returns to the gate. Against real Axiom (2026-09-29, US org, Keel on `https://dev.<tailnet>.ts.net`): DCR, sign-in, org listing, dataset + scoped token creation all worked first try. The worker's ingest path was wrong (`/v1/ingest/<dataset>` → 404 on `api.axiom.co`; the mock had accepted it), fixed to `/v1/datasets/<dataset>/ingest`; nginx requests then showed up in the dataset within seconds, stdout and stderr, with `service`/`replica` set.

Not there yet: retention setting, metrics, the ClickHouse provider, a live stream (the tab still polls `logs.tail` every 3s).
