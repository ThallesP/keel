# Porting spec: projects, environments, canvas nodes, variables, deployments, Ship, reconcile

Source of truth at the time of writing: `packages/backend/convex/{schema,projects,environments,nodes,nodeHelpers,nodesInternal,variables,deployments,reconcile,events,status,access,endpoints,swarm,migrations,tracing}.ts`, `http.ts` (worker events route), the web callers in `apps/web/src/components/canvas/*`, the CLI client `apps/cli/internal/keel/api.go`, and intent in `docs/canvas.md`, `docs/volumes.md`, `docs/workers.md`. Follows `docs/go/ARCHITECTURE.md` (SQLite, ms timestamps, `domain.Error{Code, Message}`, invalidation topics, `Jobs` port).

Neighbouring specs own: Better Auth / sessions / organizations / invitations (auth spec), log sinks + `logs.*` + traces + OTLP relay + `tracing.*` (logs spec), keel-proxy sync / certificates / `proxyInternal` (networking spec), agent internals (workers spec). This file covers what those touch only where a function in this area calls them.

---

## 0. Conventions carried over from Convex (read first)

| Convex behaviour | What the Go port must do |
| --- | --- |
| Every mutation is one serializable transaction (OCC). A thrown error rolls back every write and every `scheduler.runAfter` of that mutation. | One `a.write` per public mutation. Any error → no writes, no jobs, no invalidation. Jobs are enqueued only after commit. |
| No unique constraints; uniqueness (node name per environment, project slug per org, variable key per node, one running deployment per environment) is enforced by read-then-write inside the transaction, made safe by OCC. | SQLite single writer (`BEGIN IMMEDIATE`) gives the same safety. Also add unique indexes: `projects(organization_id, slug)`, `nodes(environment_id, name)`, `variables(node_id, key)`, and a partial unique index `deployments(environment_id) WHERE status='running'`. Map a constraint violation to the same message the read-check would have produced. |
| Optional fields that are `undefined` are absent from JSON (never `null`). A function returning `undefined` sends `null`. | Use `omitempty` / pointer fields for every "optional" below. Where this spec says "returns null", send JSON `null`. |
| `_id` strings, `_creationTime` float ms. Default read order within an index is `_creationTime` ascending. | IDs are opaque strings (`domain.NewID()` for new rows). **If Convex data is imported, keep the Convex IDs verbatim**: node IDs are baked into Swarm service names (`svc-<id>`), labels (`keel.service=<id>`), default sslip domains (hash of the id), and OTel resource attributes. "Creation order" below = insertion order (rowid or a `created_at` ms column + id tiebreak). |
| `Date.now()` | Unix ms, `int64`. Positions and other `v.number()` fields are float64 (`position.x/y` may be fractional). |
| `ConvexError("…")` message is what clients see; the CLI maps a few by exact text (`translate` in `apps/cli/internal/keel/api.go`). | Keep every message below **byte-identical** (note the en dash `–` U+2013 and middle dot `·` U+00B7 where shown). Each error below also lists the suggested `api.Code`. |
| JS string `.length` counts UTF-16 code units; JS `\s` / `.trim()` use the ECMAScript whitespace set. | Count UTF-16 units where a length limit applies (§12). Use the explicit whitespace class in §5.1 for the reference regex. |
| Argument validators (`v.id("nodes")` etc.) reject malformed ids before the handler runs with a non-`ConvexError` (`ArgumentValidationError`), which the CLI shows as `SERVER_ERROR`. | Recommended: treat a malformed id exactly like a missing row (same "not found" behaviour as the handler), except `deployments.get` which already does that explicitly. |

> **Go now:** no JavaScript string semantics. Length limits count runes (`utf8.RuneCountInString`), trimming is `strings.TrimSpace`, the reference regex uses RE2's `\s`, and ports and replicas are integers in the API schema (a fractional port fails request validation with `INVALID_INPUT`; positions stay float64).

Signed-out callers: queries in this area return `null`/`[]` (exceptions: `projects.list`, `nodes.publicAddress` throw `Not authenticated`); mutations that resolve an environment/node throw `Environment not found` / `Node not found` (they do not say "Not authenticated"); `projects.ensureDefault` / `projects.create` throw `Not authenticated`. In the Go port the auth middleware will usually reject first with `NOT_AUTHENTICATED`, which the CLI treats the same; that is acceptable.

**Roles:** no function in this area checks the member role. Any member of the organization can do everything here.

---

## 1. Data model

### 1.1 `projects`

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `_id` | id | yes | |
| `name` | string | yes | Display name, trimmed, 1–60 UTF-16 units (only enforced by `projects.create`). |
| `slug` | string | yes | URL/CLI name, `[a-z0-9-]`, ≤40, no leading/trailing `-`. Unique per organization. |
| `organizationId` | string | optional | Better Auth organization id. Unset only on legacy rows from before organizations (§9.1 `ensureDefault` adopts them). |
| `ownerId` | string | optional | Legacy (pre-organization owner user id). Cleared on adoption. Drop in Go if the import adopts legacy rows. |

Indexes: `by_organization(organizationId)`, `by_slug(organizationId, slug)`.

### 1.2 `environments`

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `projectId` | id→projects | yes | |
| `name` | string | yes | Always `"production"` today. |
| `isProduction` | bool | yes | Always `true` today. |

Index: `by_project(projectId)`. There is no API to create, rename or delete environments (the web environment switcher is "not wired"). Every project gets exactly one production environment at creation.

### 1.3 `nodes` (canvas items: services, databases, caches, volumes, groups)

| Field | Type | Req | Meaning / who writes it |
| --- | --- | --- | --- |
| `environmentId` | id→environments | yes | |
| `type` | `"service" \| "database" \| "cache" \| "volume" \| "group"` | yes | Immutable after create. |
| `name` | string | yes | Unique within the environment (references resolve by name). `[a-z0-9-]{1,40}` when user-supplied; auto-generated names are not re-validated (§3.4). |
| `parentId` | id→nodes | optional | Group membership; `position` is then relative to the group. **No public mutation sets it** (only copied by `duplicate`, cleared by group `remove`). |
| `position` | `{x: number, y: number}` | yes | Canvas position (float). Written by `create`, `move`, `duplicate`, group `remove`. |
| `config` | `{sizeGb?: number, width?: number, height?: number}` | yes | Non-deploy settings. Volume: `{sizeGb: 10}`. Group: `{width: 300, height: 180}`. Others: `{}`. Never updated after create. |
| `desired` | `Desired` | optional | Present iff type ∈ {service, database, cache}. What the user asked for. Only user-triggered mutations write it. |
| `observed` | `Observed` | optional | What Swarm reports. Replaced wholesale by observe (`setObserved`) only. |
| `endpoints` | `Endpoint[]` | optional | Public ingress (keel-proxy). Set by `expose`/`unexpose` (immediate, not Ship-gated), `followPort`, proxy status writers, migrations. Absent (not `[]`) when none. |
| `public`, `ingress` | any | optional | Legacy Cloudflare Quick Tunnel fields; `migrations.run` converts and clears them. **Drop in Go** (import-time conversion, §9.9). |
| `deployedRevision` | number | optional | Last `observed.revision` seen fully converged (`setObserved`). |
| `dirty` | bool | optional (treat absent as `false`) | Staged change not yet shipped. Drives "Ship · N changes". §6. |
| `shippedAt` | number (ms) | optional | When the current `desired.revision` was shipped (`beginDeployment`). Drives "deploying · 41s" and `stoppedAt`. |
| `applyError` | string | optional | Last `swarm.apply` failure text, or deployment timeout text. Cleared by `beginDeployment` and by a successful apply. Forces status `error`. |
| `oneShot` | bool | optional | Learned by observe: last run exited 0 with nothing running. Makes `toSpec` use `FailureAction: "continue"`. |
| `observeScheduled` | id→_scheduled_functions | optional | Debounce handle of the pending `observeNode`. **Do not port as a column**: keep it in the in-memory job scheduler (§8.6). |

Index: `by_environment(environmentId)`.

`Desired`:

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `image` | string | yes | Image reference (validated, §2.3). |
| `revision` | number (int) | yes | `0` = never shipped. Incremented by 1 by every `beginDeployment` that includes the node. Written to Swarm as label `keel.revision`. |
| `replicas` | number (int 0–20) | yes | |
| `port` | number (int 1–65535) | optional | Container port; used for `PORT`/`URL`/`DATABASE_URL` and endpoints. Not published on the host. |
| `tracing` | bool | optional | Service only. When `true`, apply adds `OTEL_*` env (§8.2). Absent = off (the setter deletes the key rather than writing `false`). |

`Observed`:

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `revision` | number | yes | Max of the service spec label and every task's `keel.revision` label; `0` when the service and tasks are gone. |
| `running` | number | yes | Tasks of `revision` with `DesiredState=running` and `Status.State=running`. |
| `completed` | number | optional | Only when `state="completed"`: count of tasks of `revision` in `complete`. |
| `finishedAt` | number (ms) | optional | Only when `state="completed"`: max `Status.Timestamp` of those tasks. |
| `state` | `"ok" \| "updating" \| "crashloop" \| "pending" \| "failed" \| "completed"` | yes | §8.4. |
| `nodeIds` | string[] | yes | Distinct Swarm node IDs running tasks (JSON column in Go). |
| `error` | string | optional | Last failed task's `Status.Err`, else Swarm's `UpdateStatus.Message` on rollback. |
| `at` | number (ms) | yes | Scan time. |

> **Go now:** `Observed` has no `nodeIds` (written on every scan, never read). `Desired.Port`, `Observed.Completed`, `Observed.FinishedAt` and `deployedRevision` are plain ints, 0 when unset (`NOT NULL DEFAULT 0` columns); `api.NodeView` keeps its optional JSON fields. The legacy `public` / `ingress` fields do not exist.

