# Porting spec: Swarm reconciler and the per-node worker

> Area: how the control plane drives Docker Swarm (apply / remove / observe), how deployments settle, and the per-node worker (`apps/worker`, Swarm global service `keel-worker`) that forwards Docker events and ships container logs. Target: one Go binary (control plane + per-node agent + CLI).
>
> Sources read (worktree HEAD `36c2ded`): `packages/backend/convex/{swarm.ts, worker.ts, nodesInternal.ts, reconcile.ts, migrations.ts, crons.ts, schema.ts, events.ts, http.ts, deployments.ts, status.ts, environments.ts, nodeHelpers.ts, variables.ts (computeEnv), tracing.ts (withTracing), access.ts, endpoints.ts, logSinks.ts (sinkOf), nodes.ts (callers), proxyInternal.ts (resync), logProviders/docker.ts}`, `apps/worker/**`, `docs/workers.md`, `scripts/bootstrap-swarm.sh`, `scripts/deploy-worker.sh`, `install.sh`, `deploy/compose.yml`, `deploy/functions-entrypoint.sh`, dockerode 5.0.1 + docker-modem 5.0.7 sources (for the exact HTTP calls).
>
> Neighbouring specs own: endpoints / keel-proxy / Caddy admin API (`proxy.sync`), variables (`computeEnv` reference syntax), tracing env, log read side (`logs.tail`, Axiom APL), node CRUD mutations, auth. This file references them where they meet.

---

## 0. What is implemented vs. only designed

| Thing | State at HEAD | Go port |
| --- | --- | --- |
| Swarm manager on the control-plane box, control plane talks to `/var/run/docker.sock` | Implemented | Port exactly |
| `apply` / `remove` / `observeNode` / `observe` / `observeSwarmNodes` / `removeLegacyTunnels` | Implemented (`swarm.ts`) | Port exactly |
| Event-driven observation (`POST /worker/events` → debounced scan) | Implemented | Port exactly |
| Deployment step settling (`reconcile.run`) + 5 min timeout | Implemented | Port exactly |
| Per-node worker: events forwarder + log shipper + `GET /worker/config` | Implemented (`apps/worker`) | Becomes the Go binary's agent mode |
| `cluster` table (ready Swarm node count) | Implemented | Port |
| **Server (machine) join flow**, `servers`/Swarm-node table, join secret, `GET /join/<secret>`, pre-flight | **Not implemented.** Only in `docs/workers.md` "Node join flow" + "Schema" (design). There is **no** `servers` or `workers` table in `schema.ts`. Joining today = run `docker swarm join` by hand on the new box; Swarm then schedules `keel-worker` there automatically (global service). | New feature; design target in §15 |
| Placement constraints / pin to node, volume mounts, `stop-first`, registry, builds | **Not implemented** (designed in `workers.md`, `volumes.md`) | Out of scope; spec must not emit them |
| Cron in this area | Only `keel-proxy resync` (2 min) → `proxyInternal.resync` | Port (§16) |

Terminology trap: in Keel's schema a **`nodes` row is a canvas node** (service / database / cache / volume / group), not a Swarm node. Swarm machines are "servers" in UI copy ("N servers" in the status bar) and "Swarm nodes" in Docker. This spec says *node* = canvas node row, *server* = Swarm node.

---

## 1. Topology

```
control-plane box (Swarm manager, the ONLY manager)
 ├─ Convex backend container  ── /var/run/docker.sock (rw, root-equivalent) ── dockerode
 │     (Go: the keel control-plane process mounts the same socket)
 ├─ web, keel-proxy (compose; proxy joins the `keel` overlay)
 ├─ keel-worker task (global service, host network, socket mounted :ro)
 └─ user services  svc-<nodeId>  (Swarm replicated services on overlay `keel`)
other servers (Swarm workers, joined over Tailscale)
 ├─ keel-worker task
 └─ user service tasks
```

- Swarm init (bootstrap): advertise/listen/data-path on the tailnet IP, `--default-addr-pool 10.200.0.0/16 --default-addr-pool-mask-length 24`.
- Overlay network name: **`keel`**, `-d overlay --attachable --opt com.docker.network.driver.mtu=1200`.
- Service DNS on the overlay: `svc-<nodeId>`; no host ports are ever published (no `EndpointSpec`).
- Control plane is single-writer; one control plane per Swarm. A second control plane sharing the Swarm is not supported (events for `svc-<id>` it does not know are ignored, but nothing garbage-collects foreign/orphan services; dev note: a second backend's cleanups hit the other's services).

---

## 2. Data touched by this area

### 2.1 `nodes` (only fields this area reads/writes)

| Field | Type | Req | Meaning / writer |
| --- | --- | --- | --- |
| `_id` | string (Convex id) | yes | Node id. **Embedded in Swarm names/labels** (`svc-<_id>`, `keel.service=<_id>`). A Go migration from Convex must keep ids byte-identical or rename live Swarm services. |
| `environmentId` | id → environments | yes | Index `by_environment`. |
| `type` | `"service"\|"database"\|"cache"\|"volume"\|"group"` | yes | Only service/database/cache carry `desired` and reach Swarm (`DEPLOYABLE`). |
| `name` | string | yes | Step label in deployments; `OTEL_SERVICE_NAME`. |
| `desired` | object, optional | no | What the user asked for. Written only by user-action mutations (and `beginDeployment` bumps `revision`). Shape below. |
| `observed` | object, optional | no | What Swarm reports. Written **only** by `setObserved` (from `observeNode` / `observe`). Replaced wholesale on every scan. |
| `deployedRevision` | number, optional | no | Last revision observe saw fully converged (`setObserved`). |
| `dirty` | boolean, optional | no | Staged changes; cleared by `beginDeployment`; set by migrations (Redis password). |
| `shippedAt` | number (ms), optional | no | Set by `beginDeployment` to now. Drives "deploying · 41s". |
| `applyError` | string, optional | no | Last `apply` failure text or deployment-timeout text. Cleared (field removed) by `beginDeployment` and by a successful `apply`. |
| `oneShot` | boolean, optional | no | Learned by `setObserved`: last run exited 0 and nothing kept running. Read by `apply` → `FailureAction: "continue"`. |
| `observeScheduled` | id → `_scheduled_functions`, optional | no | Debounce handle of the pending `observeNode`. Internal; not in any view. Go: in-memory timer map is fine (§8.2). |
| `endpoints` | array, optional | no | keel-proxy endpoints. This area only touches them in `followPort` (§6.5) and migrations (§16). |
| `public`, `ingress` | any, optional | no | Legacy Quick Tunnel fields; only `migrations.run` reads/clears them. |

`desired` (object):

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `image` | string | yes | Image ref, validated by `^[a-z0-9][a-z0-9._\-/:@]{0,199}$` at write time ("Image must look like repo/name:tag"). |
| `revision` | number | yes | `0` = never shipped. `+1` per deployment that includes the node. Goes into label `keel.revision`. |
| `replicas` | number | yes | 0–20. `0` = stopped. |
| `port` | number | no | Container port (1–65535). Not published; used for env/endpoints. |
| `tracing` | boolean | no | service only; when true `apply` appends OTEL_* env (tracing spec). |

