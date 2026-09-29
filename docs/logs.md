# Logs — sinks and providers

> Where container logs go and where the Logs tab reads them from. Decided 2026-09-20. Read this before touching `convex/logs.ts`, `convex/logSinks.ts`, `convex/logProviders/*` or `apps/worker/src/logs.ts`. The worker itself is in [`workers.md`](./workers.md).

## Decision

Logs are a per-project **sink** with a matching **provider**. The sink is the write side: the per-node worker streams every container line of the project there. The provider is the read side: `logs.tail` queries it for the Logs tab. Both sides agree on one event shape (below). The tab never knows which provider answered; it gets `{ source, lines, replicas }` either way.

Two providers exist:

| Kind | Write side | Read side | When |
|---|---|---|---|
| `docker` (default, no row) | nothing shipped | `docker service logs` from the manager socket (`logProviders/docker.ts`) | zero setup, single user poking at a service |
| `axiom` | worker POSTs NDJSON to `/v1/ingest/<dataset>` (`apps/worker/src/sinks/axiom.ts`) | APL over `/v1/datasets/_apl?format=tabular` (`logProviders/axiom.ts`) | retention, search, dashboards, alerts, metrics later |

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
logSink = v.union(v.object({ kind: "axiom", domain, dataset, token }))
logSinks: { projectId, sink }  // by_project, at most one row per project
```

`convex/logSinks.ts`:

- `get(projectId)` — public. Kind, domain, dataset, last four of the token. Never the token.
- `connectAxiom(projectId, domain, dataset, token)` — public action. Validates region (`api.axiom.co` / `api.eu.axiom.co`; a full origin is accepted only with `KEEL_ALLOW_LOCAL_SINKS=1`, for the mock), creates the dataset if missing (`POST /v2/datasets`, 409 ignored), runs `['ds'] | limit 1` to prove the token can query, then saves. A bad token fails here, nothing is stored.
- `disconnect(projectId)` — back to Docker. Shipped data stays in Axiom.
- `forNode` / `owns` / `save` — internal.

`convex/logs.ts tail(nodeId, tail)` — the one read entry point. Looks up the node's project sink and dispatches. `convex/worker.ts config` — what the worker polls: every sink with the service ids it covers.

Token scope: the worker only ingests (a Basic token would do), the control plane creates the dataset and queries it. One Advanced token with ingest + query on the dataset (plus dataset create, org-level, for the first connect) covers both; it is stored once in `logSinks` and reaches the worker through `GET /worker/config`, which is bearer-protected by `KEEL_WORKER_TOKEN`. Axiom has no OAuth app flow for third parties (checked 2026-09-20), so "sign in with Axiom" is not an option; token paste it is.

## UI

Settings palette (`apps/web/src/components/canvas/settings-dialog.tsx`, rail gear or `⌘,`): `Settings → Logs → Docker | Axiom | Disconnect`. Axiom is region → dataset (default `keel-<project>`) → token, each one `↵`; the token page masks the input (`PalettePage.secret`). The Logs tab shows `· via Axiom` in its meta line when the provider is Axiom; everything else is identical.

## Adding a provider (e.g. ClickHouse)

1. `schema.ts`: add a variant to `logSink`.
2. `apps/worker/src/sinks/<kind>.ts`: implement `Sink.send(events)`; register it in `sinks/index.ts buildSink`.
3. `convex/logProviders/<kind>.ts`: `<kind>Tail(cfg, serviceId, n): Tail` and a `<kind>Verify(cfg)`.
4. `convex/logs.ts`: one branch in `tail`. `convex/logSinks.ts`: a `connect<Kind>` action.
5. `settings-dialog.tsx`: a row under Logs.

Nothing else knows the kind.

## Verified (2026-09-20, single node, mock Axiom on `127.0.0.1:4318`)

Connect with a wrong token → `Axiom 403: forbidden`, nothing saved. Connect with the right one → dataset created, row saved. Worker picks the sink up on its next poll (`config applied {"sinks":1,"services":2}`), follows the running containers, and a new nginx node deployed afterwards is followed within 30s (its `container start` event lands before the config poll lists it; the poll catches it). Six nginx requests → 9 events ingested in one batch → Logs tab reads them back via APL with `via Axiom` in the header, `r1` tags, stderr and stdout interleaved by `_time`. Force-restarting the worker resumes both `docker events` and each container tail from the saved `since`: the mock's count did not move until a new request was made, so nothing replayed.

Not there yet: the Logs rail page (cross-service search: Axiom makes it a single APL), retention setting, metrics, the ClickHouse provider, a live stream (the tab still polls `logs.tail` every 3s).