`Endpoint` (child table in Go):

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `protocol` | `"http" \| "tcp" \| "udp"` | yes | |
| `port` | number | yes | Container port the proxy dials (`svc-<id>:<port>`). |
| `pinnedPort` | bool | optional | Set (only ever to `true`) when exposed with a port other than `desired.port`. Unpinned endpoints follow the node's port on each ship (`followPort`). |
| `domain` | string | optional | http only. Unique install-wide. |
| `publicPort` | number | optional | tcp/udp only. Unique per protocol install-wide. |
| `status` | `{state: "starting"\|"live"\|"failed", error?: string, at: number}` | yes | Written by expose (starting), followPort (starting), migrations (starting), proxy sync/cert reports (networking spec). |

Endpoint identity key (`endpointKey`): `http:<domain>` or `<protocol>:<publicPort>`.

### 1.4 `variables`

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `nodeId` | id→nodes | yes | Any node type may have variables (no type check). |
| `key` | string | yes | `^[A-Z_][A-Z0-9_]{0,63}$`. Unique per node (enforced by `set`). |
| `value` | string | yes | ≤4096 UTF-16 units. May contain references `${{ node.KEY }}` / `${{ KEY }}` (§5). |
| `secret` | bool | yes | Mask the raw value in UI. |

Index: `by_node(nodeId)`. Order = creation order; a key rename (patch) keeps the row's position. This order is the container `Env` order.

### 1.5 `deployments`

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `environmentId` | id→environments | yes | |
| `sha` | string | optional | **Never written** today (no builder). Keep the field, omit in JSON. |
| `message` | string | yes | `"<verb> <name>, <name>"` (§7.1). |
| `status` | `"running" \| "success" \| "failed"` | yes | |
| `startedAt` | number | yes | |
| `finishedAt` | number | optional | Set when status leaves `running`. |
| `steps` | `DeployStep[]` | yes | One per affected node in node creation order, then one final `{label: "health checks"}` step without `nodeId`. |
| `log` | `{at: number, nodeId?: id, text: string}[]` | yes | Append-only, capped to the **last 500** entries. |

`DeployStep`: `{nodeId?: id, label: string (node name at ship time), status: "pending"|"running"|"done"|"failed", startedAt?: number, appliedAt?: number, finishedAt?: number}`. `appliedAt` = swarm.apply created/updated the service; observe settles from there.

Indexes: `by_environment(environmentId)`, `by_status(status)`. Deployments are never deleted; `steps[].nodeId` can dangle after a node delete.

Go: `deployments`, `deployment_steps(deployment_id, idx, node_id NULL, label, status, started_at, applied_at, finished_at)`, `deployment_log(deployment_id, seq, at, node_id NULL, text)` (trim to 500 per deployment on insert). The JSON shape returned to clients is still the nested document of §9.5.

> **Go now:** `domain.Deployment` has no `sha` (nothing ever wrote one); the column stays and `api.Deployment` keeps the always-omitted field. Running deployments are read without their log (reconcile and the recovery pass only look at steps).

### 1.6 `cluster`

Single row `{servers: number, at: number}`: ready Swarm node count. Install-wide (not per org). Written by `environments.setServers` (from observe). Read by `environments.summary`.

### 1.7 Tables owned by other specs but read here

- `otlpKeys {environmentId, key}` (one per environment): `withTracing` reads the key (logs spec).
- `logSinks`: `tracing.forNode` reads it (logs spec).
- Better Auth `organization`, `member` rows: `currentMembership`, `joinOrFound` (auth spec).

---

## 2. Access control and validation

### 2.1 Access helpers (`access.ts`)