`observed` (object) — **exact JSON field names**:

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `revision` | number | yes | max(service spec label `keel.revision`, every task's label). 0 when service and tasks are gone. |
| `running` | number | yes | Tasks of `revision` with `DesiredState=running` and `Status.State=running`. |
| `completed` | number | only when `state="completed"` | Tasks of `revision` in state `complete`. |
| `finishedAt` | number (ms) | only when `state="completed"` | max `Date.parse(Status.Timestamp)` over those tasks (unparseable → 0). |
| `state` | `"ok"\|"updating"\|"crashloop"\|"pending"\|"failed"\|"completed"` | yes | See §8.4. |
| `nodeIds` | string[] | yes | Distinct Swarm node ids of running tasks (first-seen order). |
| `error` | string | no | Last failed task's `Status.Err`, else (when rolled back) `UpdateStatus.Message`. |
| `at` | number (ms) | yes | Scan time (`Date.now()`). |

> **Go now:** `domain.Observed` has no `nodeIds` (written on every scan, never read); `completed` and `finishedAt` are plain ints, 0 when unset, and `finishedAt` comes from the task's typed `Status.Timestamp`.

### 2.2 `deployments`

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `environmentId` | id | yes | Indexes `by_environment` (`["environmentId"]`) and `by_status` (`["status"]`). |
| `sha` | string | no | Unused today. |
| `message` | string | yes | `"<verb> <name>, <name>…"`; verb ∈ `ship` / `deploy` / `redeploy` / `stop` / `start`. `reconcile.run` checks `message.startsWith("stop ")`. |
| `status` | `"running"\|"success"\|"failed"` | yes | |
| `startedAt` | number | yes | |
| `finishedAt` | number | no | Set when status leaves `running`. |
| `steps` | DeployStep[] | yes | One per affected node (order of `nodes` by_environment index, i.e. creation order) **plus a final `{label:"health checks"}` step without `nodeId`**. |
| `log` | `{at:number, nodeId?:id, text:string}[]` | yes | Capped to the **last 500** entries on every write (`MAX_LOG`). |

`DeployStep`: `nodeId?` (absent only for "health checks"), `label` (node name at ship time), `status` (`pending\|running\|done\|failed`), `startedAt?`, `appliedAt?` (set when `apply` created/updated the service — observe takes over from there), `finishedAt?`.

### 2.3 `cluster`

Single row: `{ servers: number, at: number }`. `servers` = count of Swarm nodes with `Status.State == "ready"`. Upserted by `environments.setServers`. Read by `environments.summary` (status bar "N servers").

### 2.4 Read-only from this area

- `variables` (by_node) → env via `computeEnv` (own rows in index order, i.e. `_creationTime` asc, each `KEY=<value with ${{ }} references expanded>`; *provided* keys like HOST/PORT/URL/DATABASE_URL are **not** injected unless referenced). Migrations insert `REDIS_PASSWORD`.
- `logSinks`, `projects`, `environments` → `worker.config`.
- `otlpKeys`, `environments` → `withTracing`.

---

## 3. Constants

| Name | Value | Where | Meaning |
| --- | --- | --- | --- |
| `NETWORK` | `"keel"` | swarm.ts | Overlay every service attaches to. |
| service name | `svc-<nodeId>` | swarm.ts, variables.ts, worker | Swarm service name and overlay DNS name. |
| `SERVICE_LABEL` | `keel.service` | swarm.ts | `= <nodeId>` on service and container spec. |
| `REVISION_LABEL` | `keel.revision` | swarm.ts | `= String(desired.revision)` on service and container spec. |
| `LEGACY_TUNNEL_LABEL` | `keel.ingress` | swarm.ts | Label of old `cloudflared` services, removed by `removeLegacyTunnels`. |
| RestartPolicy | `on-failure`, Delay `5_000_000_000` ns, MaxAttempts `5` | toSpec | |
| `SETTLE_MS` | 2000 | swarm.ts | Re-scan delay while a task is mid-transition. |
| `SETTLE_MAX` | 2 | swarm.ts | Max settle re-scans per chain. |
| `OBSERVE_DEBOUNCE_MS` | 500 | nodesInternal.ts | Event → scan debounce. |
| `DEPLOY_TIMEOUT_MS` | 300000 (5 min) | deployments.ts | One timeout check per deployment. |
| `MAX_LOG` | 500 | deployments.ts, reconcile.ts | Deployment log cap. |
| `MAX_BODY` | 262144 (256 KiB, counted in JS string length) | http.ts | `/worker/events`, `/proxy/events`. |
| crashloop threshold | `failed.length >= 5` | summarize | Failed/rejected tasks of the current revision (all slots). |
| Worker `CONFIG_POLL_MS` | env `KEEL_CONFIG_POLL_MS`, default 30000 | worker | |
| Worker `FLUSH_MS` / `FLUSH_LINES` / `RETRY_MS` / `MAX_QUEUE` | 1000 / 500 / 5000 / 20000 | worker logs.ts | |
| Worker events retry | sleep `n*5000+5000` ms, `n` capped at 6 (5 s … 35 s) | worker controlPlane.ts | |
| Worker events reconnect | 2000 ms | worker events.ts | |
| Worker follower retry | 3000 ms | worker logs.ts | |
| Worker config refresh throttle | 5000 ms | worker logs.ts | |
| Worker HTTP timeouts | 10 s (control plane), 15 s (Axiom) | worker | |
| Worker shutdown flush budget | 5000 ms | worker index.ts | |
| Swarm `UpdateConfig.Monitor` | not set → Swarm default 5 s | toSpec | Matters for rollback and `updating` state. |
| Swarm task history | not set → Swarm default `TaskHistoryRetentionLimit` 5 | bootstrap | Bounds how many failed tasks are visible. |

---

## 4. How the control plane talks to Docker

- **Transport:** Docker Engine API over the unix socket `/var/run/docker.sock` (hard-coded in `swarm.ts` and `logProviders/docker.ts`; no env var). The socket is bind-mounted into the control-plane container (`deploy/compose.yml` `backend.volumes`). Root-equivalent; every function that touches it is internal-only.
- **Client:** `dockerode` 5.x (`new Docker({ socketPath })`), which calls **unversioned paths** (`/services/create`, not `/v1.47/...`), so the daemon answers with its default (latest) API version. No `X-Registry-Auth` header is ever sent. Image refs are passed **as-is** (no client-side digest pinning; API ≥ 1.30 does not query the registry server-side either).
- **Go recommendation:** `github.com/docker/docker/client` (or `github.com/moby/moby/client`) with `client.WithHost("unix:///var/run/docker.sock")` + `client.WithAPIVersionNegotiation()`. Use `QueryRegistry: false` on `ServiceCreate`/`ServiceUpdate` to keep tags unpinned (same as today). Consider an env override (e.g. `KEEL_DOCKER_SOCKET`, default `/var/run/docker.sock`) — new, optional.

> **Go now:** `client.New(client.FromEnv)` in `internal/adapters/swarm` (and in the agent): `DOCKER_HOST` is the only override, default `unix:///var/run/docker.sock`; no Keel socket variable.

### 4.1 Every Docker call made by the control plane

| # | Caller | Method + path | Query / body | Success | Error handling |
| --- | --- | --- | --- | --- | --- |
| D1 | `apply` cache check | `GET /images/{image}/json` | — | 200 → cached | 404 → "not cached" (null). Other → throws (apply fails). |
| D2 | `apply` pull | `POST /images/create?fromImage=<repo>&tag=<tag>` | `repo`/`tag` split by dockerode `parseRepositoryTag`: if `@` present split at first `@` (tag = digest, e.g. `sha256:…`); else at last `:` unless the part after it contains `/` (then whole string is repo, tag=`latest`); no separator → tag `latest`. Response is a JSON-lines progress stream read to EOF. | stream ends | Non-200 HTTP → reject. **In-stream `{"error":…}` messages are NOT treated as errors** by docker-modem `followProgress` (it only fails on socket/stream errors). See §19 Q1. |
| D3 | `createOrUpdate` | `GET /services/svc-<id>` | — | 200 → existing (need `Version.Index`) | 404 → null; other → throws (not retried). |
| D4 | `createOrUpdate` create | `POST /services/create` | body = spec (§5) | 200/201 | → retry loop (§6.2) |
| D5 | `createOrUpdate` update | `POST /services/svc-<id>/update?version=<Version.Index>` | body = spec (§5), full replace | 200 | → retry loop |
| D6 | `apply` orphan take-back, `remove` | `DELETE /services/svc-<id>` | — | 200/204 | 404 ignored; other → throws |
| D7 | `removeLegacyTunnels` | `GET /services?filters={"label":["keel.ingress"]}` then `DELETE /services/<ID>` each | — | | 404 on delete ignored |
| D8 | `observeNode` | `GET /services/svc-<id>` (D3) **and** `GET /tasks?filters={"label":["keel.service=<id>"]}` in parallel | — | | service 404 → null; tasks errors throw |
| D9 | `observe` sweep | `GET /services?filters={"label":["keel.service"]}` and `GET /tasks?filters={"label":["keel.service"]}` in parallel | — | | throw |
| D10 | `observeServers` | `GET /nodes` | — | | throw |
| D11 | `logs.tail` (docker provider; other spec) | `GET /services/svc-<id>/logs?stdout=1&stderr=1&tail=<n>&timestamps=1&details=1` + `GET /tasks?filters={"service":["svc-<id>"]}` | — | | 404 → empty |

`filters` are JSON-encoded objects in the query string (docker-modem `buildQuerystring` JSON-stringifies object values).

### 4.2 Error text format (user-visible via `applyError` and the deploy log)

dockerode errors read: `(HTTP code <status>) <reason> - <cause> ` where `<cause>` = response JSON `message` (else `error`, else raw body) and `<reason>` comes from a per-call map, else `unexpected`:

| Call | Mapped reasons |
| --- | --- |
| image inspect | 404 `no such image`, 500 `server error` |
| image create (pull) | 500 `server error` (404 → `unexpected`) |
| service create | 500 `server error` |
| service inspect | 404 `no such service`, 500 `server error` |
| service update | 404 `no such service`, 500 `server error` |
| service remove | 404 `no such service`, 500 `server error` |
| list services / tasks | 500 `server error` |
| list nodes | 400 `bad parameter`, 404 `no such node`, 500 `server error`, 503 `node is not part of a swarm` |

Socket failures surface as Node errors (e.g. `connect ENOENT /var/run/docker.sock`). All texts pass through `errorText`:

```ts
const errorText = (err) => (err instanceof Error ? err.message : String(err)).replace(/\s+/g, " ").trim().slice(0, 300);
```

These strings are display-only (no CLI code maps them). The Go port should keep the `(HTTP code N) reason - message` shape so existing `applyError` values and docs stay recognisable; exact parity is not a contract.

> **Go now:** the text is the moby client's error passed through `app.CompactText(text, 300)`: whitespace runs collapsed (`strings.Fields`), invalid UTF-8 replaced, cut at 300 runes. The dockerode shape is not rebuilt.

---

## 5. The service spec (`toSpec`)

Inputs: `id` (node id), `desired`, `env: string[]` (`KEY=value`), `oneShot: boolean`.

```json
{
  "Name": "svc-<id>",
  "Labels": { "keel.service": "<id>", "keel.revision": "<desired.revision>" },
  "TaskTemplate": {
    "ContainerSpec": {
      "Image": "<desired.image>",
      "Env": ["KEY=value", "..."],
      "Args": ["redis-server", "--requirepass", "<pass>"],
      "Labels": { "keel.service": "<id>", "keel.revision": "<desired.revision>" }
    },
    "RestartPolicy": { "Condition": "on-failure", "Delay": 5000000000, "MaxAttempts": 5 },
    "Networks": [{ "Target": "keel" }]
  },
  "Mode": { "Replicated": { "Replicas": <desired.replicas> } },
  "UpdateConfig": {
    "Parallelism": 1,
    "Order": "start-first",
    "FailureAction": "rollback"   // "continue" when node.oneShot === true
  }
}
```

Rules:

- `Args` is **present only** when `engineOf(image) === "redis"` **and** env has a non-empty `REDIS_PASSWORD=` entry (first match, value = text after `REDIS_PASSWORD=`). Otherwise the key is omitted (JSON.stringify drops `undefined`). The image's entrypoint then runs `redis-server --requirepass <pass>`.
- `engineOf(image)`: `image.split("@")[0].split("/").pop().split(":")[0]` ∈ {`postgres`,`mysql`,`mongo`,`redis`} else undefined. (`bitnami/redis:7` → `redis`.)
- `Env` order: own variables (creation order) expanded, then, if `desired.tracing`, the OTEL_* entries not overridden by own keys (tracing spec): `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<key>`, `OTEL_SERVICE_NAME=<name>`, `OTEL_RESOURCE_ATTRIBUTES=keel.service_id=<id>,keel.environment_id=<envId>,deployment.environment.name=<envName>` (values `encodeURIComponent`-ed), `OTEL_TRACES_EXPORTER=otlp`, `OTEL_METRICS_EXPORTER=none`, `OTEL_LOGS_EXPORTER=none`. Endpoint = `KEEL_OTLP_URL` or `${CONVEX_SITE_URL}/otlp`, trailing `/` stripped.
- Deliberately **absent**: `EndpointSpec` (zero published ports), `Placement`, `Mounts`, `Resources`, `HealthCheck` (image's own HEALTHCHECK still applies), `Secrets`, `LogDriver`, `ForceUpdate`, `RollbackConfig`, `UpdateConfig.Monitor/Delay` (Swarm defaults), `Hostname`, `Command`.
- Update is a **full replace** of the spec: any drift made by hand on the service is reverted at the next apply.
- Every deployment bumps `keel.revision` in `ContainerSpec.Labels`, so every apply of an existing service is a spec change → Swarm rolls new tasks (that is what "Redeploy"/"Run again" relies on).
- `oneShot` rationale: Swarm treats an exit 0 inside the 5 s Monitor window as a failed update; with `rollback` every Redeploy of a one-shot image rolled back (`rollback_completed` at ~t+2 s). Mode stays `Replicated` (Job mode was rejected: mode can't be changed after create, jobs reject `UpdateConfig`).
- `start-first` is only safe for stateless services (volumes not implemented).

---

## 6. `apply` — desired → Swarm

`internalAction swarm.apply({ id: nodes id, deploymentId?: deployments id, pull?: boolean = false })`. Returns nothing. Scheduled only by `beginDeployment` (always with `deploymentId`; `pull = refresh`).

### 6.1 Algorithm (exact)

```
input = applyInput(id)            // null if node missing OR node.desired missing
if input == null: return          // nodes.remove's reconcile handles the step
{desired, env, oneShot} = input
step(fn, text) = deploymentId ? deployments.<fn>({deploymentId, nodeId:id, text}) : no-op
stillWanted() = applyInput(id) != null

try:
  cached = D1(desired.image)  (404 → null)
  if cached && !pull:
      step(stepRunning, "using cached <image>")
  else:
      step(stepRunning, "pulling <image>")
      t0 = now
      try:
          D2 pull(desired.image) to EOF
          step(stepLog, "pulled <image> in <((now-t0)/1000).toFixed(1)>s")
      catch err:
          if !cached: throw err
          step(stepLog, "pull failed (<errorText(err)>), using cached image")
  if !stillWanted(): return                       // deleted during the pull; silent
  created = createOrUpdate(toSpec(id, desired, env, oneShot))
  if created && !stillWanted():                   // deleted between check and create
      D6 DELETE svc-<id> (404 ignored); return    // silent
  step(stepApplied, created ? "service created · <desired.replicas> replica(s)"
                            : "service updated · revision <desired.revision>")
  setApplyError(id, undefined)                    // clears the field
  if desired.port: followPort(id, desired.port)
catch err:
  text = errorText(err)
  setApplyError(id, text)
  step(stepFailed, "error: <text>")
  return
scheduleObserve(id)    // delay 500 ms, debounced (§8.2); guarantees a scan after stepApplied
```

Notes:

- `·` is U+00B7 MIDDLE DOT surrounded by spaces. `toFixed(1)` = one decimal, JS rounding.
- `desired`/`env`/`oneShot` are read at **run time** (not passed in args), so two quick ships converge on the latest values regardless of scheduling order.
- `applyInput(id)` = `{ name, desired, env: withTracing(node, computeEnv(node)), oneShot: node.oneShot ?? false }` or null.
- The manager's image cache decides "cached". On multi-server clusters, other servers' Swarm executors pull the tag themselves when their task starts.
- `nodes.remove` deletes the row **before** scheduling `swarm.remove` (same transaction), so `stillWanted()` is authoritative.

> **Go now:** the pull time is `fmt.Sprintf("%.1f")`, not `toFixed(1)`; applies of one node run in order through a per-node queue on `App`, and a newer revision cancels a running older apply (`superseded by revision N`).

### 6.2 `createOrUpdate(spec) → created:boolean`

```
for attempt = 0..:
  existing = D3 inspect (404 → null; other errors THROW immediately, not retried)
  try:
    if !existing: D4 create(spec) else D5 update(version=existing.Version.Index, spec)
    return existing == null
  catch err:
    if attempt >= 2: throw err          // 3 attempts total, no delay between them
```

Purpose: two applies racing on one service; the loser gets Swarm's `update out of sequence` and re-reads the version. Any create/update error is retried the same way (e.g. a `409 name conflict` on create when another apply created it first → next attempt sees it and updates).

### 6.3 Step writers (`deployments.stepRunning|stepLog|stepApplied|stepFailed`, internal mutations)

Args for all: `{ deploymentId: id, nodeId: id, text: string }`. Shared `patchStep(deploymentId, nodeId, change, text)`:

```
d = get(deploymentId); if !d: return
failed = change.status == "failed"
steps = d.steps.map(s =>
   s.nodeId == nodeId ? {...s, ...change}
 : failed && !s.nodeId ? {...s, status:"failed", finishedAt: now}   // health checks step fails too
 : s)
log = text ? [...d.log, {at: now, nodeId, text}].slice(-500) : d.log
patch(d, {steps, log, ...(failed && d.status == "running" ? {status:"failed", finishedAt: now} : {})})
```

| Writer | `change` |
| --- | --- |
| `stepRunning` | `{status:"running", startedAt: now}` |
| `stepLog` | `{}` (log only) |
| `stepApplied` | `{appliedAt: now}` (status stays `running`) |
| `stepFailed` | `{status:"failed", finishedAt: now}` → also fails the health step and the deployment |

They patch even when the deployment is no longer `running` (e.g. after a timeout); they never revive a finished deployment. Other node steps of a deployment failed this way stay `pending`/`running` forever (reconcile only looks at running deployments) — UI copes.

### 6.4 `setApplyError` (internal mutation)

`{ id, error?: string }` → if node exists, `patch(id, { applyError: error })` (undefined removes the field).

### 6.5 `followPort` (internal mutation)

`{ id, port: number }`. `moves(e) = !e.pinnedPort && e.port !== port`. If node missing or no endpoint moves → no-op. Else every moving endpoint gets `{...e, port, status: {state:"starting", at: now}}` and `proxy.sync` is scheduled (`runAfter 0`). (Endpoints/proxy spec owns the rest.)

### 6.6 `remove` (internal action)

`{ id }` → `DELETE /services/svc-<id>`, 404 ignored, other errors thrown (logged by the runtime, not retried). Scheduled by `nodes.remove` only when the deleted node had `desired`, together with `reconcile.run` (and `proxy.sync` if it had endpoints). `nodes.remove` also cancels the pending `observeScheduled`.

### 6.7 `removeLegacyTunnels` (internal action)

`{}` → D7; deletes every service labelled `keel.ingress`; returns the count. Scheduled by `migrations.run` on every install.

> **Go now:** dropped. No install carries Quick Tunnel services, so nothing removes them.

---

## 7. Who schedules what (call graph)

| Trigger | Schedules |
| --- | --- |
| `beginDeployment` (from `deployments.start`, `nodes.create{deploy}`, `nodes.stop`, `nodes.start`) | `swarm.apply({id, deploymentId, pull: refresh})` at +0 for each affected node; `reconcile.timeoutDeployment({deploymentId})` at +300000 ms |
| `swarm.apply` success | `nodesInternal.scheduleObserve({id})` (debounced +500 ms); `proxy.sync` via `followPort` when endpoints move |
| `POST /worker/events` → `events.ingest` | `swarm.observe` at +0 when `resync`; `swarm.observeSwarmNodes` at +0 once per batch with any `node` event; debounced `swarm.observeNode` per node id |
| `swarm.observeNode` | itself again at +2000 ms with `settle+1` while tasks are mid-transition (max 2) |
| `nodes.remove` | `swarm.remove`, `reconcile.run`, `proxy.sync` (if endpoints); cancels `observeScheduled` |
| `migrations.run` (every install/upgrade, `keel-functions deploy`) | `swarm.removeLegacyTunnels`, `proxy.sync` |
| cron every 2 min | `proxyInternal.resync` → `proxy.sync` if any node has endpoints |

---

## 8. Observation: Docker → `observed`

### 8.1 `events.ingest` (internal mutation)

Args: `{ events: DockerEvent[], resync?: boolean }`, `DockerEvent = { type: string, action: string, name?: string, serviceName?: string, time?: number }`.

```
if resync: runAfter(0, swarm.observe)                 // full sweep
seen = {}
for e in events:
  if e.type == "node":
      if "node" not in seen: seen.add("node"); runAfter(0, swarm.observeSwarmNodes)
      continue
  if e.type not in {"container","service"}: continue
  name = e.type == "container" ? e.serviceName : e.name
  if !name?.startsWith("svc-"): continue
  id = name.slice(4)
  if id in seen: continue; seen.add(id)
  scheduleObserveFor(id)            // ignores ids that are not a node of ours
```

Every container action (create/start/die/kill/stop/destroy/oom/…) and service action (create/update/remove) that names `svc-*` triggers a scan; the worker already filtered `exec_*` and `health_status*`.

> **Go now:** `app.DockerEvent` is `{Type, Name, ServiceName}`; `action` and `time` are not read. `IngestWorkerEvents` schedules `observeServers` once, before the loop, when the batch has any `node` event.

### 8.2 Debounce: `scheduleObserveFor(rawId, {delayMs = 500, settle?})` → boolean

```
id = normalizeId("nodes", rawId)          // Go: look up by id; unknown/malformed → return false
node = get(id); if !id || !node?.desired: return false
pending = node.observeScheduled && scheduledFunction(node.observeScheduled)
if pending.state == "pending":
   if pending.scheduledTime <= now + delayMs: return false   // an earlier-or-equal scan will cover it
   cancel(pending)                                           // a later one (settle re-check) is replaced
scheduled = runAfter(delayMs, swarm.observeNode, {id, settle})
patch(id, {observeScheduled: scheduled})
return true
```

- An event arriving while a settle re-check (+2 s) is pending replaces it with a +500 ms scan **with `settle` undefined (counter resets to 0)**.
- A scan that is `inProgress` is not `pending`, so a new event schedules a fresh scan.
- Internal mutation wrappers: `nodesInternal.scheduleObserve({id, delayMs?, settle?})` → same function; `nodesInternal.clearObserveScheduled({id})` → if node exists, `patch(observeScheduled: undefined)`.
- Go: keep this per node in memory (`map[nodeID]*pendingScan{due time.Time, timer}`) guarded by a mutex; no DB field needed. Known benign race in TS (clear runs after a newer schedule → one extra scan) need not be reproduced. On process start, run one full sweep (§8.5) instead of restoring timers.

### 8.3 `observeNode` (internal action)

Args `{ id, settle?: number = 0 }`.

```
clearObserveScheduled(id)                       // FIRST: events from now on get their own scan
[service, tasks] = parallel(D3 inspect svc-<id> (404→null),
                            GET /tasks?filters={"label":["keel.service=<id>"]})
observed = summarize(tasks, service)
setObserved(id, observed)
reconcile.run()                                 // separate transaction
if settle < 2 && settling(tasks, observed.revision):
   scheduleObserve(id, delayMs: 2000, settle: settle + 1)
```

`settling(tasks, revision)` = some task with `Number(label keel.revision) == revision` AND `DesiredState != "shutdown"` AND `Status.State ∈ TRANSIENT`, where `TRANSIENT = {new, allocated, assigned, accepted, preparing, ready, starting}` (`pending` deliberately excluded: that is the deployment timeout's job). Rationale: Swarm reports `starting` ~100 ms after the daemon's `container start` event.

### 8.4 `summarize(tasks, service) → observed` (exact)

Task fields used: `NodeID?`, `DesiredState`, `Status.State`, `Status.Err?`, `Status.Timestamp?`, `Spec.ContainerSpec.Labels`. Service fields: `Spec.Name`, `Spec.Labels`, `UpdateStatus.State?`, `UpdateStatus.Message?`.

```ts
const update = service?.UpdateStatus?.State;
const rolledBack = update === "paused" || (update?.startsWith("rollback") ?? false);
const revision = Math.max(
  Number(service?.Spec?.Labels?.["keel.revision"] ?? 0),
  ...tasks.map((t) => Number(label(t, "keel.revision") ?? 0)),
);
const current = tasks.filter((t) => Number(label(t, "keel.revision")) === revision); // no label → excluded
const live = current.filter((t) => t.DesiredState === "running");
const running = live.filter((t) => t.Status.State === "running");
const failed = current.filter((t) => t.Status.State === "failed" || t.Status.State === "rejected");
const completed = current.filter((t) => t.Status.State === "complete");
const oneShot = completed.length > 0 && live.length === 0 && failed.length === 0;
const finishedAt = Math.max(...completed.map((t) => Date.parse(t.Status.Timestamp ?? "") || 0));
return {
  revision,
  running: running.length,
  ...(oneShot && { completed: completed.length, finishedAt }),
  nodeIds: [...new Set(running.flatMap((t) => (t.NodeID ? [t.NodeID] : [])))],
  state: rolledBack ? "failed"
       : failed.length >= 5 ? "crashloop"
       : live.some((t) => t.Status.State === "pending") ? "pending"
       : update === "updating" || running.length < live.length ? "updating"
       : oneShot ? "completed"
       : "ok",
  error: failed[failed.length - 1]?.Status.Err ?? (rolledBack ? service?.UpdateStatus?.Message : undefined),
  at: Date.now(),
};
```

> **Go now:** the Swarm adapter parses `keel.revision` once with `strconv.Atoi` (a missing or non-numeric label reads as 0, not `NaN`) and hands `summarizeTasks` typed tasks (revision, Keel node id, `Status.Timestamp` as epoch ms); `nodeIds` is gone.

Semantics to keep:

- `revision` is the max of spec label and task labels: after a rollback the spec label reverts but the failed revision's tasks are still newest, so `revision` names what failed and `state:"failed"` says it is not running.
- `UpdateStatus.State` (`updating`, `paused`, `completed`, `rollback_started`, `rollback_paused`, `rollback_completed`) is authoritative for updates: `updating` keeps `state:"updating"` even when the new task already runs (Swarm is watching it for Monitor=5 s). Any `rollback_*` or `paused` → `failed`. A fresh create has no `UpdateStatus`.
- `error` takes the **last failed task in API order** (Docker's order; effectively arbitrary). Exact port: keep API order. If that task has no `Err`, falls back to the rollback message.
- `crashloop` counts failed+rejected tasks of the current revision across all slots; visibility is bounded by Swarm's task history (default 5 per slot) and MaxAttempts 5.
- Service 404 and no tasks → `{revision:0, running:0, nodeIds:[], state:"ok", at}`.
- At 0 replicas the service spec label still carries the revision while Swarm drops task history, so a stopped service observes `revision = desired.revision, running 0`.

### 8.5 `observe` — full sweep (internal action, `{}`)

```
nodes = listDeployable()              // nodes with desired && desired.revision > 0 → [{id, environmentId}]
if nodes non-empty:
   [services, tasks] = parallel(GET /services?filters={"label":["keel.service"]},
                                GET /tasks?filters={"label":["keel.service"]})
   byName = map services by Spec.Name
   for node in nodes:
      own = tasks where label keel.service == node.id
      setObserved(node.id, summarize(own, byName["svc-"+node.id] ?? null))
   reconcile.run()                    // once, after all
observeServers()
```

No settle re-checks, does not touch `observeScheduled`, does **not** remove orphan `svc-*` services (no GC anywhere). Triggered by `resync` (worker (re)connect or after a failed POST) and manually (`bunx convex run swarm:observe`). Go: also run it once at control-plane start.

### 8.6 `observeSwarmNodes` / `observeServers`

`GET /nodes` → `ready = count(Status.State == "ready")` → `environments.setServers({servers: ready})`: upsert the single `cluster` row with `{servers, at: now}` (patch first row if any, else insert).

### 8.7 `setObserved` (internal mutation)

Args `{ id, observed }` (validator = `observed` shape §2.1).

```
node = get(id); if !node: return               // deleted between list and write
next = {...node, observed}
patch(id, {
  observed,
  deployedRevision: converged(next) && observed.revision > 0 ? observed.revision : node.deployedRevision,
  oneShot: observed.state == "completed" ? true : observed.running > 0 ? false : node.oneShot,
})
```

---

## 9. Status derivation (pure functions over `desired` / `observed` / `applyError`)

```ts
function converged({ desired, observed }) {
  if (!desired || !observed) return false;
  if (desired.replicas === 0) return observed.running === 0;
  if (observed.revision !== desired.revision) return false;
  if (observed.state === "completed") return (observed.completed ?? 0) >= desired.replicas;
  return observed.state === "ok" && observed.running >= desired.replicas;
}

// "healthy" | "done" | "deploying" | "stopping" | "error" | "stopped" | "pending"
function deriveStatus(node) {
  const { desired, observed } = node;
  if (!desired || desired.revision === 0) return "pending";   // never shipped
  if (node.applyError) return "error";
  if (!observed) return "deploying";
  if (desired.replicas === 0) {
    if (observed.running > 0) return "stopping";
    if (observed.revision === 0 || observed.revision >= desired.revision) return "stopped";
    return "stopping";
  }
  if (observed.revision < desired.revision) return "deploying";
  if (observed.state === "crashloop" || observed.state === "failed") return "error";
  if (converged(node)) return observed.state === "completed" ? "done" : "healthy";
  return "deploying";
}
```

`DEPLOYABLE = {service, database, cache}`. In the node view (`nodes.list`): `deploy.step` (only when status `deploying` and `shippedAt` set) = `"pulling image"` if `!observed || observed.revision < desired.revision`, else `"rolling out"` if `observed.state == "updating"`, else `"starting"`; `error = applyError ?? (status == "error" ? observed.error : undefined)`; `running = observed.running ?? 0`; `finishedAt = status == "done" ? observed.finishedAt : undefined`; `stoppedAt = shippedAt` when stopped/stopping.

---

## 10. Deployments: start, settle, timeout

### 10.1 `beginDeployment(environmentId, {only?, refresh=false, verb?})` (helper inside mutations)

```
if exists deployment in env with status "running":  throw ConvexError("A deployment is already running")
nodes = env nodes (by_environment index order)
affected = nodes where desired && type ∈ DEPLOYABLE && (only ? id ∈ only : dirty)
if affected empty: throw ConvexError("Nothing to ship")
for n in affected: patch(n, {desired: {...desired, revision: revision+1}, dirty: false, shippedAt: now, applyError: undefined})
steps = affected.map(n => {nodeId: n._id, label: n.name, status: "pending"}) + [{label: "health checks", status: "pending"}]
word = verb ?? (only ? (refresh ? "redeploy" : "deploy") : "ship")
message = word + " " + affected.names.join(", ")
id = insert deployments {environmentId, message, status: "running", startedAt: now, steps, log: []}
for n in affected: runAfter(0, swarm.apply, {id: n._id, deploymentId: id, pull: refresh})
runAfter(300000, reconcile.timeoutDeployment, {deploymentId: id})
return id
```

Exact `ConvexError` strings (CLI maps them, `apps/cli/internal/keel/api.go translate`): `"A deployment is already running"` → `DEPLOYMENT_RUNNING`; `"Nothing to ship"` → `NOTHING_TO_SHIP`; callers' access errors `"Environment not found"` (signed out or foreign env also yields this) and `"Node not found"`. The running-check + insert must be atomic per environment in Go (transaction with a lock on the environment, or a unique partial index on `(environmentId) WHERE status='running'`).

Public entry: `deployments.start({environmentId, only?: id[], refresh?: boolean}) → deploymentId` after `requireEnvironment`. Verbs from `nodes.stop` (`"stop"`, after setting `replicas: 0`; no-op returning undefined if already 0) and `nodes.start` (`"deploy"` if revision 0 else `"start"`, replicas `0→1`). `nodes.create({deploy:true})` swallows `ConvexError`s from it (node stays dirty).

### 10.2 `reconcile.run` (internal mutation, `{}`) — after every observe and every node delete

```
for d in deployments where status == "running" (by_status index):
  log = copy(d.log); steps = []
  for step in d.steps:
     if !step.nodeId || step.status in {done, failed}: steps.push(step); continue
     node = get(step.nodeId)
     if step.status == "pending" && node: steps.push(step); continue   // waits for apply
     [next, text] = settle(step, node, now); steps.push(next)
     if text: log.push({at: now, nodeId: step.nodeId, text})
  nodeSteps = steps with nodeId; health = first step without nodeId
  anyFailed = some nodeStep failed; allDone = every nodeStep done
  allApplied = every nodeStep (status != pending && appliedAt)
  if health:
     if anyFailed: health.status = failed, health.finishedAt = now
     elif allDone: health.status = done, finishedAt = now, startedAt = startedAt ?? now
                   log.push({at: now, text: d.message.startsWith("stop ") ? "stopped" : "all replicas healthy"})
     elif allApplied && health.status == "pending": health.status = running, startedAt = now
  status = anyFailed ? "failed" : allDone ? "success" : "running"
  patch(d, {steps, log: log.slice(-500), status, finishedAt: status == "running" ? undefined : now})
```

`settle(step, node, now)`:

```
name = step.label
if !node: return [{...step, status: failed, finishedAt: now}, "<name>: node deleted"]
if !step.appliedAt || !node.observed || !node.desired: return [step]
if node.observed.revision == node.desired.revision:
   if state in {crashloop, failed}:
      why = error ? " · <error>" : ""
      what = state == "failed" ? "rolled back by Swarm" : "crash loop"
      return [{...step, status: failed, finishedAt: now}, "<name>: <what><why>"]
   if converged(node):
      text = state == "completed" ? "<name>: ran to completion"
           : desired.replicas == 0 ? "<name>: stopped"
           : "<name>: <observed.running>/<desired.replicas> replicas running"
      return [{...step, status: done, finishedAt: now}, text]
return [step]
```

Notes: global (all running deployments in all environments); writes every running deployment each time (Go may skip unchanged writes). Settling is keyed on `observed.revision == desired.revision`; a scan that still sees the old revision leaves the step running. A node whose desired moved on (a later ship) is compared against its *current* desired.

### 10.3 `reconcile.timeoutDeployment` (internal mutation, `{deploymentId}`)

```
d = get(id); if !d || d.status != "running": return
for step in d.steps:
  if step.status in {done, failed}: keep
  else: step → {status: failed, finishedAt: now}
        if step.nodeId:
           node = get(nodeId); why = node?.observed?.error ? " · <error>" : ""
           log.push({at: now, nodeId, text: "<label>: timed out waiting for replicas<why>"})
           if node: patch(node, {applyError: "timed out waiting for replicas<why>"})
patch(d, {steps, log: slice(-500), status: failed, finishedAt: now})
```

Covers: task `pending` (no server can schedule it), a pull that hangs, a container that never emits an event. It is the only timer in the deploy path. **Durable**: must survive a control-plane restart (§18).

### 10.4 Timeline reference (dev box)

Ship of cached `nginx:alpine`: "service created" → `success` in ~3.2 s (`container start` event at +2.2 s, scan at +2.7 s). Redeploy of `postgres:16`: ~9 s, gated by `UpdateStatus` `completed` at +8.6 s (5 s Monitor after the new task starts).

---

## 11. Control-plane HTTP routes for the worker

All under the Convex **site** origin (`http://<KEEL_ADDR>:3211` on installs). Go: same paths on the control plane's HTTP listener (keep them stable: deployed agents and keel-proxy call them).

### 11.1 Auth (shared by `/worker/events`, `/worker/config`, `/proxy/events`)

```ts
function authorized(req) {
  const expected = process.env.KEEL_WORKER_TOKEN;
  const header = req.headers.get("authorization") ?? "";
  if (!expected || !header.startsWith("Bearer ")) return false;   // case-sensitive "Bearer "
  return timingSafeEqual(header.slice("Bearer ".length).trim(), expected); // constant time, length-mixed
}
```

Fail → `401`, body `unauthorized` (text/plain). Empty/unset `KEEL_WORKER_TOKEN` rejects everything. Go: `subtle.ConstantTimeCompare`.

### 11.2 `POST /worker/events`

| Step | Behaviour |
| --- | --- |
| 1 | Auth → 401 `unauthorized`. |
| 2 | Read whole body as text; `length > 262144` → `413` `too large`. |
| 3 | Parse: if `text.trim()` starts with `[` → `JSON.parse(text)`; else split on `\n`, drop lines that are blank after trim, `JSON.parse` each. Non-array result → wrapped in an array. Every element must be a non-null object with keys `Type` and `Action` present. Any failure → `400` `bad json`. Empty body or `[]` → zero events (valid). |
| 4 | Trim each: `{type: String(Type), action: String(Action), name: Actor.Attributes.name if string, serviceName: Actor.Attributes["com.docker.swarm.service.name"] if string, time: e.time if number}`. |
| 5 | `resync = header "x-keel-resync" === "1"`. |
| 6 | `events.ingest({events, resync})`; respond `200` `ok`. |

> **Go now:** the body must be a JSON array (no single object, no NDJSON), at most 256 KiB in bytes (`http.MaxBytesReader`, else `413 too large`); each element decodes into `Type` and `Actor.Attributes`, and one without `Type` is `400 bad json`. The token check compares SHA-256 digests in constant time.

### 11.3 `GET /worker/config`

Auth → 401. Else `200`, headers `content-type: application/json`, `cache-control: no-store`, body = `worker.config` (§12).

### 11.4 `POST /proxy/events` (owned by the proxy spec)

Same bearer token. Body `{event: "cert_obtained"|"cert_failed", name: string, error?: string}`; 413 `too large`, 400 `bad json` / `bad report`; → `proxyInternal.certReport`. Listed here only because it shares `KEEL_WORKER_TOKEN`.

---

## 12. `worker.config` (internal query) — what every node's worker receives

```
if no logSinks row exists at all: return {sinks: []}
projects = all projects (default order: _creationTime asc)
sinkOfOrg = {}
for p in projects with organizationId (first time per org): sinkOfOrg[org] = sinkOf(org)
environments = all; nodes = all
projectOfEnv = env._id → env.projectId
return { sinks: projects.flatMap(p =>
   row = p.organizationId && sinkOfOrg[p.organizationId]; if !row: []
   serviceIds = nodes.filter(n => n.desired && projectOfEnv[n.environmentId] == p._id).map(n => n._id)
   [{ projectId: p._id, serviceIds, sink: row.sink, since: row._creationTime }]) }
```

- `sinkOf(org)`: the `logSinks` row with `organizationId == org` (unique), else the newest legacy row (no `organizationId`, `projectId` of a project in that org), else null.
- `serviceIds` includes never-shipped nodes (`desired` present, any revision).
- `sink` is the **full** stored object: `{kind:"axiom", domain, dataset, token, traces?, org?}` (secret token; only ever sent here, behind the bearer).
- `since` = the sink row's `_creationTime` (float epoch ms, e.g. `1727600000000.123`).

Example:

```json
{"sinks":[{"projectId":"k17…","serviceIds":["j57…","j58…"],"sink":{"kind":"axiom","domain":"api.axiom.co","dataset":"keel","traces":"keel-traces","token":"xaat-…","org":"acme"},"since":1727600000000.123}]}
```

Shape is a worker contract: "keep this small and stable: every node holds a copy, and a change here is a worker release". Only add fields.

> **Go now:** one entry per organization sink, `{serviceIds, sink, since}` with no `projectId` and no legacy `sinkOf` fallback; `since` is `log_sinks.created_at` (integer ms). The agent ships in the same image as `keel serve`, so the shape moves with it.

---

## 13. The per-node worker (agent mode of the Go binary)

Replaces `apps/worker` (Bun, no deps). Runs on every Swarm node as the global service `keel-worker`. **Invariants:** outbound-only (listens on nothing), every Docker call is a `GET`, one bearer token for every control-plane route, restarts resume from a state file. (The socket is bind-mounted `:ro`, but a read-only bind mount does not stop API writes on a unix socket; "read-only" is enforced by the code, keep it so.)

### 13.1 Environment

| Var | Default | Meaning |
| --- | --- | --- |
| `KEEL_URL` | **required** | Control plane site URL, trailing `/` stripped. Install: `http://<KEEL_ADDR>:3211`. Missing → startup throws `KEEL_URL is required (Convex site URL, e.g. https://x.convex.site)`. |
| `KEEL_WORKER_TOKEN` | — | Bearer token. If unset, read `/run/secrets/keel_worker_token` (trimmed). Neither → throws `no KEEL_WORKER_TOKEN and no /run/secrets/keel_worker_token`. |
| `KEEL_STATE` | `/var/lib/keel-worker/state.json` | Resume state file (named volume `keel-worker-state`). |
| `KEEL_CONFIG_POLL_MS` | `30000` | Config poll interval. |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket. |

> **Go now:** the service is `keel-agent`; `KEEL_STATE` defaults to `/var/lib/keel-agent/state.json` (volume `keel-agent-state`); there is no `DOCKER_SOCKET` (moby's `client.FromEnv`, so `DOCKER_HOST`); `KEEL_TS_AUTHKEY` optionally reaches `KEEL_URL` through an embedded tsnet node; the missing-`KEEL_URL` error names the control plane's URL.

### 13.2 Startup and main loop

1. `loadState()`: parse JSON `{eventsSince?: string, logsSince?: {[containerId]: string}}`; any error → `{logsSince: {}}`.
2. State writer: every 1 s, if dirty, write `JSON.stringify(state)` to `KEEL_STATE` (errors logged).
3. `GET /info` → node id = `Swarm.NodeID ?? Name ?? ""` (goes into every log event's `node`).
4. Run concurrently forever: **config poll loop** and **event forwarder**.
5. `SIGTERM`/`SIGINT`: flush all log queues, wait for drains raced against 5 s, `exit 0`. (Swarm stop-grace-period is 10 s.)

Config poll loop:

```
loop:
  try: cfg = GET <KEEL_URL>/worker/config (Bearer, 10 s timeout; non-2xx → error "config <status>")
       applyConfig(cfg.sinks); reconcileFollowers()
  catch: log "poll failed: <err>"
  wait KEEL_CONFIG_POLL_MS or until refreshConfig() wakes it
```

(`refreshConfig()` while a poll is in flight is a no-op — acceptable.)

### 13.3 Docker calls made by the worker (all GET, unversioned paths, over the socket)

| Path | Params | Use |
| --- | --- | --- |
| `/info` | — | Swarm NodeID. |
| `/containers/json` | `filters={"label":["com.docker.swarm.service.name"],"status":["running","exited"]}` (a `status` filter implies `all`; Go: also pass `all=1`) | Swarm task containers for log followers. Fields used: `Id`, `Labels`, `State`. |
| `/events` | `filters={"type":["container","service","node"]}`, `since=<eventsSince>` when known | Live NDJSON stream. |
| `/containers/{id}/logs` | `follow=1&stdout=1&stderr=1&timestamps=1` + `since=<s.nnnnnnnnn>` or `tail=0` | Per-container tail. |

Non-OK → error `docker <path>: <status> <body>`.

### 13.4 Event forwarder

```
loop forever:
  resync = true
  try:
    stream = GET /events (filters type container|service|node; since=state.eventsSince if set)
    if postEvents("[]", resync=true) accepted: resync = false      // asks CP for a full sweep
    for each NDJSON line (skip blank; skip unparseable):
       e = parse(line)
       if e.Type == "container" && e.id: onContainerEvent(e.Action, e.id, e.Actor.Attributes ?? {})
       if relevant(e): resync = !postEvents(line /* raw line */, resync)
       if e.timeNano is number:
          ns = timeNano + 1                                  // since is inclusive
          state.eventsSince = "<ns / 1e9>.<pad9(ns % 1e9)>"; mark dirty
    log "docker events stream ended, reconnecting in 2s"
  catch: log "stream error: …, reconnecting in 2s"
  sleep 2 s
```

`relevant(e)`: false if `Action` starts with `exec_` or `health_status`; `container` → `Attributes["com.docker.swarm.service.name"]` starts with `svc-`; `service` → `Attributes.name` starts with `svc-`; `node` → true; anything else false.

Each relevant event is its own POST (body = the raw Docker JSON line), sent **in order, synchronously** (the stream is not read while a POST retries).

`postEvents(body, resync) → accepted:boolean`:

```
for n = 0; ; n = min(n+1, 6):
  try POST <KEEL_URL>/worker/events
        headers: authorization: Bearer <token>, content-type: application/json, x-keel-resync: "1"|"0"
        timeout 10 s
      2xx → return true
      4xx → log "rejected <status>, skipping" {body: first 120 chars}; return false
      other → log "post failed <status>, retry in <n*5+5>s"
  catch → log "post failed (<err>), retry in <n*5+5>s"
  resync = true                       // retries always ask for a sweep
  sleep n*5000 + 5000 ms
```

Docker event fields the control plane needs: `Type`, `Action`, `Actor.Attributes.name`, `Actor.Attributes["com.docker.swarm.service.name"]`, `time`. Go agent recommendation: read the container id from `Actor.ID` (fall back to the legacy top-level `id`, which the TS reads and Docker has deprecated).

> **Go now:** the agent reads moby's typed `events.Message` (container id from `Actor.ID`) and posts each relevant one as a one-element JSON array, re-encoded, not the raw line; `X-Keel-Resync: 1` is sent only when set; a 4xx is logged with its status and event count. The control plane reads only `Type`, `name` and the service-name label.

### 13.5 Log shipping

Routing state (rebuilt on every config poll):

- `routes`: projectId → `{sink, serviceIds}`; `sinkByService`: serviceId → sink; `sinceByService`: serviceId → docker-since string of `cfg.since` (only when `since` is a number).
- `sinkKey(cfg) = "<kind>:<domain>:<dataset>:<last 6 chars of token>"`. Sinks are reused across polls when the key matches; equal sinks across projects share **one queue**.
- `readSince` (in-memory): containerId → since after the last line read in this process.
- `followers`: containerId → cancel func. `finished`: containers read to EOF.
- `queues`: sinkKey → `{sink, entries[], draining, room waiters[]}`. One global `flushTimer`, one global `retryTimer`.

`applyConfig(sinks)`: build new maps; drop queues whose key no sink uses any more (log `dropping <n> queued lines for a removed sink`, wake their waiters); log `config applied {sinks, services}` when the routing changed.

> **Go now:** no project routes and no `sinkKey`: queues are keyed by the decoded `SinkConfig{Kind, Domain, Dataset, Token}` and each service id maps to its queue; there is no `config applied` line. The replica slot is `strconv.Atoi`, a line's stamp is parsed once with `time.Parse(time.RFC3339Nano)` into `Line.Time`, and every Docker `since` is `dockerTime` (`<unix>.<9-digit ns>`, the line's time + 1 ns), not `sinceAfter`'s regex and carry.

`dockerSince(ms) = "<floor(ms/1000)>.<pad9(min(round((ms % 1000) * 1e6), 999999999))>"`.

`reconcileFollowers()`:

```
containers = GET /containers/json (svc label filter, running|exited)
for c in containers:
   serviceId = svcIdOf(c.Labels)   // label com.docker.swarm.service.name minus "svc-", else null
   routed = serviceId && sinkByService has it
   pending = c.Id in state.logsSince && c.Id not in finished
   if routed && (c.State == "running" || pending): start(c) else stop(c.Id)    // keep resume point
for id in followers not in containers: stop(id, forget=true)
for id in state.logsSince keys not in containers: stop(id, forget=true)
drop finished ids not in containers
```

`start(c)`: no-op if already following or unrouted; else spawn `follow(c)`; on exit remove from `followers` if still the same handle.

`follow(c)`:

```
serviceId, service = labels["com.docker.swarm.service.name"], task = labels["com.docker.swarm.task.id"] ?? ""
replica = Number(labels["com.docker.swarm.task.name"]?.split(".")[1] ?? 0) || 0     // svc-<id>.<slot>.<taskid>
container = c.Id[:12]
while not cancelled:
  if !sinkByService[serviceId]: return
  try:
    since = readSince[c.Id] ?? state.logsSince[c.Id] ?? sinceByService[serviceId]   // else tail=0 (new lines only)
    stream = GET /containers/<Id>/logs (follow, stdout, stderr, timestamps, since|tail=0)
    for frame in FrameParser(stream): for line in LineSplitter(frame):
        sink = sinkByService[serviceId]; if !sink: return
        q = queueFor(sink); wait until len(q.entries) < 20000 (or cancelled); if cancelled: return
        after = line.time ? sinceAfter(line.time) : undefined
        if after: readSince[c.Id] = after
        enqueue(q, {event, container: c.Id, since: after})
    finished.add(c.Id); return                     // EOF: container exited
  catch: if cancelled return; log "follow <container> failed (<err>), retry in 3s"; sleep 3 s
```

Event (Axiom row) — **field names are a contract** with the read side (`docs/logs.md`):

```json
{"_time":"<RFC3339Nano from Docker, or now ISO if the line had none>","message":"<line without stamp>","stream":"stdout|stderr","service_id":"<nodeId>","service":"svc-<nodeId>","task":"<swarm task id>","replica":<slot>,"node":"<swarm node id>","container":"<12-char id>"}
```

Queue mechanics:

- `enqueue`: push; if `len ≥ 500` flush now; else start the 1 s flush timer if none.
- `flush`: clear timer; for every queue with entries and no drain in progress, start `drain(q)`.
- `drain(q)`: while entries and the queue is still registered: take the first 500; `ok = sink.send(events)`; if !ok → put the batch back at the **front**, arm the single 5 s retry timer (→ flush), return; else **checkpoint** (for each entry with `since`, `state.logsSince[container] = since`, later wins, mark dirty) and if `len < 10000` wake all room waiters.
- `stop(id, forget=false)`: cancel follower; if `forget` also delete `finished`, `readSince`, `state.logsSince[id]`.
- `onContainerEvent(action, id, attrs)`: `start` → if routed, `start({Id:id, Labels:attrs, State:"running"})`; else if ≥ 5 s since the last forced refresh, trigger an immediate config poll. `destroy` → `stop(id, forget=true)`. Other actions ignored (EOF ends the follower on exit).

Delivery guarantees: a container's resume point advances only after the sink accepted the batch → restarts re-read undelivered lines (duplicates possible, no gaps). Back-pressure at 20 000 queued lines per sink; Docker's json-file is the buffer. Only a sink 4xx (not 429) drops lines; a removed sink drops its queue.

Frame parsing (`FrameParser`, Docker multiplexed stream): 8-byte header `[type, 0, 0, 0, len u32 BE]` + payload; type 1 stdout, 2 stderr (0 treated as stdout). Carry partial frames across chunks. If a header's type byte is `> 2` → TTY container: the rest of the buffer is raw stdout text, carry cleared. `LineSplitter`: per-stream partial buffers; split on `\n`, keep the trailing partial, drop empty parts; strip one trailing `\r`; if the text before the first space has length ≥ 20, ends with `Z` and has `-` at index 4 it is the timestamp (`time`), the rest is `text`; else `time=""`.

`sinceAfter(stamp)` (exclusive resume point): regex `^(.+?)(?:\.(\d{1,9}))?Z$`; `secs = floor(Date.parse(m1+"Z")/1000)`; `nanos = int(frac right-padded to 9 digits) + 1` with carry into secs; return `"<secs>.<pad9(nanos)>"`; undefined if no match / NaN.

### 13.6 Axiom sink (write side)

```
base = domain contains "://" ? domain : "https://" + domain, trailing "/" stripped
url  = domain endsWith ".edge.axiom.co" ? base + "/v1/ingest/" + urlenc(dataset)
                                         : base + "/v1/datasets/" + urlenc(dataset) + "/ingest"
send(events):  body = NDJSON (one JSON object per line, "\n"-joined, no trailing newline)
  for attempt 0..4:
     POST url, authorization: Bearer <token>, content-type: application/x-ndjson, timeout 15 s
       2xx → true
       4xx except 429 → log "rejected <status>, dropping <n> events"; true
       else log "ingest <status>, retry <attempt+1>"   (network error: "ingest failed (…), retry <n>")
     sleep 1000 * 2^attempt ms          // also after the 5th attempt (1+2+4+8+16 = 31 s)
  log "unreachable, keeping <n> events for a later attempt"; false
```

> **Go now:** the base URL is `domain.AxiomBaseURL`, the dataset is `url.PathEscape`d, the body comes from `json.Encoder` (every line ends in `\n`), and the log lines are slog (A1).

### 13.7 State file

`{"eventsSince":"1727600000.123456790","logsSince":{"<full container id>":"1727600000.123456790"}}`. Losing it costs one full sweep and, for logs, re-reading every container from its sink's connect time (duplicates, never a gap). The Go agent should read and write the **same file and format** on the same volume so an upgrade from the Bun worker does not replay or skip.

> **Go now:** same JSON shape, but on `keel-agent-state` at `/var/lib/keel-agent/state.json`; nothing reads the Bun worker's `keel-worker-state` (no install to upgrade).

---

## 14. Worker deployment, cluster bootstrap, token

### 14.1 `scripts/bootstrap-swarm.sh [--swarm-only]` (idempotent, on the manager)

1. `TAILSCALE_IP` = env or `tailscale ip -4`.
2. If `docker info --format '{{.Swarm.LocalNodeState}}'` ≠ `active`: `docker swarm init --advertise-addr $IP --listen-addr $IP:2377 --data-path-addr $IP --default-addr-pool 10.200.0.0/16 --default-addr-pool-mask-length 24`.
3. If `docker network inspect keel` fails: `docker network create -d overlay --attachable --opt com.docker.network.driver.mtu=1200 keel`.
4. `--swarm-only` → stop (install.sh runs this before compose up, since keel-proxy joins the overlay). Else run `deploy-worker.sh`.

Inter-server ports (tailnet only): 2377/tcp, 7946/tcp+udp, 4789/udp.

### 14.2 `scripts/deploy-worker.sh` (idempotent)

Inputs: env or `$ROOT/infra/worker/.env.local` — `KEEL_URL` (default `http://<tailnet-ip>:3211`), `KEEL_WORKER_TOKEN` (generated `openssl rand -hex 32` if absent, written 0600 to `.env.local`, pushed with `convex env set KEEL_WORKER_TOKEN` via stdin), `KEEL_REGISTRY` (push target for locally built images; empty = single node), `KEEL_WORKER_IMAGE` (use this image instead of building; install.sh passes `ghcr.io/thallesp/keel-worker:<version>`).

- Image: given, or built from `apps/worker` tagged `[$KEEL_REGISTRY/]keel-worker:<sha256(Dockerfile package.json tsconfig.json src/**)[:12]>` (pushed if registry set).
- Secret: `keel-worker-token-<sha256(token)[:12]>` created if missing (value via stdin).
- Digest pin: `WANT = IMAGE@<RepoDigest>` when Docker knows one; flags `--no-resolve-image` (+ `--with-registry-auth` when a digest exists).
- Create when missing:

```
docker service create --detach --quiet --name keel-worker --mode global --network host \
  --no-resolve-image [--with-registry-auth] \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly \
  --mount type=volume,src=keel-worker-state,dst=/var/lib/keel-worker \
  --secret source=<SECRET>,target=keel_worker_token \
  --env KEEL_URL=<KEEL_URL> \
  --restart-condition any --restart-delay 2s --stop-grace-period 10s \
  <WANT>
```

- Else update only if image, secret name or `KEEL_URL` differ: `docker service update --detach --quiet <resolve flags> --image <WANT> --secret-rm <CUR> --secret-add source=<SECRET>,target=keel_worker_token --env-add KEEL_URL=<URL> keel-worker`, then best-effort `docker secret rm <old>`.
- Removes the legacy `keel-events` service and its `keel-events-*` configs / `keel-events-token-*` secrets.

Go port: the agent is the same binary (`keel agent` or similar) in the `keel` image; keep the service name `keel-worker`, the `keel-worker-state` volume, the secret target name `keel_worker_token`, global mode, host network. The control plane could create/update this service itself through the Docker API instead of a shell script; parameters above are the spec.

> **Go now:** `keel serve` creates or updates `keel-agent` itself when `KEEL_AGENT_IMAGE` is set (`EnsureAgent`, `internal/adapters/swarm/agent.go`): command `keel agent`, volume `keel-agent-state` at `/var/lib/keel-agent`, secret `keel-agent-token-<sha256(token)[:12]>` with target `keel_worker_token`, global mode, host network, every capability dropped; no deploy script and no `keel-worker` or `keel-events` cleanup.

### 14.3 `install.sh` order and the token

1. `KEEL_WORKER_TOKEN` = saved value or `rand_hex` (32 random bytes → 64 hex chars), persisted in `/opt/keel/.env` (0600).
2. `start_swarm`: `bootstrap-swarm.sh --swarm-only` with `TAILSCALE_IP=$KEEL_ADDR`.
3. `start_control_plane`: compose up; `keel-functions deploy` sets Convex env `SITE_URL`, `BETTER_AUTH_SECRET`, `KEEL_WORKER_TOKEN` (+ `KEEL_PUBLIC_IP`, `KEEL_ACME_EMAIL` or removes them), pushes functions, runs `migrations:run`.
4. `start_workers`: `bootstrap-swarm.sh` with `KEEL_URL=http://$KEEL_ADDR:3211`, `KEEL_WORKER_IMAGE=$PREFIX/keel-worker:$VERSION`.
5. `check_health`: `GET http://$KEEL_ADDR:3211/worker/config` with the bearer must succeed ("Convex does not accept the worker token" otherwise); `keel-worker` replicas `N/N`, N ≠ 0 within 120 s.

The same token authenticates keel-proxy's reports (`/proxy/events`, written into the proxy config by `proxyInternal`). Rotation: delete it from the env file and re-run (new secret name → service update swaps it). Go: control plane reads `KEEL_WORKER_TOKEN` from its environment; agents from env or `/run/secrets/keel_worker_token`.

---

## 15. Server (machine) join flow

**Today:** no Keel code. An operator runs `docker swarm join --token <worker token> <manager-tailnet-ip>:2377` (advertising the new box's tailnet IP) on a box that has Docker and Tailscale. Swarm then: schedules a `keel-worker` task there (global service); the worker's `node` events reach `/worker/events` → `observeSwarmNodes` → `cluster.servers` updates; unpinned user tasks may land there. Multi-server caveat: the worker image must be pullable by the new node (`KEEL_REGISTRY` or the published ghcr image), and user images must be pullable from a registry.

**Design target (docs/workers.md, not built — treat as a new feature in Go):**

1. "Add server" on the canvas inserts a server row `{name, status:"pending", joinSecret (single use)}`.
2. `GET /join/<secret>` serves a per-server shell script: install Docker if missing; install/bring up Tailscale (auth key or interactive); pre-flight dump (running containers, bound ports, existing network subnets) printed and confirmed; `docker swarm join --token <worker-token> <manager-tailnet-ip>:2377` advertising the node's tailnet IP; POST back the Swarm `NodeID` and the pre-flight dump.
3. The callback flips the row to `status:"ready"`, stores `swarmNodeId`, `unmanagedContainers`; canvas warns when unmanaged containers exist.
4. Designed schema: `{name, status: "pending"|"ready"|"down" (+ "removed" per volumes.md), swarmNodeId?, tailscaleIp?, joinSecret?, unmanagedContainers?: string[], lastSeen?}` index `by_swarm_id`.

Joining is additive: existing containers/compose/volumes/bridge networks keep running, invisible to the scheduler; conflicts are bound host ports and subnet overlap (pre-flight). Never run `docker swarm leave --force` on the manager.

---

## 16. `migrations.run` and crons

### 16.1 `migrations.run` (internal mutation, `{}`) — after every deploy of functions (every install/upgrade)

Returns `{ quickTunnelsConverted: number, redisPasswords: number, domainsMoved: number }`. Each part idempotent:

1. **Quick Tunnel → endpoint.** `ip = KEEL_PUBLIC_IP || undefined`. For each node with `public !== undefined || ingress !== undefined`: if `public` truthy && `type == "service"` && `desired.port` && `ip` && no endpoints → endpoint `{protocol:"http", port: desired.port, domain: defaultDomain(node, ip), status:{state:"starting", at}}` (count it). Patch `public: undefined, ingress: undefined` (+ `endpoints: [http]` when made).
2. **Redis password.** For each `type == "cache"` node with `engineOf(desired?.image) == "redis"` and no `REDIS_PASSWORD` variable: insert variable `{nodeId, key:"REDIS_PASSWORD", value: randomSecret(), secret: true}`, patch `dirty: true`, `markReferrersDirty(node)` (transitively dirties nodes whose variables reference it). `randomSecret(20)` over alphabet `abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789` (TS uses `Math.random`; Go: `crypto/rand`).
3. **Public IP moved** (only if `ip`): for each node's http endpoints, `movedDefaultDomain(node, domain, ip)` → new domain + `status:{state:"starting", at}`. `defaultDomain = "<name>-<shortHash(_id)>.<ip with . → ->.sslip.io"`; `movedDefaultDomain` matches `^(.+-([0-9a-z]{6}))\.(\d+-\d+-\d+-\d+)\.sslip\.io$` with group 2 == `shortHash(_id)` and group 3 ≠ current dashed IP → `"<group1>.<dashed ip>.sslip.io"`. `shortHash` = FNV-1a 32-bit over UTF-16 code units (`h=0x811c9dc5; h = imul(h ^ c, 0x01000193)`), unsigned, base 36, left-pad `0` to 6, last 6 chars.
4. Schedule `swarm.removeLegacyTunnels` and `proxy.sync` at +0.

Go: run the same steps at control-plane boot (and/or in the Convex→Go importer for 1).

> **Go now:** no `migrations.run` and no backfills: no Quick Tunnel conversion, no Redis password pass (a new cache gets `REDIS_PASSWORD` when it is created), no legacy tunnel removal. Only step 3 survives, as `moveDefaultDomains` in the recovery pass (`recoverIngress`), with `shortHash` from `hash/fnv` over the id's bytes (ids are ASCII).

### 16.2 Crons (`crons.ts`)

Exactly one: `"keel-proxy resync"`, interval **2 minutes**, → `proxyInternal.resync`: if any node has a non-empty `endpoints`, schedule `proxy.sync` at +0. Nothing in the Swarm path is on a timer (observation is event-driven; the deployment timeout is a one-shot per deployment).

---

## 17. Realtime: subscriptions and invalidation

Reactive queries the web subscribes to that this area's writes change:

| Query (args) | Subscribed in | Reads |
| --- | --- | --- |
| `nodes.list({environmentId})` | `use-synced-graph.ts`, `topbar.tsx`, `observability/chrome.tsx` | nodes view: status (desired/observed/applyError), running, deployedRevision, error, deploy step, endpoints |
| `environments.summary({environmentId})` → `{pendingChanges, counts, servers}` | `use-data.ts` (status bar, Ship button) | nodes (dirty, status) + `cluster.servers` |
| `deployments.latest({environmentId})` | `use-data.ts` | newest deployment of env |
| `deployments.get({id})` | `use-deployment-link.ts` | one deployment |
| `deployments.listForNode({nodeId})` | bottom panel Deployments tab | last 50 deployments of the node's env filtered to ones with a step for the node, max 20 |

The CLI polls the same queries over HTTP (`deployments:get` for `--wait`), not reactive.

Proposed coarse keys (WebSocket pushes `{key}`; client refetches queries bound to it):

| Write | Key(s) to publish |
| --- | --- |
| `setObserved` (nodes.observed / deployedRevision / oneShot) | `env:<environmentId>` |
| `setApplyError`, `followPort`, timeout's node `applyError`, `beginDeployment` node patches | `env:<environmentId>` |
| deployment insert, step writers, `reconcile.run`, `timeoutDeployment` | `env:<environmentId>` and `deployment:<id>` |
| `environments.setServers` (cluster row) | `cluster` (global; every `environments.summary` subscriber) |
| `observeScheduled` set/clear | none (not visible; or drop the field in Go) |
| `migrations.run` | `env:<environmentId>` per touched node |
| `worker.config` | not pushed; agents poll every 30 s |

Bind `nodes.list`, `environments.summary`, `deployments.latest`, `deployments.listForNode` to `env:<id>` (listForNode via the node's environment), `environments.summary` additionally to `cluster`, `deployments.get` to `deployment:<id>`. Every scan rewrites `observed.at`, so publish only when a view-visible field changed, or debounce publishes per key (~100–250 ms) to absorb rollout bursts.

---

## 18. Replacing Convex runtime semantics in Go

| Convex behaviour relied on | Go equivalent |
| --- | --- |
| Mutations are serializable transactions (OCC retry) | One DB transaction per mutation-equivalent; lock the deployment row (`SELECT … FOR UPDATE`) in step writers / `reconcile.run` / timeout, since several `apply` jobs of one deployment write the same row concurrently. Lock per environment for `beginDeployment`'s "already running" check. |
| `observeNode` = `setObserved` then `reconcile.run` as **two** transactions | Either keep two, or one transaction (strictly safer). |
| `scheduler.runAfter` is durable and part of the mutation's transaction | Durable jobs table (`kind, args, runAt, state`) written in the same transaction as the triggering write, for `apply`, `remove`, `timeoutDeployment`, `proxy.sync`, `removeLegacyTunnels`. Debounced observe scans can be in-memory. |
| Scheduled actions run at most once; failures are only logged | `apply` already records its own failure; `remove` failure is only logged (an orphan `svc-*` remains). Keep, or retry `remove` on 5xx/socket errors (safe: 404 is success). |
| Node actions time out at 10 min, 16 concurrent | Give `apply` a context deadline (≥ 10 min, pulls can take minutes; observed: `postgres:16` 224 s); bound concurrent Docker jobs. |
| Restart of the control plane | On boot: (a) full `observe` sweep; (b) re-arm `timeoutDeployment` for every `running` deployment at `startedAt + 300000` (fire now if overdue); (c) re-run `apply` for steps still `pending`/without `appliedAt` of running deployments if the jobs table did not persist them (`pull = message starts with "redeploy "`); (d) `migrations.run` equivalent; (e) proxy sync. |
| `db.normalizeId` rejects foreign ids | Unknown `svc-<x>` ids are ignored (no error). |
| `_creationTime` float ms | `since` in `worker.config` stays epoch **milliseconds** (number, may be fractional). |

> **Go now:** no jobs table. Jobs live in memory (`internal/adapters/jobs`) and the recovery pass, itself a job queued at start (`Jobs.After("recover", 0, a.Recover)`), re-derives them: (a), (b), (c) and (e) above, plus `keel-agent`; there is no (d).

Docker access stays internal: no HTTP route ever takes a raw image/command/mount; images are validated by the regex in §2.1.

---

## 19. Quirks, open questions, things not to "fix" by accident

1. **Pull errors inside the progress stream are ignored** (dockerode `followProgress`). A pull that 200s then emits `{"error":…}` counts as success; the task then fails/rejects on the node and observation/timeout reports it. Recommended Go change: treat `errorDetail`/`error` lines as a pull failure (same handling as an HTTP error: cached → "pull failed (…), using cached image"; not cached → `applyError` + `stepFailed`). Deliberate deviation; decide explicitly.
2. `createOrUpdate` retries on **any** create/update error (not only "out of sequence"), 3 attempts, no backoff; inspect errors are not retried.
3. Orphan `svc-*` services (node deleted while `remove` failed, or another control plane) are never collected; the sweep only reads labelled services that match known nodes.
4. `summarize.error` uses the last failed task in Docker's API order.
5. `reconcile.run` scans every running deployment on every scan (cheap at current scale).
6. When `stepFailed` fails a deployment, sibling node steps stay `pending`/`running` forever in the stored document.
7. Worker posts one HTTP request per Docker event (not batched despite comments); the control plane accepts arrays/NDJSON, so a Go agent may batch without a protocol change.
8. Worker reads the deprecated top-level `id` of container events; use `Actor.ID`.
9. `nodesInternal.owned` (internal query `{id} → boolean`, "node owned by caller") is dead code; skip it.
10. Docker socket mounted `:ro` does not prevent writes; the worker's GET-only rule is a code invariant.
11. `MAX_BODY` counts UTF-16 code units, not bytes; a byte limit of 262144 in Go is an acceptable equivalent.
12. Image cache check and pull happen on the **manager** daemon only; other servers pull at task start (Swarm executor), so "using cached" says nothing about remote servers.
13. `listDeployable` (sweep) uses `desired.revision > 0`; `worker.config.serviceIds` uses any `desired` (includes never-shipped nodes).

> **Go now (7, 11):** the agent still posts one event per request, as a one-element JSON array, and the route takes only arrays (no single object, no NDJSON); the 256 KiB limit counts bytes.

---

## 20. Function index (this area)

| Name | Kind | Visibility | Args | Returns | Side effects |
| --- | --- | --- | --- | --- | --- |
| `swarm.apply` | action (node) | internal | `{id: nodes id, deploymentId?: deployments id, pull?: bool}` | — | Docker D1–D6; step writers; `setApplyError`; `followPort`; `scheduleObserve` |
| `swarm.remove` | action | internal | `{id}` | — | `DELETE /services/svc-<id>` |
| `swarm.removeLegacyTunnels` | action | internal | `{}` | `number` | D7 |
| `swarm.observeNode` | action | internal | `{id, settle?: number}` | — | D8; `clearObserveScheduled`, `setObserved`, `reconcile.run`, maybe `scheduleObserve(+2000, settle+1)` |
| `swarm.observeSwarmNodes` | action | internal | `{}` | — | `GET /nodes`; `environments.setServers` |
| `swarm.observe` | action | internal | `{}` | — | D9 + D10; `setObserved` ×N, `reconcile.run`, `setServers` |
| `events.ingest` | mutation | internal | `{events: DockerEvent[], resync?: bool}` | — | schedules observe / observeSwarmNodes / observeNode |
| `worker.config` | query | internal (served by `GET /worker/config`) | `{}` | `{sinks: {projectId, serviceIds, sink, since}[]}` | — |
| `nodesInternal.listDeployable` | query | internal | `{}` | `{id, environmentId}[]` | — |
| `nodesInternal.applyInput` | query | internal | `{id}` | `{name, desired, env: string[], oneShot: bool} \| null` | — |
| `nodesInternal.setObserved` | mutation | internal | `{id, observed}` | — | patch observed/deployedRevision/oneShot |
| `nodesInternal.scheduleObserve` | mutation | internal | `{id, delayMs?, settle?}` | `boolean` | debounce (§8.2) |
| `nodesInternal.clearObserveScheduled` | mutation | internal | `{id}` | — | patch |
| `nodesInternal.setApplyError` | mutation | internal | `{id, error?: string}` | — | patch |
| `nodesInternal.followPort` | mutation | internal | `{id, port: number}` | — | patch endpoints; `proxy.sync` |
| `nodesInternal.owned` | query | internal | `{id}` | `boolean` | dead code |
| `reconcile.run` | mutation | internal | `{}` | — | settles steps/deployments |
| `reconcile.timeoutDeployment` | mutation | internal | `{deploymentId}` | — | fails deployment; node `applyError` |
| `deployments.stepRunning/stepLog/stepApplied/stepFailed` | mutation | internal | `{deploymentId, nodeId, text}` | — | `patchStep` |
| `deployments.start` | mutation | public | `{environmentId, only?: id[], refresh?: bool}` | deployment id | `beginDeployment` |
| `environments.setServers` | mutation | internal | `{servers: number}` | — | upsert `cluster` |
| `environments.summary` | query | public | `{environmentId}` | `{pendingChanges, counts: {[status]: n}, servers} \| null` | — |
| `migrations.run` | mutation | internal (CLI `convex run` at install) | `{}` | `{quickTunnelsConverted, redisPasswords, domainsMoved}` | §16.1 |
| `proxyInternal.resync` | mutation | internal (cron 2 min) | `{}` | — | `proxy.sync` |
| `POST /worker/events` | httpAction | bearer `KEEL_WORKER_TOKEN` | Docker events (object / array / NDJSON), header `X-Keel-Resync` | `200 ok` / `400 bad json` / `401 unauthorized` / `413 too large` | `events.ingest` |
| `GET /worker/config` | httpAction | bearer | — | `200` JSON / `401 unauthorized` | — |

Env vars read by the control plane in this area: `KEEL_WORKER_TOKEN` (worker/proxy bearer; required for the routes to accept anything), `KEEL_PUBLIC_IP` (migrations), `KEEL_OTLP_URL` / `CONVEX_SITE_URL` (tracing env injected by apply). Docker socket path is hard-coded `/var/run/docker.sock`.

> **Go now:** `GET /worker/config` entries carry no `projectId` (§12), `POST /worker/events` takes only a JSON array (§11.2), `swarm.removeLegacyTunnels` and `migrations.run` are gone (§6.7, §16.1), and the Docker socket comes from `DOCKER_HOST` (§4).

---

## Addendum (critic)

### A1. Operational log lines (not a contract; keep them for `docker service logs` parity)

Nothing parses these, but they are what an operator greps today, so the Go control plane and
agent should print the same lines (same wording, same `key=value` fields).

> **Go now:** not kept. `keel serve` and `keel agent` log through `log/slog` text handlers on stderr (`time=… level=… msg=… key=value`, errors as `err`), and only start (plus the agent's tailnet join), shutdown and failures: the per-event, per-scan, per-sweep, per-follower, `streaming docker events` and `config applied` lines are gone, and none of the templates below is kept.

**Control plane** (Convex `console.log` / `console.warn`, visible in the backend container logs):

| Where | Line (exact template) | When |
| --- | --- | --- |
| `events.ingest` (`ingestOne`) | `event <type> <action> <serviceName ?? name> → observeNode scheduled\|skipped` | once per distinct `svc-*` id in a batch (also listed in projects.md §8.7) |
| `swarm.observeNode` | `observeNode <id> tasks=<tasks.length> update=<UpdateStatus.State ?? "-"> revision=<observed.revision> state=<observed.state> running=<observed.running> settle=<settle>` | every single-node scan (also projects.md §8.5) |
| `observeServers` (from `observeSwarmNodes` and `observe`) | `observeServers ready=<ready>/<servers.length>` | every `GET /nodes` |
| `swarm.observe` | `observe (full sweep) nodes=<listDeployable().length>` | after every full sweep, **also when there were 0 deployable nodes** (printed before `observeServers`) |
| `otlp.traces` (warn) | `otlp: Axiom unreachable: <message>` / `otlp: Axiom <status>[: <detail>]` | relay failures (observability.md §9.2) |

**Agent** (`apps/worker/src/log.ts`): every line goes to **stderr** as

```
<new Date().toISOString()> [<scope>] <msg>[ <JSON.stringify(extra)>]
```

(UTC ISO-8601 with milliseconds and `Z`; the JSON suffix only when `extra` is given.)
`errorText(err)` = error message (or `String(err)`), whitespace runs collapsed to one space,
trimmed, first 300 chars. Lines by scope:

| Scope | Line (exact template) |
| --- | --- |
| `worker` | `starting on node <Swarm.NodeID ?? "?"> (<Name ?? "?">)` at startup, after `GET /info` |
| `worker` | `<SIGTERM\|SIGINT>, flushing` on shutdown (then flush raced against 5 s, `exit 0`) |
| `config` | `poll failed: <errorText>` |
| `events` | `streaming docker events` or `streaming docker events since <eventsSince>` (after `GET /events` answered) |
| `events` | `docker events stream ended, reconnecting in 2s` / `stream error: <String(err)>, reconnecting in 2s` |
| `events` | `rejected <status>, skipping {"body":"<first 120 chars of the POST body>"}` |
| `events` | `post failed <status>, retry in <n*5+5>s` / `post failed (<errorText>), retry in <n*5+5>s` |
| `logs` | `following <svc-name> (<container id[:12]>) {"since":"<since>"}` (`"now"` when there is no resume point) |
| `logs` | `follow <container id[:12]> failed (<errorText>), retry in 3s` |
| `logs` | `dropping <n> queued lines for a removed sink` |
| `logs` | `config applied {"sinks":<routes>,"services":<serviceIds>}` (only when routing changed) |
| `axiom` | `rejected <status>, dropping <n> events {"text":"<first 200 chars of the response>"}` (4xx except 429) |
| `axiom` | `ingest <status>, retry <attempt+1> {"text":"<first 200 chars>"}` (5xx / 429) |
| `axiom` | `ingest failed (<errorText>), retry <attempt+1>` (network error / 15 s timeout) |
| `axiom` | `unreachable, keeping <n> events for a later attempt` (after 5 attempts; `send` returns false) |
| `state` | `write failed: <String(err)>` (state-file write) |

Go agent recommendation: a tiny `log(scope, msg, extra)` helper that writes exactly this format
to stderr (do not switch the agent to `slog` JSON without updating whoever greps these).