| Helper | Rule | Failure |
| --- | --- | --- |
| `requireUser` | session user present | throw `Not authenticated` (`NOT_AUTHENTICATED`) |
| `currentMembership` | user → Better Auth `member` row (`findOne where userId`) → `{user, organizationId, role}`; null if signed out or no row. One membership per user (one org per install). | returns null |
| `ownedProject(id)` | membership && project exists && `project.organizationId === membership.organizationId` | null |
| `ownedEnvironment(id)` | environment exists && `ownedProject(environment.projectId)` → `{environment, project}` | null |
| `ownedNode(id)` | node exists && `ownedEnvironment(node.environmentId)` → `{node, environment, project}` | null |
| `requireEnvironment(id)` | `ownedEnvironment` | throw `Environment not found` (`PROJECT_NOT_FOUND`, the CLI's mapping) |
| `requireNode(id)` | `ownedNode` | throw `Node not found` (`SERVICE_NOT_FOUND`) |

`NO_ORGANIZATION` message: `You're not in an organization yet. Ask a member for an invite link.` (ASCII apostrophe). Code `NO_ORGANIZATION`.

### 2.2 Validators (exact messages)

| Function | Rule | Message | Code |
| --- | --- | --- | --- |
| `validName(name)` | `^[a-z0-9-]{1,40}$` | `Name: 1–40 chars, a-z 0-9 and - only` | `INVALID_INPUT` |
| `validEnvKey(key)` | `^[A-Z_][A-Z0-9_]{0,63}$` | `Key: UPPER_SNAKE_CASE only` | `INVALID_INPUT` |
| `validImage(image)` | `^[a-z0-9][a-z0-9._\-/:@]{0,199}$` (so ≤200 chars, no uppercase, no spaces) | `Image must look like repo/name:tag` | `INVALID_INPUT` |
| `validPort(port)` | if given: integer and 1 ≤ p ≤ 65535 (`Number.isInteger`: `80.0` ok, `80.5` rejected) | `Port must be 1–65535` | `INVALID_INPUT` |
| `validReplicas(r)` | if given: integer and 0 ≤ r ≤ 20 | `Replicas must be 0–20` | `INVALID_INPUT` |
| `validDomain(raw)` | `d = lower(trim(raw))` minus one trailing `.`; `len(d) ≤ 253`, ≥2 labels, each label `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`; returns normalized `d` | `Domain must look like app.example.com` | `INVALID_INPUT` |

`validPort`/`validReplicas` return the input (so `undefined` passes through and `0` replicas is a real value: callers use `??`, not `||`).

---

## 3. Node kinds, defaults, engines, credentials, naming

### 3.1 Type defaults (`DEFAULTS`)

| type | default name | default image | default port | desired? | dirty at create | config at create |
| --- | --- | --- | --- | --- | --- | --- |
| `service` | `service` | `nginx:alpine` | 80 | yes | `true` | `{}` |
| `database` | `postgres` | `postgres:16` | 5432 | yes | `true` | `{}` |
| `cache` | `redis` | `redis:7` | 6379 | yes | `true` | `{}` |
| `volume` | `data` | — | — | no | `false` | `{sizeGb: 10}` |
| `group` | `group` | — | — | no | `false` | `{width: 300, height: 180}` |

`DEPLOYABLE = {service, database, cache}`. Volume and group never reach Swarm.

### 3.2 Engines (`ENGINES`, the Add dialog's "Database" choices)

| engine | node type | image | port |
| --- | --- | --- | --- |
| `postgres` | database | `postgres:16` | 5432 |
| `mysql` | database | `mysql:8` | 3306 |
| `mongo` | database | `mongo:7` | 27017 |
| `redis` | cache | `redis:7` | 6379 |

`engineOf(image)`: `repo = image.split("@")[0].split("/").last.split(":")[0]`; returns `repo` if it is one of the four keys, else none. Examples: `postgres:16`→postgres, `docker.io/library/mysql:8.4`→mysql, `nginx`→none. Used for: credential seeding, `DATABASE_URL` scheme, Redis `--requirepass`, Redis expose guard. Note it is image-based, so a `service` whose image is `redis:7` also gets the Redis expose guard and `--requirepass` args.

### 3.3 Seeded variables (generated credentials), inserted by `nodes.create` for type database|cache only, in this order

| engine (`engineOf(desired.image)`) | rows `(key, value, secret)` |
| --- | --- |
| postgres | `POSTGRES_USER=app` (false), `POSTGRES_PASSWORD=<rand>` (true), `POSTGRES_DB=app` (false) |
| mysql | `MYSQL_ROOT_PASSWORD=<rand>` (true), `MYSQL_USER=app` (false), `MYSQL_PASSWORD=<rand>` (true), `MYSQL_DATABASE=app` (false) |
| mongo | `MONGO_INITDB_ROOT_USERNAME=app` (false), `MONGO_INITDB_ROOT_PASSWORD=<rand>` (true) |
| redis | `REDIS_PASSWORD=<rand>` (true) |
| none | nothing |

`randomSecret(20)`: 20 chars drawn from the 56-char alphabet `abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789` (no `l o I O 0 1`). TS uses `Math.random`; **Go must use `crypto/rand`** (uniform, e.g. rejection sampling). Redis's image reads no password from env: apply turns `REDIS_PASSWORD` into `redis-server --requirepass <pass>` (§8.2).

### 3.4 Naming

- `uniqueName(base, taken)`: `base` if free, else `base-2`, `base-3`, … first free. Does **not** validate or truncate, so a 40-char base can yield a 42-char name that `validName` would reject and that the reference regex (`[a-z0-9-]{1,40}`) cannot address. Preserve or clamp (recommended: clamp base to 40 − len(suffix)); flag as a known quirk.
- `nameFromImage(image, fallback)`: `repo` as in `engineOf`, then lowercase, replace every run of `[^a-z0-9-]+` with `-`, trim leading/trailing `-`, then take the first 40 chars (may end in `-`). Empty → `fallback`. `ghcr.io/acme/api-server:1.2` → `api-server`.
- Auto name on create: `engine` key if given (`postgres`, `mysql`, `mongo`, `redis`), else `nameFromImage(runsImage, DEFAULTS[type].name)` where `runsImage` = the final image or the type's default image, else (volume/group) the type's default name. So a default service is `nginx`, a default database `postgres`, a default cache `redis`, volume `data`, group `group`. Then `uniqueName` within the environment.
- Duplicate name: `uniqueName(name[0:32] + "-copy", taken)` → `api-copy`, `api-copy-2`.

### 3.5 Positions

- Supplied by the web (drop point, rounded to integers by the client).
- Omitted by the CLI → `nextPosition(siblings)`: over every node of the environment **without** `parentId`, candidate `x = position.x + (config.width ?? 220) + 60`; take the node with the strictly greatest candidate (first wins ties), result `{x: candidate, y: thatNode.position.y}`; no top-level nodes → `{x: 0, y: 0}`.
- `move` writes any numbers, no validation, no dirty.
- Duplicate: `{x + 40, y + 40}`.
- Removing a group re-parents children to top level with absolute position `group.position + child.position`.

### 3.6 Edges

None. Edges were removed 2026-09-26; wiring is variable references only. There is no edges table.

### 3.7 Volumes and groups today vs intent

- Volume nodes are canvas-only labels (`config.sizeGb`), status always `pending`, never mounted. `docs/volumes.md` (pin + backup, `volumes`/`backups`/`migrations`/`orphans`/`jobs` tables, `vol-<id>` mounts, stop-first) is **design intent, not implemented**; do not port it as behaviour.
- **No `Mounts` in any Swarm spec today.** A database's data lives in whatever anonymous volume its image declares, which belongs to the task's container; a new task (Redeploy/Start/rollout) does not get the old data. Port as-is; do not silently add mounts without the volumes design.
- Groups: no API to set `parentId` or resize. Only create/rename/move/remove apply. `duplicate` refuses groups.

---

## 4. Status model

### 4.1 `converged(node)` (exact)

```ts
if (!desired || !observed) return false;
if (desired.replicas === 0) return observed.running === 0;          // revision NOT checked here
if (observed.revision !== desired.revision) return false;
if (observed.state === "completed") return (observed.completed ?? 0) >= desired.replicas;
return observed.state === "ok" && observed.running >= desired.replicas;
```

### 4.2 `deriveStatus(node)` → `"healthy"|"done"|"deploying"|"stopping"|"error"|"stopped"|"pending"` (never stored)

```ts
if (!desired || desired.revision === 0) return "pending";      // never shipped; also every volume/group
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
```

Notes: a node whose Swarm service vanished out of band gets `observed.revision = 0`, so with replicas > 0 it reads `deploying` until a deployment timeout sets `applyError`. `observed.state = "pending"` (unschedulable) reads `deploying`.

### 4.3 `NodeView` (what `nodes.list` returns per node; `view()` in `nodeHelpers.ts`)

| JSON field | Type | Derivation |
| --- | --- | --- |
| `id` | string | `_id` |
| `type` | string | |
| `name` | string | |
| `parentId` | string? | |
| `position` | `{x, y}` | |
| `config` | `{sizeGb?, width?, height?}` | raw |
| `dirty` | bool | `dirty ?? false` |
| `status` | NodeStatus | `deriveStatus` |
| `image` | string? | `desired.image` |
| `port` | number? | `desired.port` |
| `replicas` | number | `desired.replicas ?? 0` |
| `running` | number | `observed.running ?? 0` |
| `revision` | number | `desired.revision ?? 0` |
| `deployedRevision` | number? | |
| `public` | bool | `endpoints.length > 0` |
| `publicUrl` | string? | `endpointAddress` of the **first** `http` endpoint in array order (`https://<domain>`) |
| `endpoints` | `EndpointView[]` | always an array (`[]` when none) |
| `error` | string? | `applyError ?? (status === "error" ? observed.error : undefined)` |
| `deploy` | `{step, startedAt}`? | only when `status === "deploying"` **and** `shippedAt` set. `step`: `"pulling image"` if `!observed || observed.revision < (desired.revision ?? 0)`; else `"rolling out"` if `observed.state === "updating"`; else `"starting"`. `startedAt = shippedAt`. |
| `stoppedAt` | number? | `shippedAt` when status is `stopped` or `stopping` |
| `finishedAt` | number? | `observed.finishedAt` when status is `done` |

`EndpointView`: `{protocol, port, domain?, publicPort?, address, state, error?}` with `address = "https://<domain>"` (http) or `"<KEEL_PUBLIC_IP or literally <public IP>>:<publicPort>"` (tcp/udp), `state = status.state`, `error = status.error`.

UI mapping (client side, for reference): healthy `●●● Online`, done `✓ Completed 4s ago`, deploying `● Deploying · <step> <elapsed>`, error `Crashed` + error pill, stopping `Stopping…`, stopped `Stopped 2h ago`, pending `○ Not deployed`. Node toolbar per status: pending → Deploy; healthy/error → Redeploy, Stop (+ Restart in ⋯); done → Run again (+ Redeploy in ⋯); stopped → Start; deploying/stopping → none. Expose offered when status ≠ pending.

---

## 5. Variables and references

### 5.1 Syntax

```
REF_RE = /\$\{\{\s*(?:([a-z0-9-]{1,40})\.)?([A-Z_][A-Z0-9_]{0,63})\s*\}\}/g
```

- Group 1 (optional) = target node **name** in the same environment; absent = the row's own node (`${{ POSTGRES_USER }}`).
- Group 2 = key.
- Anything that does not match (e.g. lowercase key `${{ pg.url }}`) is literal text.
- Go RE2 `\s` is ASCII-only and lacks `\v`; use the ECMAScript set explicitly: `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*` in both `\s*` positions. Matches are non-overlapping, left to right (`FindAllStringSubmatchIndex`).

> **Go now:** `canvasRefRE` (`internal/domain/reference.go`) uses RE2's own `\s`; the ECMAScript set is not emulated.

### 5.2 Keys a node answers to

1. Its own variables (always win over 2.).
2. "Provided" keys, computed, never stored, only when the node has `desired` (so volumes/groups provide nothing). Credentials are read through a getter that returns the node's **own** variable (fully expanded, depth+1) or a fallback:

| node | key | value | secret |
| --- | --- | --- | --- |
| database, `engineOf(image)=mysql` | `DATABASE_URL` | `mysql://{enc(MYSQL_USER\|"app")}:{enc(MYSQL_PASSWORD\|"")}@svc-<id>:{port ?? 3306}/{enc(MYSQL_DATABASE\|"app")}` | true |
| database, `mongo` | `DATABASE_URL` | `mongodb://{enc(MONGO_INITDB_ROOT_USERNAME\|"app")}:{enc(MONGO_INITDB_ROOT_PASSWORD\|"")}@svc-<id>:{port ?? 27017}` (no db path) | true |
| database, anything else (postgres or custom image) | `DATABASE_URL` | `postgres://{enc(POSTGRES_USER\|"app")}:{enc(POSTGRES_PASSWORD\|"")}@svc-<id>:{port ?? 5432}/{enc(POSTGRES_DB\|"app")}` | true |
| cache (any image) | `REDIS_URL` | `redis://default:{enc(pass)}@svc-<id>:{port ?? 6379}` if `REDIS_PASSWORD` (own, expanded, fallback `""`) non-empty, else `redis://svc-<id>:{port ?? 6379}` | `pass != ""` |
| service with `port` | `URL` | `http://svc-<id>:<port>` | false |
| every node with desired | `HOST` | `svc-<id>` | false |
| every node with desired and `port` | `PORT` | decimal string of `port` | false |

Order of provided keys (matters for `referenceable`): the URL key first (`DATABASE_URL`/`REDIS_URL`/`URL`), then `HOST`, then `PORT`.

`enc` = JavaScript `encodeURIComponent`: UTF-8 bytes, keep `A–Z a–z 0–9 - _ . ! ~ * ' ( )`, everything else `%XX` uppercase hex. **Not** Go's `url.QueryEscape`/`PathEscape` (they differ on space, `!`, `'`, `(`, `)`, `*`, `~`, `$`, `&`, `+`, …). Write a dedicated function and test it.

> **Go now:** no `encodeURIComponent` clone. Connection URLs are built with `net/url` (`url.URL` plus `url.UserPassword`), which escapes the userinfo the way URL parsers expect.

### 5.3 Resolution algorithm (`resolver(environmentId)`)

State per resolution: `byName` = all nodes of the environment (every type) by name; a per-node cache of own variable rows (each node's rows load once).

```
expand(node, value, depth=0) -> {resolved, secret, parts}
  for each REF_RE match m in value (left to right):
    append preceding text to resolved and to parts as {text}
    target = (m.name absent) ? node : byName[m.name]
    hit = (target && depth < 5) ? lookup(target, m.key, depth) : null
    parts += {ref: {node: m.name (absent for self), nodeId: target?.id, key: m.key, missing: hit == null}}
    if hit: resolved += hit.value; secret ||= hit.secret        // miss contributes ""
  append trailing text

lookup(target, key, depth) -> {value, secret} | null
  row = target's own row with key
  if row: inner = expand(target, row.value, depth+1)               // unqualified refs inside are relative to target
          return {value: inner.resolved, secret: row.secret || inner.secret}
  get(k, fallback) = own row k ? expand(target, row.value, depth+1).resolved : fallback
  return provided(target, get)[key] ?? null
```

- `MAX_DEPTH = 5`: chains/cycles terminate; anything past depth 5 resolves to `""`. Example: `A = x${{ A }}` resolves to six `x`s; `A = ${{ A }}` resolves to `""` but its top-level part is **not** `missing` (the depth-0 lookup hit).
- `parts` are reported only for the top-level value (what `variables.list` returns).
- Missing target name, missing key, or depth exhaustion → `missing: true`, contributes `""`.
- A text part is emitted only when non-empty (`m.index > last`, trailing `last < len`).

### 5.4 `computeEnv(node)` (container env)

`[ "<KEY>=<expand(node, row.value).resolved>" for row in node's own rows in creation order ]`. Provided keys are **not** added (a database container does not get `DATABASE_URL`). Tracing variables are appended after (§8.2). References resolve at apply time, so every ship sees current values.

### 5.5 Rewriting references

- `referencing(node)`: every variable row in the environment with at least one match that `pointsAt` the node, where `pointsAt(name, rowNodeId, node) = name absent ? rowNodeId == node.id : name == node.name`.
- `rewriteReferences(node, to)`: for each such row, replace every match that points at the node with `${{ <newKey> }}` (unqualified form kept, self) or `${{ <newName>.<newKey> }}` (qualified); matches that do not point at the node are kept byte-for-byte. Patch only if the string changed. Side effect: rewritten references are normalized to single spaces inside the braces.
- Node rename: `to = key => {name: newName, key}`. Not dirty (resolved values do not change; but note `OTEL_SERVICE_NAME` uses the node name and will only change on the next ship of that service).
- Variable key rename (`set` with `previousKey`): `to = k => {name: node.name, key: k === previousKey ? newKey : k}` (all references to the node are normalized, only `previousKey` changes). Runs after the row patch.

### 5.6 Dirty propagation (`markReferrersDirty(node)`)

```
nodes = all nodes of node.environmentId; byName = name -> id
referrers: target id -> set of referrer node ids
for each node n, each own row of n, each match m:
    target = m.name absent ? n.id : byName[m.name]
    if !target || target == n.id: continue        // self references never count
    referrers[target].add(n.id)
BFS from node.id (seen = {node.id}); every newly reached id gets patch {dirty: true}
```

Transitive (`api` → `worker.QUEUE_URL` → `redis.REDIS_URL`). The starting node is not marked here (callers mark it). Marks any node type (a group with variables gets `dirty: true` even though it never ships; harmless, never counted).

---

## 6. The dirty / Ship model

- There is no graph diff: `dirty` is a sticky boolean per node. Reverting a change by hand does not clear it. Only `beginDeployment` clears it (for the nodes it ships).
- `pendingChanges` (Ship button "Ship · N changes", CLI `keel status`) = number of nodes in the environment with `dirty && type ∈ DEPLOYABLE`.

| Write | Node itself | Referrers (transitive) |
| --- | --- | --- |
| `nodes.create` (deployable) | `dirty: true` (volume/group: `false`) | — |
| `nodes.duplicate` | `dirty: DEPLOYABLE(type)` | — |
| `nodes.setDesired` (image/port/replicas) | `true` (always, even if unchanged) | yes |
| `nodes.stop` / `nodes.start` | `true`, then immediately shipped in the same transaction | no |
| `variables.set` | `true` | yes |
| `variables.remove` (row existed) | `true` | yes |
| `nodes.remove` | (row deleted) | yes (their refs now resolve to `""`) |
| `tracing.setEnabled` (switch changed) | `true` | no |
| `migrations.run` (Redis given a password) | `true` | yes |
| `nodes.move`, `nodes.rename`, `nodes.expose`, `nodes.unexpose` | no | no |
| `beginDeployment` | `dirty: false` for every shipped node | — |

Ship (topbar button / `⌘↵` / `keel ship`) = `deployments.start({environmentId})` with no `only`: ships every dirty deployable node. Per-node actions ship only that node (`only: [id]`) regardless of its dirty flag and clear it.

---

## 7. Deployment lifecycle

### 7.1 `beginDeployment(environmentId, {only?, refresh=false, verb?})` (shared by `deployments.start`, `nodes.create{deploy}`, `nodes.start`, `nodes.stop`)

Inside the caller's transaction, in this order:

1. If any deployment of the environment has `status = "running"` → throw `A deployment is already running` (`DEPLOYMENT_RUNNING`).
2. `nodes` = all nodes of the environment (creation order). `wanted = only ? Set(only) : null` (an empty array is still a set → nothing matches).
3. `affected = nodes.filter(n => n.desired && DEPLOYABLE(n.type) && (wanted ? wanted.has(n.id) : n.dirty))`. Ids in `only` that are non-deployable, from another environment or unknown are silently ignored.
4. Empty → throw `Nothing to ship` (`NOTHING_TO_SHIP`).
5. `now`; for each affected: `desired.revision += 1`, `dirty = false`, `shippedAt = now`, `applyError` cleared.
6. `steps = affected.map(n => {nodeId: n.id, label: n.name, status: "pending"})` + `{label: "health checks", status: "pending"}`.
7. `word = verb ?? (wanted ? (refresh ? "redeploy" : "deploy") : "ship")`; `message = word + " " + affected.names.join(", ")`.
8. Insert deployment `{environmentId, message, status: "running", startedAt: now, steps, log: []}`.
9. After commit: for each affected, job `swarm.apply({id, deploymentId, pull: refresh})` now; one job `reconcile.timeoutDeployment({deploymentId})` at `now + 5 min` (`DEPLOY_TIMEOUT_MS = 300000`).
10. Return the deployment id.

Messages by entry point:

| Entry point | message |
| --- | --- |
| `deployments.start` without `only` (Ship) | `ship a, b` |
| `deployments.start` with `only`, `refresh=false` (web Restart, Run again; CLI) | `deploy a` |
| `deployments.start` with `only`, `refresh=true` (web Redeploy, Retry) | `redeploy a` |
| `nodes.create {deploy: true}` | `deploy a` |
| `nodes.start` on `revision 0` / otherwise | `deploy a` / `start a` |
| `nodes.stop` | `stop a` |

### 7.2 `swarm.apply({id, deploymentId?, pull=false})` (internal job; Docker calls in §8)

Outside any transaction. `step(fn, text)` writes deployment progress only when `deploymentId` is set.

1. `input = applyInput(id)` = `{name, desired, env: withTracing(node, computeEnv(node)), oneShot: node.oneShot ?? false}`, or null if the node is gone / has no desired → **return silently** (the step stays `pending`; `reconcile.run` scheduled by `nodes.remove` fails it with "node deleted").
2. `cached = image inspect(desired.image)` (404 → none).
3. If `cached && !pull`: `stepRunning("using cached <image>")`. Else: `stepRunning("pulling <image>")`; pull; on success `stepLog("pulled <image> in <seconds with 1 decimal>s")` (e.g. `pulled nginx:alpine in 3.4s`); on failure: if `cached` → `stepLog("pull failed (<errorText>), using cached image")` and continue, else go to error handling.
4. `stillWanted()` = `applyInput(id) != null`. False → return silently.
5. `created = createOrUpdate(toSpec(id, desired, env, oneShot))`.
6. If `created && !stillWanted()` → remove `svc-<id>` (404 ignored) and return (no orphan service).
7. `stepApplied(created ? "service created · <replicas> replica(s)" : "service updated · revision <revision>")`.
8. `setApplyError(id, undefined)` (clear).
9. If `desired.port` → `followPort(id, port)`: every endpoint with `!pinnedPort && port != desired.port` gets `port = desired.port`, `status = {state: "starting", at: now}`; if any moved, job `proxy.sync`.
10. Any exception in 2–9: `text = errorText(err)` (message, whitespace runs collapsed to one space, trimmed, first 300 chars); `setApplyError(id, text)`; `stepFailed("error: <text>")`; return (no observe scheduled).
11. On success: `scheduleObserve(id)` (debounced 500 ms, §8.6) so a scan lands after `stepApplied` even if the event burst already went by.

`errorText` in TS wraps Docker HTTP errors as `(HTTP code <n>) <reason> - <docker message> ` (docker-modem); Go messages will differ (they are display-only; nothing parses them, except that the UI highlights lines matching `/error:|crash loop|timed out|node deleted/`).

Progress writers (internal mutations, all no-ops if the deployment row is gone). `patchStep(deploymentId, nodeId, change, text?)`:

- Apply `change` to the step whose `nodeId` matches.
- If `change.status === "failed"`: the health step (no `nodeId`) also becomes `{status: "failed", finishedAt: now}`, and if the deployment is still `running` it becomes `{status: "failed", finishedAt: now}`.
- If `text`: append `{at: now, nodeId, text}` to `log`, keep the last 500.

| Writer | change |
| --- | --- |
| `stepRunning` | `{status: "running", startedAt: now}` + text |
| `stepLog` | `{}` + text |
| `stepApplied` | `{appliedAt: now}` + text (status stays `running`) |
| `stepFailed` | `{status: "failed", finishedAt: now}` + text |

Note (preserve or fix knowingly): a failed step fails the deployment immediately, but sibling node steps keep running; their later writes still land on the (now `failed`) deployment, and since `reconcile.run` only looks at `running` deployments those siblings can stay `running` forever in the record. A new Ship is allowed as soon as the deployment is `failed`, so an older apply can still be in flight. Two applies for the same node can therefore race; the later `createOrUpdate` wins even if it carries an older revision. Recommended Go improvement: serialize applies per node (job key `apply:<nodeId>`) and re-read `desired.revision` right before `createOrUpdate`, skipping if a newer revision exists.

### 7.3 Observation → `setObserved` → `reconcile.run`

- `nodesInternal.setObserved({id, observed})`: if node gone → no-op. Patch:
  - `observed` (whole object),
  - `deployedRevision = (converged({...node, observed}) && observed.revision > 0) ? observed.revision : node.deployedRevision`,
  - `oneShot = observed.state === "completed" ? true : observed.running > 0 ? false : node.oneShot`.
- After each `observeNode` (and once after a full sweep and after a node delete): `reconcile.run`.

### 7.4 `reconcile.run` (internal; settles every `running` deployment)

Equivalent and cheaper in Go: only the deployments of the affected environment (steps only reference nodes of their own environment). Exact rules:

```
for d in deployments where status == "running":
  log = copy(d.log); steps = []
  for step in d.steps:
    if !step.nodeId || step.status in {done, failed}: keep; continue
    node = get(step.nodeId)
    if step.status == "pending" && node: keep; continue          // waits for apply
    [next, text] = settle(step, node, now); push next; if text: log += {at: now, nodeId: step.nodeId, text}
  nodeSteps = steps with nodeId; health = step without nodeId
  anyFailed = some nodeStep failed
  allDone   = every nodeStep done
  allApplied = every nodeStep (status != pending && appliedAt set)
  if health:
    if anyFailed: health = {status: failed, finishedAt: now}
    elif allDone: health = {status: done, finishedAt: now, startedAt: health.startedAt ?? now}
                  log += {at: now, text: d.message startsWith "stop " ? "stopped" : "all replicas healthy"}
    elif allApplied && health.status == pending: health = {status: running, startedAt: now}
  status = anyFailed ? failed : allDone ? success : running
  patch d {steps, log: last 500, status, finishedAt: status == running ? unset : now}
```

`settle(step, node, now)`:

| condition (in order) | result step | log text |
| --- | --- | --- |
| node deleted | `failed`, `finishedAt` | `<label>: node deleted` |
| `!step.appliedAt \|\| !node.observed \|\| !node.desired` | unchanged | — |
| `observed.revision === desired.revision` and state `crashloop` | `failed` | `<label>: crash loop` + (` · <observed.error>` if any) |
| same revision and state `failed` | `failed` | `<label>: rolled back by Swarm` + (` · <error>`) |
| same revision and `converged(node)` | `done`, `finishedAt` | `completed` → `<label>: ran to completion`; replicas 0 → `<label>: stopped`; else `<label>: <running>/<replicas> replicas running` |
| otherwise | unchanged | — |

`<label>` is the step label (node name at ship time). The TS patches every running deployment on every run even when nothing changed; in Go, write and invalidate only when something changed.

### 7.5 `reconcile.timeoutDeployment({deploymentId})` (the only per-deployment timer)

At `startedAt + 5 min`: if the deployment is missing or not `running` → no-op. Otherwise every step not `done`/`failed` → `{status: "failed", finishedAt: now}`; for node steps: `why = node?.observed?.error ? " · " + error : ""`, log `<label>: timed out waiting for replicas<why>`, and if the node exists `applyError = "timed out waiting for replicas<why>"` (node goes red until the next ship). The health step fails without a log line. Deployment → `failed`, `finishedAt`.

Go: this job must survive restarts. On `serve` start, for every `running` deployment, re-arm the timeout at `max(now, startedAt + 5 min)` (the ARCHITECTURE recovery pass covers observes; add this).

### 7.6 Deployment status as clients see it

- Ship button: running → `Shipping… <done steps>/<all steps incl. health> <elapsed>`, not clickable; latest failed with at least one failed node step whose node still exists → `Retry`; else `Ship · N changes` / `Ship`.
- **Retry is client-side**: `deployments.start({environmentId, only: <nodeIds of failed steps that still exist>, refresh: true})` → message `redeploy …`. With no such node left the button is a plain Ship. Keep `only` + `refresh` in the Go API.
- Deployments tab pills (ACTIVE/COMPLETED/DEPLOYING/STOPPING/FAILED/STOPPED/PENDING/REMOVED) are derived client-side from deployment status + node status.
- `/p/<slug>?deployment=<id>` selects a deployment; `deployments.get` returns null for malformed/foreign ids.

### 7.7 Node actions → server calls

| UI action | Call |
| --- | --- |
| Add (web: always `deploy: true`) | `nodes.create` |
| Deploy (pending) / Start (stopped) | `nodes.start` |
| Stop | `nodes.stop` |
| Redeploy | `deployments.start({only:[id], refresh: true})` |
| Restart, Run again | `deployments.start({only:[id], refresh: false})` |
| Ship / Retry | `deployments.start({environmentId[, only, refresh: true]})` |
| `keel ship` / `keel redeploy` | `deployments.start` (CLI passes `refresh` explicitly, `only` only when non-empty) |

---

## 8. Docker / Swarm integration

### 8.1 Constants

| Name | Value |
| --- | --- |
| Docker socket | `/var/run/docker.sock` (Go: `DOCKER_HOST`, default `unix:///var/run/docker.sock`) |
| Overlay network | `keel` (created by install, attachable overlay, MTU 1200; not created here) |
| Service name | `svc-<nodeId>` (also the overlay DNS name, `HOST`) |
| Labels | `keel.service=<nodeId>`, `keel.revision=<revision>` on the service **and** the container spec |
| Legacy label | `keel.ingress` (Quick Tunnel services, removed by `removeLegacyTunnels`) |
| Settle re-scan | 2000 ms, at most 2 times |
| Observe debounce | 500 ms |

### 8.2 `toSpec(id, desired, env, oneShot)` (exact)

```json
{
  "Name": "svc-<id>",
  "Labels": {"keel.service": "<id>", "keel.revision": "<revision>"},
  "TaskTemplate": {
    "ContainerSpec": {
      "Image": "<desired.image>",
      "Env": ["K=V", ...],
      "Args": ["redis-server", "--requirepass", "<pass>"],   // only when engineOf(image)=="redis" and env has a non-empty REDIS_PASSWORD; else omitted
      "Labels": {"keel.service": "<id>", "keel.revision": "<revision>"}
    },
    "RestartPolicy": {"Condition": "on-failure", "Delay": 5000000000, "MaxAttempts": 5},
    "Networks": [{"Target": "keel"}]
  },
  "Mode": {"Replicated": {"Replicas": <desired.replicas>}},
  "UpdateConfig": {"Parallelism": 1, "Order": "start-first", "FailureAction": "<oneShot ? continue : rollback>"}
}
```

No `EndpointSpec` (zero published ports), no `Mounts`, no placement constraints, no `StopGracePeriod`, no registry auth. `Args` reads the password from the final env (`REDIS_PASSWORD=` prefix, first match).

`Env` = `computeEnv(node)` then, if `desired.tracing` and the environment exists and an OTLP key exists for it, the tracing variables not overridden by the service's own keys, in this order (logs spec owns the details):

```
OTEL_EXPORTER_OTLP_ENDPOINT=<KEEL_OTLP_URL || "<site URL>/otlp", trailing "/" stripped>
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<ingest key>
OTEL_SERVICE_NAME=<node.name>
OTEL_RESOURCE_ATTRIBUTES=keel.service_id=<enc id>,keel.environment_id=<enc envId>,deployment.environment.name=<enc env name>
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=none
OTEL_LOGS_EXPORTER=none
```

A key is skipped if the service sets it itself; `OTEL_EXPORTER_OTLP_HEADERS` is also skipped if the service sets `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`.

### 8.3 Docker API calls

| Use | Call | Notes |
| --- | --- | --- |
| cached? | `GET /images/{image}/json` | 404 → not cached |
| pull | `POST /images/create?fromImage=<repo>&tag=<tag>` and read the JSON progress stream to the end | dockerode split: `@` wins over the last `:`; the part after the separator is the tag unless it contains `/` (then the whole ref is the repo); no tag → `latest`. Only an HTTP error status (e.g. 404 for an unknown image) or a broken stream fails the pull: docker-modem's `followProgress` ignores in-stream `error`/`errorDetail` messages (the service is then created and its tasks get `rejected`). Recommended in Go: treat an in-stream `errorDetail` as a pull failure. |
| inspect service | `GET /services/svc-<id>` | 404 → create |
| create | `POST /services/create` body = spec | |
| update | `POST /services/svc-<id>/update?version=<Version.Index>` body = spec | `createOrUpdate` loops inspect → create/update up to 3 attempts (catches "update out of sequence" from concurrent applies), rethrows the 3rd error; returns `created = (inspect was 404)` |
| remove | `DELETE /services/svc-<id>` | 404 ignored |
| one node's tasks | `GET /tasks?filters={"label":["keel.service=<id>"]}` | with `GET /services/svc-<id>` (404 → null), in parallel |
| full sweep | `GET /services?filters={"label":["keel.service"]}`, `GET /tasks?filters={"label":["keel.service"]}` | |
| servers | `GET /nodes` | `servers = count(Status.State == "ready")` → `environments.setServers` |
| legacy tunnels | `GET /services?filters={"label":["keel.ingress"]}` then `DELETE` each | migrations only |

### 8.4 `summarize(tasks, service)` → `Observed` (exact)

```ts
update = service?.UpdateStatus?.State
rolledBack = update === "paused" || update?.startsWith("rollback")
revision = max(Number(service?.Spec?.Labels["keel.revision"] ?? 0), ...tasks.map(t => Number(t.Spec.ContainerSpec.Labels["keel.revision"] ?? 0)))
current   = tasks with label revision == revision
live      = current where DesiredState == "running"
running   = live where Status.State == "running"
failed    = current where Status.State in {"failed", "rejected"}
completed = current where Status.State == "complete"
oneShot   = completed.length > 0 && live.length == 0 && failed.length == 0
finishedAt = max(parse(Status.Timestamp) ms) over completed (0 if unparsable)
state = rolledBack ? "failed"
      : failed.length >= 5 ? "crashloop"
      : live.some(Status.State == "pending") ? "pending"
      : (update === "updating" || running.length < live.length) ? "updating"
      : oneShot ? "completed" : "ok"
error = last(failed).Status.Err ?? (rolledBack ? service.UpdateStatus.Message : undefined)
return {revision, running: running.length, ...(oneShot && {completed: completed.length, finishedAt}),
        nodeIds: distinct(running.NodeID non-empty), state, error, at: now}
```

"last" = last in Docker's list order. A missing service with no tasks gives `{revision: 0, running: 0, state: "ok", nodeIds: []}`.

> **Go now:** the Swarm adapter parses `keel.revision` once with `strconv.Atoi` (missing or non-numeric reads as 0) and hands typed tasks to `summarizeTasks`; there is no `nodeIds`.

`settling(tasks, revision)` = some task of `revision` with `DesiredState != "shutdown"` and `Status.State ∈ {new, allocated, assigned, accepted, preparing, ready, starting}` (`pending` deliberately excluded: that is the timeout's job).

### 8.5 Observe jobs

- `observeNode({id, settle=0})`: clear the node's pending-observe handle first (so events arriving during the scan schedule a fresh one); fetch service + tasks; `setObserved`; `reconcile.run`; if `settle < 2 && settling(tasks, observed.revision)` → `scheduleObserve(id, delay 2000, settle+1)`. Log line `observeNode <id> tasks=<n> update=<state|-> revision=<r> state=<s> running=<n> settle=<k>`.
- `observeSwarmNodes()`: servers count only.
- `observe()` (full sweep): `listDeployable` = every node install-wide with `desired.revision > 0`; one services + tasks listing; `setObserved` per node (tasks filtered by label = id, service by name); one `reconcile.run` if any; then servers count. Scheduled on agent resync and (Go) at startup.

> **Go now:** no per-scan log line: observe logs through slog only on failure (`observeNode`, `observe (full sweep)`, `observeServers`, with `node` and `err`). `observeNode` writes `setObserved` and `reconcile` in one transaction, and also re-scans every 2 s while Swarm reports `updating` or `rollback_started`, up to the 5-minute deploy timeout.

### 8.6 Debounce (`scheduleObserveFor(rawId, {delayMs = 500, settle?})`)

- Resolve id; ignore (return false) if not a node id, node missing, or node has no `desired`.
- If a scan for the node is pending and due at or before `now + delayMs` → coalesce (return false).
- If a pending scan is due later (a settle re-check) → cancel it, schedule the sooner one.
- Else schedule `observeNode({id, settle})` at `now + delayMs`; return true.

The ARCHITECTURE `Jobs.After(key, …)` ("pending job with the same key wins") must be extended or wrapped to also replace a pending job that is due **later** than the new one. Key: `observe:<nodeId>`. Cancel the pending scan when the node is deleted.

> **Go now:** wrapped, not extended: a map on `App` holds each node's pending scan (due time, settle count, generation) and every scan is its own job keyed `observe:<nodeId>:<gen>`; a job whose generation is no longer the node's current one does nothing. `ScheduleRemoveService` drops the node's entry.

### 8.7 Docker events ingestion (`POST /worker/events` → `events.ingest`)

HTTP (path unchanged): bearer `KEEL_WORKER_TOKEN`, constant-time compare; missing env or header → `401 "unauthorized"`. Body > 256 KiB → `413 "too large"`. Parse: trimmed body starting with `[` → JSON array; else NDJSON (non-empty lines, each JSON); a single object is accepted. Each item must be an object with `Type` and `Action` keys, else `400 "bad json"`. Trim each to `{type: String(Type), action: String(Action), name: Actor.Attributes.name (string), serviceName: Actor.Attributes["com.docker.swarm.service.name"] (string), time: number}`. Header `X-Keel-Resync: 1` → resync. Reply `200 "ok"`.

`ingest({events, resync})`: if resync → full `observe` job. Per batch `seen` set:
- `type == "node"`: schedule `observeSwarmNodes` once per batch.
- `type` not container/service: ignore.
- name = container → `serviceName`, service → `name`; must start with `svc-`; id = rest; once per id per batch → `scheduleObserveFor(id)`. Log `event <type> <action> <serviceName ?? name> → observeNode scheduled|skipped`.

> **Go now:** the body is a JSON array only (no single object, no NDJSON), at most 256 KiB in bytes; an element needs `Type` (not `Action`) and is trimmed to `{type, name, serviceName}`; a malformed body is `400 bad json`. There is no per-event log line.

---

## 9. Function reference

Columns: visibility (P = public, I = internal), auth, args, return, effects, errors. "Topics" = invalidation (§11).

### 9.1 `projects`

**`projects.ensureDefault`** (P mutation, no args) → `string` (project slug). Called by the web home route, which then navigates to `/p/<slug>`.

1. `joinOrFound()`:
   - `requireUser` (`Not authenticated`).
   - membership exists → use it.
   - else if any Better Auth `organization` row exists → throw NO_ORGANIZATION.
   - else create organization `{name: "Default", slug: "default", createdAt: now}` and member `{organizationId, userId, role: "owner", createdAt: now}` (the first account founds and owns the install's organization).
2. Legacy adoption (rows with `organizationId` unset; can be dropped if the Convex import already assigned them): `own` = projects of the org; `taken` = their slugs; legacy sorted with the caller's own (`ownerId == user`) first; each gets `slug = uniqueName(slug, taken)`, `organizationId`, `ownerId` cleared, and `name = slug` if the slug changed.
3. `existing = legacy.find(ownerId == user) ?? own[0] ?? legacy[0]` → return its current slug.
4. Else insert project `{name: "acme-support", slug: "acme-support", organizationId}` + environment `{name: "production", isProduction: true}`; return `"acme-support"`.

Topics: org projects (and org membership if founded).

**`projects.create`** (P mutation) args `{name: string}` → `{id, name, slug, environments: [{id, name: "production", isProduction: true}]}`.

1. `joinOrFound()` (founds the org on a fresh install, so `keel project create` works before the dashboard was opened).
2. `name = trim(name)`; `len == 0 || len > 60` (UTF-16) → `Project name: 1–60 characters` (`INVALID_INPUT`).
3. `slug = slugOf(name)`: NFKD normalize → strip U+0300–U+036F → lowercase → replace runs of `[^a-z0-9]+` with `-` → first 40 chars → trim leading/trailing `-`. (Go: `golang.org/x/text/unicode/norm`; JS `toLowerCase` uses full case mapping, Go `strings.ToLower` simple mapping, e.g. U+0130; negligible.) Empty → `Project name needs a letter or digit (a-z, 0-9)` (`INVALID_INPUT`).
4. Slug taken in the org → `Project "<slug>" already exists` (`NAME_TAKEN`; CLI parses the slug out of it).
5. Insert project `{name, slug, organizationId}` + production environment. Topics: org projects.

> **Go now:** no `joinOrFound` and no legacy adoption: the organization is founded at the first sign-up, and every project use case requires a membership (`RequireMember`). The name goes through `strings.TrimSpace` and its 60-character limit counts runes; the slug strips combining marks with the `x/text` `runes.Remove(runes.In(unicode.Mn))` recipe after NFKD; a taken slug is refused by the unique `(organization_id, slug)` insert. `projects.list` has no "no organization yet" case.

**`projects.getBySlug`** (P query) args `{slug}` → `{id, name, slug, environment: {id, name}}` or `null` (signed out, no membership, unknown slug, or no environment). Environment = the production one, else the first. Subscribed by the project route.

**`projects.list`** (P query, no args) → `[{id, name, slug, environments: [{id, name, isProduction}]}]`, projects in creation order, environments sorted production first (stable). No membership: signed out → `Not authenticated`; an organization exists → NO_ORGANIZATION; no organization at all → `[]`. Subscribed by the project switcher; used by the CLI.

### 9.2 `environments`

**`environments.summary`** (P query) args `{environmentId}` → `{pendingChanges: number, counts: {<NodeStatus>: number}, servers: number}` or `null` if not owned.
- `pendingChanges` = count of `dirty && DEPLOYABLE` nodes.
- `counts` = per `deriveStatus` over every node except groups (volumes count as `pending`); keys with zero are absent.
- `servers` = `cluster.servers ?? 0`.
Subscribed by Ship button and status bar; CLI `keel status`.

**`environments.setServers`** (I mutation) `{servers}`: upsert the single `cluster` row `{servers, at: now}`. Topics: every summary (install-wide).

### 9.3 `nodes`

**`nodes.list`** (P query) `{environmentId}` → `NodeView[]` (§4.3) in creation order; `[]` if not owned. The canvas, topbar, observability chrome and CLI (`keel service list`, which drops groups and maps `dirty` → `staged`) use it.

**`nodes.create`** (P mutation) args:

| arg | type | notes |
| --- | --- | --- |
| `environmentId` | id | |
| `type` | node type | |
| `name` | string? | empty string = omitted |
| `position` | `{x,y}`? | omitted → `nextPosition` |
| `image` | string? | service only |
| `engine` | `"postgres"\|"mysql"\|"mongo"\|"redis"`? | database/cache only |
| `port` | number? | deployable only |
| `replicas` | number? | deployable only |
| `deploy` | bool? | ship right away |

Returns `{id, deploymentId?}`. Order of checks (first failure wins):

1. `requireEnvironment` → `Environment not found`.
2. `image` given and `type != service` → `Only services take a custom image`.
3. `port` or `replicas` given and type not deployable → `This node type has no runtime settings`.
4. `engine` given and `ENGINES[engine].type != type` → `<engine> is not a <type>` (e.g. `redis is not a database`, `postgres is not a service`).
5. `finalImage = ENGINES[engine].image` if engine, else `validImage(image)` if given (→ `Image must look like repo/name:tag`).
6. `name` (non-empty) already used in the environment → `"<name>" is already taken` (with the double quotes; `NAME_TAKEN`). Checked **before** `validName`.
7. `finalName = name ? validName(name) : uniqueName(baseName, taken)` (§3.4).
8. `desired` (deployable only) = `{image: finalImage ?? default image, revision: 0, replicas: validReplicas(replicas) ?? 1, port: validPort(port) ?? engine port ?? default port}` (replicas validated before port).
9. Insert node `{environmentId, type, name, position, config (§3.1), desired, dirty: DEPLOYABLE(type)}`.
10. database/cache: insert seeded variables (§3.3) for `engineOf(desired.image)`.
11. `deploy && deployable`: `beginDeployment(environmentId, {only: [id]})` (message `deploy <name>`); a domain error from it (a deployment already running) is swallowed: the node stays dirty and `deploymentId` is omitted.

Topics: environment.

**`nodes.move`** (P mutation) `{id, position}` → null. `requireNode`; patch position. The web applies it optimistically on drag end. Topics: environment.

**`nodes.rename`** (P mutation) `{id, name}` → null. `requireNode`; same name → no-op (before validation); `validName`; taken in environment → `"<name>" is already taken`; `renameReferences` (§5.5); patch name. Not dirty. Endpoints keep their domain. Topics: environment (+ every node's variables views).

**`nodes.setDesired`** (P mutation) `{id, image?, port?, replicas?}` → null. Not called by web or CLI today; keep it. `requireNode`; no desired → `This node type has no runtime settings`; merged desired: `image` → `validImage`, `port` → `validPort` (cannot unset a port), `replicas` → `validReplicas` (`?? old`); keeps `revision`/`tracing`; `dirty: true`; `markReferrersDirty`. Topics: environment.

**`nodes.stop`** (P mutation) `{id}` → deployment id, or `null` if already at 0 replicas. `requireNode`; no desired → `This node type cannot be stopped`; replicas 0 → return null; patch `desired.replicas = 0, dirty: true`; `beginDeployment(env, {only: [id], verb: "stop"})` (its errors roll back the patch). Topics: environment.

**`nodes.start`** (P mutation) `{id}` → deployment id. `requireNode`; no desired → `This node type cannot be started`; `replicas = old == 0 ? 1 : old` (a stopped 3-replica service comes back with 1); patch, `dirty: true`; `beginDeployment(env, {only: [id], verb: revision == 0 ? "deploy" : "start"})`. Always ships, even if already running. Topics: environment.

**`nodes.expose`** (P mutation) `{id, protocol?: "http"|"tcp"|"udp", port?, domain?, publicPort?}` → `EndpointView`. Immediate (not Ship-gated). Checks in order:

1. `requireNode`.
2. no desired or type not service/database/cache → `Only services, databases and caches can be exposed`.
3. `engineOf(image) == "redis"` and (no `REDIS_PASSWORD` row || `dirty` || `deployedRevision != desired.revision`) → `Ship this Redis first: its password takes effect on the next Ship`.
4. `protocol = arg ?? (service ? "http" : "tcp")`.
5. `port = validPort(arg port ?? desired.port)`; none → `Set the service's port first`.
6. `KEEL_PUBLIC_IP` unset → `Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP` (`UNAVAILABLE` or `INVALID_INPUT`).
7. `own` = node's endpoints; `others` = every other node's endpoints **install-wide** with owner name.
8. http: `publicPort` given → `HTTP is always served on 80 and 443`; `domain = arg ? validDomain(arg) : defaultDomain(node, ip)`; another node holds that http domain → `<domain> is already used by <ownerName>` (`CONFLICT`); wanted `{http, port, domain}`.
   tcp/udp: `domain` given → `Only HTTP endpoints have a domain`; if no `publicPort` given and `own` has the same protocol + same container port → return it (no write); `taken` = public ports of that protocol in others ∪ own, plus 80/443 for tcp; `publicPort = arg ? validPort(arg) : allocatePublicPort(port, taken)` (container port if free, else first free from 20000 up to 65535; none → `No free public port left`); tcp on 80/443 → `80 and 443 serve HTTP; pick another public port`; another node holds it → `Port <publicPort>/<protocol> is already used by <ownerName>` (`CONFLICT`); wanted `{protocol, port, publicPort}`.
9. If `own` has an endpoint with the same key and the same container port → return it (no write). Else `rest = own minus same key`; `rest.length >= 10` → `At most 10 endpoints per node`.
10. New endpoint `{...wanted, pinnedPort: true only if port != desired.port, status: {state: "starting", at: now}}` replaces any same-key one (that is how "same domain/public port, new container port" works); patch; job `proxy.sync`. Return its view.

`defaultDomain(node, ip) = "<name>-<shortHash(id)>.<ip with . → ->.sslip.io"`; `shortHash` = 32-bit FNV-1a over the id's UTF-16 code units (`h = 0x811c9dc5; h = imul(h ^ c, 0x01000193)`), unsigned, base-36, left-padded with `0` to 6, last 6 chars. Topics: environment.

> **Go now:** `shortHash` is `hash/fnv` (`New32a`) over the id's bytes, the same value for ASCII ids. The default protocol comes from a table by node type, `pickPublicPort` owns the tcp/udp choice, and an endpoint without a stored public port renders as port 0 instead of failing the list.

**`nodes.unexpose`** (P mutation) `{id, protocol?, domain?, publicPort?}` → null. `requireNode`; no selector = close all; a partial selector must name the endpoint (`protocol=http` + `domain`, or tcp/udp + `publicPort`) else `Name the endpoint: protocol and domain (http) or public port`; no endpoints → no-op; filter by key (`validDomain` applied to the domain); nothing removed → no-op; patch (`endpoints` unset when empty); job `proxy.sync`. Topics: environment.

> **Go now:** the domain and the public port are validated before anything else, so a malformed one is `INVALID_INPUT` even when the node has no endpoints.

**`nodes.publicAddress`** (P query, no args) → `KEEL_PUBLIC_IP` or `null`; `requireUser` (`Not authenticated`). Static per process.

**`nodes.duplicate`** (P mutation) `{id}` → new node id (string). `requireNode`; group → `Groups cannot be duplicated`. Copy: `environmentId, type, parentId, config, oneShot`, `desired` with `revision: 0` (keeps image/port/replicas/tracing), `name` per §3.4, `position + 40`, `dirty: DEPLOYABLE(type)`. Not copied: `observed, deployedRevision, applyError, shippedAt, endpoints`, legacy fields, debounce handle. Variables copied verbatim in order (a duplicated database **shares the same generated passwords**; references to other nodes still point at them; self references stay relative). No referrer dirtying. Topics: environment.

**`nodes.remove`** (P mutation) `{id}` → null. See §10. Idempotent: a missing / foreign node returns without error (multi-select delete races itself). The web removes it optimistically.

### 9.4 `variables`

**`variables.list`** (P query) `{nodeId}` → `VariableView[]` in row order; `[]` if not owned.

```ts
VariableView = {
  key: string,
  value: string,          // as typed
  resolved: string,       // fully expanded (what the container gets)
  secret: boolean,        // the row's flag
  resolvedSecret: boolean,// any referenced value (or the row) is secret
  parts: ({text: string} | {ref: {node?: string, nodeId?: string, key: string, missing: boolean}})[]
}
```

Note: `resolvedSecret` from `expand` covers referenced secrets only; the row's own `secret` is reported separately (the UI masks `resolved` if either is set).

**`variables.referenceable`** (P query) `{nodeId}` → `[{nodeId, name, type, image?, keys: [{key, secret, provided}]}]`; `[]` if not owned. Every other node of the environment that is deployable (excludes self, groups, volumes), in creation order. `keys` = provided keys (§5.2 order) **not** shadowed by an own row, computed with every credential at its fallback (credentials are never read, so `REDIS_URL` shows `secret: false`), marked `provided: true`; then own rows `{key, secret: row.secret, provided: false}`.

**`variables.set`** (P mutation) `{nodeId, key, value, secret, previousKey?}` → null. Upsert by key; `previousKey` renames that row.

1. `requireNode` (any node type).
2. `validEnvKey(key)`.
3. `value` length > 4096 UTF-16 units → `Value too long`.
4. `previousKey` defaults to `key`. If `previousKey != key` and a row with `key` exists → `<key> already exists` (`NAME_TAKEN` recommended; CLI today maps it to `INVALID_INPUT`).
5. Row with `previousKey` exists → patch `{key, value, secret}`; else insert `{nodeId, key, value, secret}` (an unknown `previousKey` silently inserts).
6. If a row was renamed → rewrite references to the node (§5.5).
7. Node `dirty: true`; `markReferrersDirty(node)`.

> **Go now:** `previousKey` is a plain string (`""` = no rename); the 4096 limit counts runes; step 4 has no read-check: the `UNIQUE (node_id, key)` index refuses the clash and the use case maps it to `<key> already exists` with `NAME_TAKEN`.

Topics: environment (+ variables views of every node in it).

**`variables.remove`** (P mutation) `{nodeId, key}` → null. `requireNode`; no such row → no-op (no dirty); delete; node `dirty: true`; `markReferrersDirty`. References to the removed key now resolve to `""` and show as missing.

### 9.5 `deployments`

Returned document shape (raw Convex doc; the CLI reads `_id` and the nested arrays):

```json
{
  "_id": "…", "_creationTime": 1730000000000.5,
  "environmentId": "…", "sha": "…?", "message": "ship api, postgres",
  "status": "running|success|failed", "startedAt": 0, "finishedAt": 0,
  "steps": [{"nodeId": "…?", "label": "api", "status": "pending|running|done|failed",
             "startedAt": 0, "appliedAt": 0, "finishedAt": 0}],
  "log": [{"at": 0, "nodeId": "…?", "text": "using cached nginx:alpine"}]
}
```

(Optional fields absent when unset.) The Go API may rename `_id` → `id` only together with the CLI client; keep both if in doubt.

| Function | Vis | Args | Returns | Rules |
| --- | --- | --- | --- | --- |
| `deployments.start` | P mutation | `{environmentId, only?: id[], refresh?: bool}` | deployment id | `requireEnvironment`; `beginDeployment(env, {only, refresh})` (§7.1) |
| `deployments.latest` | P query | `{environmentId}` | doc or `null` | null if not owned or none; newest by creation |
| `deployments.get` | P query | `{id: string}` | doc or `null` | any string accepted; malformed/missing/foreign → null |
| `deployments.listForNode` | P query | `{nodeId}` | doc[] | `[]` if not owned; newest 50 deployments of the node's environment, keep those with a step for the node, first 20 |
| `deployments.stepRunning/stepLog/stepApplied/stepFailed` | I mutation | `{deploymentId, nodeId, text}` | — | §7.2 |

### 9.6 `reconcile`

`reconcile.run` (I mutation, no args) §7.4. `reconcile.timeoutDeployment` (I mutation `{deploymentId}`) §7.5.

### 9.7 `nodesInternal`

| Function | Kind | Args | Effect / return |
| --- | --- | --- | --- |
| `owned` | I query | `{id}` | `ownedNode(id) != null` (used by `logs.*`) |
| `listDeployable` | I query | — | `[{id, environmentId}]` for every node install-wide with `desired.revision > 0` |
| `applyInput` | I query | `{id}` | `{name, desired, env, oneShot}` or null (§7.2) |
| `setObserved` | I mutation | `{id, observed}` | §7.3 |
| `scheduleObserve` | I mutation | `{id, delayMs?, settle?}` | §8.6, returns bool |
| `clearObserveScheduled` | I mutation | `{id}` | clears the debounce handle if the node exists |
| `setApplyError` | I mutation | `{id, error?}` | sets/clears `applyError` if the node exists |
| `followPort` | I mutation | `{id, port}` | §7.2 step 9 |

### 9.8 `events`, `swarm`

`events.ingest` (I mutation) §8.7. `swarm.apply`, `swarm.remove({id})` (delete `svc-<id>`, 404 ignored), `swarm.observeNode`, `swarm.observeSwarmNodes`, `swarm.observe`, `swarm.removeLegacyTunnels` (I actions) §7–8.

### 9.9 `migrations.run` (I mutation, run on every install/upgrade)

Idempotent steps, then `removeLegacyTunnels` + `proxy.sync` jobs; returns `{quickTunnelsConverted, redisPasswords, domainsMoved}`:

1. Legacy `public`/`ingress`: if `public` truthy, type service, has port, `KEEL_PUBLIC_IP` set and no endpoints → add one http endpoint on `defaultDomain` with status starting; always clear both fields.
2. Every cache with `engineOf(image) == "redis"` lacking a `REDIS_PASSWORD` row: insert one (random, secret), `dirty: true`, `markReferrersDirty`.
3. If `KEEL_PUBLIC_IP` set: every http endpoint whose domain matches `^(.+-([0-9a-z]{6}))\.(\d+-\d+-\d+-\d+)\.sslip\.io$` with group 2 == `shortHash(node id)` and group 3 != the current dashed IP → same name on the current IP, status starting.

In Go: step 1 belongs in the Convex importer only; steps 2–3 stay in the startup data-migration pass.

> **Go now:** no `migrations.run`, importer or data-migration pass. A new Redis cache gets its `REDIS_PASSWORD` when it is created; only step 3 survives, as `moveDefaultDomains` in the recovery pass (`recoverIngress`).

### 9.10 Related functions in other areas that write node state

- `tracing.enable` (P action) → `tracing.setEnabled` (I mutation `{nodeId, on}`): `requireNode`; no-op if no desired or unchanged; sets `desired.tracing = true` or deletes the key; `dirty: true` (no referrer dirtying). Errors `Only services can be traced`, `Connect Axiom to see traces`, `Sign in with Axiom again to turn on traces` (logs spec).
- `proxyInternal.setStatuses`, `proxyInternal.certReport` write `endpoints[].status` (networking spec). Topic: environment of each touched node.

---

## 10. Deletes and cascades

`nodes.remove({id})`, in one transaction:

1. `ownedNode` null → return (no error).
2. `markReferrersDirty(node)` (computed **before** deleting the node's own rows; referrers' references now resolve to `""` and render red/missing).
3. Delete the node's variables.
4. Children (`parentId == id`, same environment): `parentId` cleared, `position = node.position + child.position`.
5. Cancel the node's pending observe.
6. Delete the node row **before** scheduling Swarm removal (so an in-flight `apply` that re-reads `applyInput` sees null and backs out, and removes a service it just created).
7. After commit: if it had endpoints → `proxy.sync`; if it had `desired` → `swarm.remove({id})` and `reconcile.run` (fails its `pending`/`running` steps in a running deployment with `<label>: node deleted` instead of waiting 5 minutes).

Not cascaded: deployment rows and their steps (keep dangling `nodeId`; the web's Retry ignores dead ids), variables in other nodes that reference it (stay, unresolved), `otlpKeys`.

**Not implemented anywhere**: deleting or renaming projects, creating/deleting environments, deleting deployments. The Go port must not invent them silently; if added, cascade order would be: nodes (as above, each) → variables → deployments → otlpKeys → environment → project.

---

## 11. Realtime: subscriptions and invalidation

Web subscriptions in this area (Convex `useQuery`, reactive): `projects.list`, `projects.getBySlug`, `nodes.list`, `environments.summary`, `deployments.latest`, `deployments.get`, `deployments.listForNode`, `variables.list`, `variables.referenceable`, `tracing.forNode`, `nodes.publicAddress` (static), `organizations.current`.

Read dependencies (what must trigger a refetch):

| Query | Depends on |
| --- | --- |
| `projects.list`, `projects.getBySlug` | projects + environments of the org; membership |
| `nodes.list` | every node row of the environment (`desired`, `observed`, `dirty`, `applyError`, `shippedAt`, `endpoints`, …) |
| `environments.summary` | node rows of the environment + the install-wide `cluster` row |
| `deployments.latest`, `deployments.get`, `deployments.listForNode` | deployment rows of the environment |
| `variables.list(nodeId)` | variables of **every** node in the environment, node names, `desired` (image/port) of referenced nodes |
| `variables.referenceable(nodeId)` | nodes of the environment + their variables |
| `tracing.forNode` | the node, its variables, the env's OTLP key, the org's sink |

Recommended topics (ARCHITECTURE `Changes` helpers): `ch.Projects(org)`, `ch.Environment(org, envID)` (canvas, summary, deployments, and every node sub-resource of that environment: route node variables/referenceable under the environment path or publish `ch.Node` for each node of the environment), `ch.Node(org, nodeID)`, and a cluster change publishing every environment topic (or a `/api/environments` prefix to all orgs).

| Write | Topics |
| --- | --- |
| `projects.create`, `projects.ensureDefault` | `Projects(org)` (+ org membership when founded) |
| `nodes.create/move/rename/setDesired/stop/start/expose/unexpose/duplicate/remove` | `Environment(env)` |
| `variables.set/remove` | `Environment(env)` (values of referrers' views change too) |
| `deployments.start` and every `beginDeployment` | `Environment(env)` |
| `deployments.step*`, `reconcile.run`, `reconcile.timeoutDeployment` | `Environment(env)` (only when something changed) |
| `setObserved`, `setApplyError`, `followPort` | `Environment(env)` (`setObserved`: only if the derived view changed; observe runs on every Docker event) |
| `clearObserveScheduled`, debounce bookkeeping | none (not in any view) |
| `environments.setServers` | every environment (summary) |
| `tracing.setEnabled` | `Environment(env)` |
| proxy status / cert reports, `migrations.run` | `Environment(env)` of each touched node |

The web uses optimistic updates for `nodes.move` (position) and `nodes.remove` (drop from list), and keeps a dragged node's local position while dragging. With TanStack Query the same can be done with `setQueryData` + rollback on error.

---

## 12. Limits and constants

| Limit | Value | Where |
| --- | --- | --- |
| Project name | 1–60 UTF-16 units after trim | `projects.create` |
| Project slug | ≤40 chars `[a-z0-9-]` | `slugOf` |
| Node name | `^[a-z0-9-]{1,40}$` (explicit names only) | `validName` |
| Variable key | `^[A-Z_][A-Z0-9_]{0,63}$` | `validEnvKey` |
| Variable value | ≤4096 UTF-16 units | `variables.set` |
| Image | `^[a-z0-9][a-z0-9._\-/:@]{0,199}$` | `validImage` |
| Port | integer 1–65535 | `validPort` |
| Replicas | integer 0–20 | `validReplicas` |
| Endpoints per node | 10 | `MAX_ENDPOINTS` |
| Spare public ports | from 20000 | `allocatePublicPort` |
| Reference depth | 5 | `MAX_DEPTH` |
| Deployment log | last 500 entries | `MAX_LOG` |
| `listForNode` | scan 50, return ≤20 | |
| Deploy timeout | 5 min | `DEPLOY_TIMEOUT_MS` |
| Observe debounce / settle | 500 ms / 2 s × 2 | |
| Swarm restart | on-failure, 5 s delay, 5 attempts; `crashloop` at ≥5 failed tasks of the revision | `toSpec`, `summarize` |
| `createOrUpdate` attempts | 3 | |
| `errorText` | 300 chars | |
| Worker events body | 256 KiB | `/worker/events` |
| Generated password | 20 chars, 56-char alphabet | `randomSecret` |
| Concurrency | one `running` deployment per environment | `beginDeployment` |

> **Go now:** the project name and variable value limits count runes, the error text is cut at 300 runes (`CompactText`), and the worker events body limit counts bytes.

## 13. Environment variables read in this area

| Var | Meaning | Default |
| --- | --- | --- |
| `KEEL_PUBLIC_IP` | Control plane public IPv4: default sslip domains, tcp/udp addresses, `publicAddress`; required by `expose` | unset (expose refuses) |
| `KEEL_WORKER_TOKEN` | Bearer for `/worker/events` (and `/worker/config`, `/proxy/events`) | unset → every request 401 |
| `KEEL_OTLP_URL` | OTLP endpoint injected into traced services | `<site URL>/otlp` (was `CONVEX_SITE_URL`; in Go the URL agents/services reach `serve` at) |
| `DOCKER_HOST` | Docker socket | `unix:///var/run/docker.sock` |

## 14. Quirks to preserve (or change deliberately, with a test)

1. `dirty` is sticky; no diff. Every `setDesired` marks dirty even with identical values.
2. `start` after `stop` returns to 1 replica, not the previous count.
3. Volumes count as `pending` in `summary.counts`.
4. `uniqueName` can exceed 40 chars.
5. A failed step fails the deployment while sibling applies continue; their steps can stay `running` on a failed deployment.
6. Concurrent applies for one node can apply an older revision last (§7.2).
7. `rename` does not dirty; `OTEL_SERVICE_NAME` lags until the next ship.
8. `duplicate` copies credentials verbatim (two databases with the same password) and `oneShot`.
9. `referenceable` reports `REDIS_URL` as non-secret.
10. Service with a redis image is treated as Redis for `--requirepass` and the expose guard.
11. `nodes.create{deploy}` silently skips the ship if another deployment is running.
12. Database data does not persist across tasks (no mounts).
13. `expose` uniqueness checks scan every node install-wide (domains and public ports are global).
