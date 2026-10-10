# Workers — Docker Swarm design

> How Keel runs user containers across one or many servers. Decided 2026-09-13 after evaluating per-node agents, Restate/Temporal, Nomad and k3s. Read this before touching `internal/app/{swarm,observe,reconcile,events}.go`, `internal/adapters/swarm`, `internal/agent` or the node join flow. UI lives in [`canvas.md`](./canvas.md).

> **Go port.** Convex, the Bun worker and `scripts/deploy-worker.sh` are replaced: the control plane is `keel serve` and the per-node process is `keel agent`, one Go binary ([`docs/go/ARCHITECTURE.md`](./go/ARCHITECTURE.md)). Convex actions, mutations, crons and `scheduler.runAfter` became use cases in `internal/app` and in-memory jobs (`internal/adapters/jobs`), which the start-up `Recover` pass re-derives on every start. Decisions and rationale below are unchanged; the dated Status entries at the end are history and keep the names of their time.

## Decision

Docker Swarm is the worker layer. `keel serve` is the control plane and its SQLite database the only place desired state lives. Swarm is the reconciler: it keeps containers running, restarts on death, reschedules when a node dies, does rolling updates and rollback.

We do **not** ship a per-node reconciler agent. Joining a server is a script that runs `docker swarm join`. The Swarm manager runs on the control-plane box next to `keel serve`, so its jobs reach every worker through one Docker socket.

One thing does run on every node: `keel-agent`, a Swarm **global service** running `keel agent` (`internal/agent`, the control plane's own image; was `keel-worker` from `apps/worker`, Bun). `keel serve` creates and updates it on every start when `KEEL_AGENT_IMAGE` is set (`ensureAgent` in `internal/app/reconcile.go`, `EnsureAgent` in `internal/adapters/swarm/agent.go`). It is not a reconciler: the socket is mounted read-only, every Docker call it makes is a GET, nothing listens, and it only talks outbound to `keel serve`'s worker routes under one bearer token (`KEEL_WORKER_TOKEN`). Swarm schedules it onto new nodes by itself, so the join flow is still plain `docker swarm join`. Two jobs today:

- **Events** (`internal/agent/events.go`): streams that node's `docker events` to `POST /worker/events`, one JSON array per event (see [`observe`](#observe--swarm-to-observed)). `scope=local` events (container start/die) are only visible on the daemon that runs the container, and observation must be event-driven, not polled.
- **Logs** (`internal/agent/logs.go`): follows every `svc-*` container's stdout/stderr and ships it to its organization's log sink, when one is configured. Config comes from `GET /worker/config`, polled every 30s. See [`logs.md`](./logs.md).

Anything that needs to run on the node itself (metrics, exec into a container, volume rsync in [`volumes.md`](./volumes.md)) goes into this agent rather than a new service. It replaced the 60-line `docker:cli` shell forwarder (`infra/events-sidecar`, removed 2026-09-20) because a shell script could not follow logs, batch, or resume per container.

Let Swarm pick nodes. No custom scheduler, no resource reservations for v1. `Placement.Constraints` exists for pinning if a user drags a service onto a specific node on the canvas, and that is the extent of it.

## Why not the alternatives

| Option                          | Why not                                                                                                                                                        |
| ------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agent per node + own reconciler | Works, but we own restart, crash-loop, drift detection, per-node event streams. Swarm already does all of it.                                                  |
| Restate / Temporal / Hatchet    | Orchestrate multi-step work. Converge is one idempotent step. Restate also pushes to workers, so every node exposes a port. Second state store next to SQLite. |
| Nomad                           | Real scheduler, but one more server on the control plane and BSL license.                                                                                      |
| k3s / Kubernetes                | 1GB RAM idle, networking fights Tailscale, kills the cheap-VPS story.                                                                                          |
| Compose per project (Coolify)   | Single node only, still needs an agent to run it on each box.                                                                                                  |

Swarm caveats accepted: overlay MTU on WireGuard needs one config line, Swarm is in maintenance mode at Docker Inc. Stable, not evolving. Fine for our scope.

## How keel serve reaches Docker

`keel serve` runs in the `keel` container with the manager socket mounted, and drives Swarm with the moby client (`github.com/moby/moby/client`) in `internal/adapters/swarm`. Only that package and `internal/agent` import the Docker SDK. Before the Go port this was `dockerode` in Convex `"use node"` actions inside the `convex-backend` container (verified on a clean host 2026-09-29).

```yaml
# deploy/compose.yml (installed to /opt/keel/compose.yml by install.sh)
keel:
  image: ghcr.io/thallesp/keel:<version>
  command: ["serve"]
  volumes:
    - keel-data:/data # keel.db (SQLite, WAL)
    - /var/run/docker.sock:/var/run/docker.sock
```

Facts:

- The image runs as root on purpose (see the repo-root `Dockerfile`): the socket's group id differs per host, so socket permissions are fine.
- The image ships no `docker` CLI. Use the moby client over `DOCKER_HOST` (default `unix:///var/run/docker.sock`), never shell out to `docker`.
- Nothing is installed at run time: the client is compiled in (Convex needed `dockerode` and `ssh2` as external packages, fetched from npm on the first push).
- Instead of Convex's 10-minute, 16-concurrent node actions, every Docker call carries its own deadline (an apply 15 min because pulls are slow, in `apply` in `internal/app/swarm.go`; scans and removals 1 min, `dockerCallDeadline` in `internal/app/deploy_state.go`) and the applies of one node run one at a time. Never block a job on a build.
- SQLite is a single writer (one write connection on `$KEEL_DATA_DIR/keel.db`), so an install runs one `keel serve`; pending jobs live in memory, die with the process and are re-derived by the start-up `Recover` pass. Control plane is one box. Running containers survive a control-plane outage, new deploys do not.

Security: mounting the Docker socket gives `keel serve` root-equivalent access to the control-plane host. Only use cases and jobs call the Swarm port (`app.Swarm`, `internal/app/ports_deploy.go`); HTTP handlers never touch adapters. Never expose a route that takes a raw image, command, or mount. `keel proxy`, which parses internet traffic, never gets the socket. The `keel` container is trusted infrastructure at the same tier as the host.

## Cluster bootstrap

Runs once on the control plane during install.

Script: `scripts/bootstrap-swarm.sh`. Idempotent. `install.sh` fetches and runs it before the control plane starts (keel-proxy joins the `keel` overlay); in dev you run it by hand. It no longer deploys the per-node agent: `keel serve` does.

```bash
# advertise and listen on the tailnet IP only, so workers reach the manager over
# WireGuard and no Swarm port is exposed on the public interface
docker swarm init \
  --advertise-addr "$TAILSCALE_IP" \
  --listen-addr "$TAILSCALE_IP:2377" \
  --data-path-addr "$TAILSCALE_IP" \
  --default-addr-pool 10.200.0.0/16 \
  --default-addr-pool-mask-length 24

# overlay for user containers. MTU below WireGuard's 1280 so packets don't fragment.
docker network create -d overlay --attachable \
  --opt com.docker.network.driver.mtu=1200 \
  keel
```

`--default-addr-pool` avoids the default 10.0.0.0/8, which collides with corporate VPNs and existing bridge networks. Tailscale's 100.64.0.0/10 does not overlap.

Ports between nodes, all on the tailnet interface only: 2377/tcp (manager), 7946/tcp+udp (gossip), 4789/udp (VXLAN).

## Node join flow

1. User clicks "Add server" on the canvas. A use case inserts a `nodes` row with `status: "pending"` and a single-use join secret.
2. `keel serve` serves `GET /join/<secret>` as a shell script (a raw route next to `/worker/*`). Script is generated per node so the canvas can name it before it exists.
3. Script, on the new server:
   - Install Docker if missing.
   - Install and bring up Tailscale if missing (auth key passed in, or interactive).
   - Pre-flight dump: running containers, bound ports, existing network subnets. Print and confirm.
   - `docker swarm join --token <worker-token> <manager-tailnet-ip>:2377`, advertising the node's own tailnet IP.
   - POST back to `keel serve` with the Swarm `NodeID` and the pre-flight dump.
4. The use case flips the row to `status: "ready"`, stores `swarmNodeId` and `unmanagedContainers`. Canvas shows the node, with a warning badge if unmanaged containers exist.

Joining does not break what is already running on the server. Swarm mode is additive. Existing containers, compose projects, volumes and bridge networks are untouched. They are just invisible to the scheduler. Real conflicts are host ports already bound and subnet overlap, both caught by the pre-flight.

Later, optional: "adopt" an unmanaged container. Build a `ServiceSpec` from its `inspect`, create the service, stop the old container once the task is healthy. Nobody else does this. Good for the migrate-off-bare-VPS user.

## Schema

```ts
nodes: defineTable({
  name: v.string(),
  status: v.union(v.literal("pending"), v.literal("ready"), v.literal("down")),
  swarmNodeId: v.optional(v.string()),
  tailscaleIp: v.optional(v.string()),
  joinSecret: v.optional(v.string()),
  unmanagedContainers: v.optional(v.array(v.string())),
  lastSeen: v.optional(v.number()),
}).index("by_swarm_id", ["swarmNodeId"]),

services: defineTable({
  projectId: v.id("projects"),
  name: v.string(),
  // desired: what the user asked for. Only mutations from user actions write this.
  desired: v.object({
    image: v.string(),
    revision: v.number(),
    env: v.array(v.string()),
    replicas: v.number(),
    port: v.optional(v.number()),
    pinNodeId: v.optional(v.id("nodes")),
  }),
  // observed: what Swarm reports. Only the observe action writes this.
  observed: v.optional(v.object({
    revision: v.number(),
    running: v.number(),
    state: v.union(v.literal("ok"), v.literal("updating"), v.literal("crashloop"), v.literal("pending")),
    nodeIds: v.array(v.string()),
    error: v.optional(v.string()),
    at: v.number(),
  })),
}).index("by_project", ["projectId"]),
```

That is the design, in the Convex validators it was written in. What exists: `services` folded into the canvas `nodes` table on 2026-09-14, and in Go `desired` and `observed` are its `desired_*` / `observed_*` columns (with `deployed_revision`, `apply_error`, `one_shot`) in `internal/adapters/sqlite/migrations/0001_init.sql`, typed `domain.Desired` / `domain.Observed` in `internal/domain/node.go`. The servers table above is not built.

Keep `desired` and `observed` split. A single `status` field cannot tell "user wants it stopped" from "it is stopped".

## Control plane side

Three jobs plus one HTTP route. That is the whole worker layer. Nothing runs on a timer.

### `apply` — desired to Swarm

Idempotent. Create if missing, update if present. `version` is Swarm's optimistic-concurrency token, update fails if the service changed underneath (`createOrUpdate` re-reads it and tries again, 3 attempts).

```go
// internal/adapters/swarm/service.go (trimmed: engine args, one-shot FailureAction)
func toSpec(s app.ServiceSpec) swarm.ServiceSpec {
	labels := map[string]string{labelService: s.NodeID, "keel.revision": strconv.Itoa(s.Revision)}
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: serviceName(s.NodeID), Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: s.Image, Env: s.Env, Labels: labels},
			RestartPolicy: &swarm.RestartPolicy{
				Condition:   swarm.RestartPolicyConditionOnFailure,
				Delay:       new(5 * time.Second),
				MaxAttempts: new(uint64(5)),
			},
			Networks: []swarm.NetworkAttachmentConfig{{Target: "keel"}},
			// Pin to node, not built yet: Placement: &swarm.Placement{Constraints: []string{"node.id==" + pin}}
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: new(uint64(s.Replicas))}},
		// start-first is for stateless only. Services with volumes must use stop-first,
		// otherwise two tasks share one volume during rollout. See volumes.md rules 5 and 6.
		UpdateConfig: &swarm.UpdateConfig{Parallelism: 1, Order: swarm.UpdateOrderStartFirst, FailureAction: swarm.UpdateFailureActionRollback},
		// No EndpointSpec: nothing is published on the host. Public traffic comes in through
		// keel-proxy (networking.md); service-to-service traffic uses `svc-<id>` on the overlay.
	}
}

// internal/app/swarm.go: apply → createOrUpdate, through the app.Swarm port
version, found, err := a.Swarm.ServiceVersion(ctx, spec.NodeID)
if !found {
	err = a.Swarm.CreateService(ctx, spec)
} else {
	err = a.Swarm.UpdateService(ctx, version, spec) // POST /services/svc-<id>/update?version=…
}

// remove (ScheduleRemoveService): a.Swarm.RemoveService(ctx, nodeID), a missing service is fine
```

User actions map to `desired` writes followed by an apply job queued once the write commits (`scheduleApply` in `internal/app/swarm.go`, was `ctx.scheduler.runAfter(0, internal.swarm.apply, ...)`). Applies of one node run one at a time in order, and each re-reads `desired.revision` first: one superseded by a newer ship skips (`superseded by revision N`) instead of applying the older revision last.

| User action | `desired` change                   |
| ----------- | ---------------------------------- |
| Ship        | `image`, `revision + 1`            |
| Stop        | `replicas: 0`                      |
| Start       | `replicas: 1`                      |
| Scale       | `replicas: n`                      |
| Rollback    | `image` = previous, `revision + 1` |
| Pin to node | `pinNodeId`                        |
| Delete      | row deleted, `remove` scheduled    |

### `observe` — Swarm to observed

Event-driven. No cron. The per-node agent POSTs each Docker event (`type=container|service|node`, only containers whose `com.docker.swarm.service.name` label starts with `svc-` and services named `svc-*`, never `exec_*` or `health_status*` actions) to `POST /worker/events` on `keel serve` (the agent's `KEEL_URL`) with a bearer token (`KEEL_WORKER_TOKEN` in serve's env, compared as SHA-256 digests in constant time). The body is always a JSON array: `[]` with `X-Keel-Resync: 1` each time the agent's event stream (re)connects, then a one-element array per event, in order, each post waiting for the previous one. The route (`internal/transport/http/deploy_raw.go`) authenticates, takes only an array (no single object, no NDJSON; over 256 KiB is `413 too large`, anything else malformed or an element without `Type` is `400 bad json`), and hands a trimmed `{type, name, serviceName}` list (`app.DockerEvent`) to `IngestWorkerEvents` (`internal/app/events.go`). A 4xx makes the agent log it and skip the event; anything else is retried every 5 to 35s with the resync header set.

`IngestWorkerEvents` maps each event to a node id (`svc-<id>` from the container label or the service name), drops ids that are not ours (orphan services, the user's own containers), and calls `scheduleObserve` (`internal/app/observe.go`), which is the debounce: a map on `App` holds each node's pending scan, a `Jobs.After` job keyed `observe:<id>:<gen>` (Convex kept its id in `nodes.observeScheduled`); if one is pending and due no later than +500ms the event is coalesced into it, otherwise a new one is scheduled at +500ms. A rollout emits about six events and results in two or three scans, not six. A batch with any `type=node` event schedules `observeServers` once, before the loop (job `observe:servers`), which refreshes the `cluster` row's ready-server count.

`observeNode` frees the node's slot first (so an event that lands mid-scan gets its own scan), then reads one task list filtered on `keel.service=<id>` plus one service inspect (`ObserveService` in `internal/adapters/swarm/observe.go`), and in one write calls `setObserved` and `reconcile` on the result of `summarizeTasks`. Three things in `summarizeTasks` matter:

- `revision` is the max of the service spec label and every task's label. After a rollback the spec label reverts but the failed revision's tasks are still the newest.
- `UpdateStatus.State` from the service is authoritative for updates: `updating` keeps `state: "updating"` even when the new task is already running, because Swarm watches it for `UpdateConfig.Monitor` (5s) and rolls back if it dies. `paused` / `rollback_*` give `state: "failed"` with Swarm's message as `error`. `completed` lets the task counts decide. A fresh `create` has no `UpdateStatus` at all; readiness comes from the `container start` event on the node that runs it.
- If a task is mid-transition (`assigned`, `preparing`, `starting`) the scan re-schedules itself at most twice, 2s apart. The Docker API lags `container start` by ~100ms, so a scan can land on `starting`. This is bounded by Swarm's own transition, not a poll.

`observeAll` (full sweep, every service, job `observe:all`) runs on every `keel serve` start (the `Recover` pass) and is scheduled when the agent sends `X-Keel-Resync: 1`, which it does with the `[]` it posts whenever its event stream (re)connects and on the next post after a failed one. Docker only buffers a small number of past events, so a restart is treated as "we may have missed something". There is no manual trigger any more (Convex had `bunx convex run swarm:observe`); restarting `keel serve` runs one.

Observe logs through `log/slog` (text on stderr, `docker compose -p keel logs keel`) and only on failure: `observeNode`, `observe (full sweep)`, `observeServers` or `schedule observe`, with `node` and `err` attributes. A scan that works writes the node and says nothing, and so does an event.

The only timer left is `timeoutDeployment` (`internal/app/reconcile.go`), a `Jobs.After` job keyed `timeout:<deployment id>` that `beginDeployment` (`internal/app/deployments.go`) queues at +5min per deployment. If the deployment is still running (task `pending` because no node can schedule it, a container that exits 0 and is never restarted, a pull that hangs) every unfinished step fails and the node gets `applyError`. One scheduled check per deployment, not polling. Jobs are in memory, so `Recover` re-arms the timeout of every running deployment at max(now, start + 5min) and re-queues the applies that never reached Swarm.

Measured on the dev box: Ship of a cached `nginx:alpine` goes "service created" → `success` in 3.2s (`container start` event at +2.2s, scan at +2.7s). Redeploy of `postgres:16` completes at +9s, gated by Swarm's `updatestate: completed` at +8.6s (5s Monitor after the new task starts).

`apply` has two related changes: it skips the registry when `ImageCached` succeeds and the deployment was a Ship (`pull: false`; explicit Redeploy passes `pull: true`), and it re-reads the node (`wantedRevision`) right before `createOrUpdate` and once more right after, removing the service it just created if the node was deleted in between. `RemoveNode` (`internal/app/nodes.go`) deletes the row before scheduling the removal (`ScheduleRemoveService`), so that read is authoritative.

Crash-loop: Swarm gives up after `MaxAttempts`. Task shows `failed`, `observe` flags `crashloop` with the exit error, canvas shows red. Matches Railway's "Crashed".

One-shot images (2026-09-15): a container that exits 0 (`hello-world`, a migration) stays in `Mode: Replicated` with `RestartPolicy: on-failure`; Swarm marks the task `complete` / desired `shutdown` and never restarts it, and `docker service ls` reads `0/1` forever. `summarizeTasks` counts those tasks: when the current revision has `complete` tasks and no live or failed ones it reports `state: "completed"` with `completed` and `finishedAt`, `domain.Converged` accepts `completed >= replicas`, `domain.DeriveStatus` (`internal/domain/status.go`) gives `done` (green dot, body `✓ completed 4s ago`, toolbar `Run again`), and the deploy step logs `ran to completion`. `setObserved` records `nodes.one_shot`, which `toSpec` turns into `UpdateConfig.FailureAction: continue`: Swarm treats exit 0 inside the 5s Monitor window as a failed update and with `rollback` every Redeploy of a one-shot rolled back to the previous revision (verified: `rollback_completed` at t+2s). Re-running is a normal Redeploy, since the revision label bump is a spec change and Swarm starts a new task for it. Job mode (`replicated-job`) was measured and rejected: a service's mode cannot be changed after create (`service mode change is not allowed`) and jobs reject `UpdateConfig`.

Node death: Swarm reschedules unpinned tasks itself. Pinned tasks sit `pending` until the node returns. `observe` surfaces `pending` so the canvas can offer "unpin and reschedule" for stateless services. Services with volumes are never unpinned automatically; `nodes.status` gains `removed` for the user-declared case and restore-elsewhere is offered only then. See [`volumes.md`](./volumes.md).

## Builds and registry

Swarm runs images, does not make them. Two pieces still needed, not designed yet:

- **Build.** BuildKit on the control plane, or a build service pinned there. Kicked off from a `keel serve` job, result pushed to the registry, then `apply` with the new tag. Jobs are in memory and die with the process, so the job starts the build and a callback or poll finishes it.
- **Registry.** Workers need to pull the image. `registry:2` as a Swarm service on the control plane, reachable over the tailnet. Image tags are `<tailnet-ip>:5000/svc-<id>:<revision>`.

Cross-node choreography (rolling a revision across pinned nodes one at a time, health check between) is a control-plane workflow: patch `desired` on node 1, wait until observe records `observed.revision`, patch node 2. `keel serve` is the loop, Swarm is the hands. There is no durable workflow engine yet (ARCHITECTURE "Use cases": in-memory jobs plus `Recover`), so this needs one, or a state row that `Recover` resumes from.

## Networking notes

- Public traffic enters through `keel-proxy` on the control plane (a Compose container on the `keel` overlay whose listeners live in the host namespace) and reaches `svc-<id>:<port>` over the overlay. Full design in [`networking.md`](./networking.md). Never publish host ports via `EndpointSpec`; exposing is an endpoint on the node, not a Swarm change.
- Service-to-service traffic uses the `keel` overlay. DNS name is the service name, `svc-<id>`.
- User containers do not get Tailscale directly. Overlay is enough for v1.

## Ops gotchas

- `docker swarm leave --force` on the manager kills every task on that node. Only the control plane is a manager. Never run it there by hand.
- Single manager, no HA. Matches `keel serve` being one process on one SQLite file. Add managers later only if the control plane itself gets HA.
- Updating the `keel` network MTU after creation requires recreating it. Get it right at bootstrap.
- `RestartPolicy.Delay` is in nanoseconds. `5_000_000_000` is 5s.

## Not doing in v1

- Custom scheduler or resource reservations. Swarm decides placement.
- Per-node metrics. `keel agent` (`internal/agent`) is where they go when the canvas needs CPU/RAM graphs; today it forwards events and ships logs.
- Adopt unmanaged containers.
- Manager HA.

## Status (2026-09-14)

Implemented, end-to-end verified on a single-node cluster: create, stop, start, ship (new image), delete. Files: `convex/schema.ts`, `convex/services.ts`, `convex/swarm.ts`, `convex/crons.ts`, `scripts/bootstrap-swarm.sh`, dashboard at `apps/web/src/routes/_auth/dashboard.tsx`.

Deliberately not there yet, in order of need: `nodes` table and join flow, `projects` (services hang off `ownerId` for now), `pinNodeId`, scale, rollback, builds and registry. `observe` also skips node liveness until `nodes` exists. Notes learned:

- Swarm deletes task history when replicas hit 0, so `observed.revision` reads 0 while stopped. UI derives "stopped" from `desired.replicas === 0 && observed.running === 0` before comparing revisions.
- `dockerode` `service.update` needs `version` as a query param and the spec as the body: `update({ _query: { version }, _body: spec })`. Passing them flat sends the spec in the query string.
- Local dev: `CONVEX_AGENT_MODE=anonymous bunx convex dev` runs a local backend on 3210/3211 that talks to the host Docker socket directly. No compose needed.

## Status (2026-09-14, backend wired)

The canvas drives Swarm end-to-end. `services` is gone; deployable nodes (`service | database | cache`) carry `desired`/`observed` on the `nodes` table (`convex/schema.ts`). Files: `nodes.ts` (public), `nodesInternal.ts` (apply input, observed writes), `variables.ts` (own vars with `${{ node.KEY }}` references expanded → `computeEnv`, read by `apply` at run time; `edges.ts` removed 2026-09-26), `deployments.ts` (start + step writers), `reconcile.ts` (settles steps after each observe), `swarm.ts` (apply / remove / observe), `logs.ts` (`docker service logs` demux, polled by the Logs tab). Verified: Add Database + Service → connect → Ship → two `svc-*` services at 1/1 → Logs tab shows nginx/postgres output → Delete removes the Swarm service.

- `apply` now pulls the image first (`docker.pull` + `followProgress`) so the drawer can show pull time, then create/update. A pull failure with a cached image falls back to the cache; without one it fails the step and sets `nodes.applyError` (node goes red).
- `apply` schedules an extra `observe` 3s after it finishes; the 15s cron stays as the sweep. `observe` also writes ready-node count to a single `cluster` row for the status bar.
- `EndpointSpec` is gone: nothing publishes host ports. Consumers reach producers as `svc-<nodeId>:<port>` on the overlay (`DATABASE_URL=postgres://app:…@svc-<id>:5432/app`).
- `deployedRevision` is written by `setObserved` once observed matches desired and every replica runs. A step that applied but never converges fails after 5 min (`reconcile.ts`).
- Docker Hub pulls from this box are slow (postgres:16 took 224s); the drawer stays on "pulling" that long. That is real, not a bug.
- Not there yet: Cancel, Restart, scale/stop UI (`nodes.setDesired` exists, no UI), image editing UI, node liveness rows, pin to node, rollback, builds, registry.

## Status (2026-09-14, event-driven observe)

`crons.ts` is gone. Observation is driven by `docker events` forwarded from every node by the per-node worker (`apps/worker`, `scripts/deploy-worker.sh`, called from `bootstrap-swarm.sh`; was a `docker:cli` shell forwarder until 2026-09-20) into `POST /worker/events` (`convex/http.ts`) → `events.ingest` → debounced `swarm.observeNode`. `observed.state` gained `failed` (Swarm paused or rolled back the last update). The one timer is `reconcile.timeoutDeployment` at +5min per deployment. `apply` uses cached images for Ship and re-checks the node still exists around `createService`; the orphan `svc-*` that race left behind was removed by hand. Verified: Ship converges in ~3s, Redeploy in ~9s via `updatestate: completed`, a bogus token gets 401, killing the forwarder task has Swarm restart it and resume with `--since`. Gotchas:

- Convex module names are `[a-zA-Z0-9_]`; the forwarder's own ids are validated with `db.normalizeId`, so a `svc-<not-ours>` event is ignored instead of throwing.
- The worker reaches Convex on the tailnet IP (`KEEL_URL`, no path); the production `.convex.site` URL works unchanged.
- Token rotation: delete `KEEL_WORKER_TOKEN` from `infra/worker/.env.local` and re-run `scripts/deploy-worker.sh`. It regenerates, updates Convex env, and swaps the Swarm secret.
- Resume state (`eventsSince`, per-container `logsSince`) lives in `state.json` on a named volume (`keel-worker-state`) per node. Losing it costs one full sweep and, for logs, new lines only.

## Status (2026-09-20, worker app)

`apps/worker` replaces the shell forwarder. Image is built locally by `scripts/deploy-worker.sh` and tagged by content hash, so re-running the script after a source change is the release; the service is `--no-resolve-image` while there is no registry (single node). Multi-node needs `KEEL_REGISTRY=<tailnet-ip>:5000` so other nodes can pull it, which is the same registry the builder will need. Bun's `fetch` dials the socket directly (`{ unix: "/var/run/docker.sock" }`), so there is no Docker client library. Verified: `docker events` and container tails resume from `state.json` across a `--force` restart, the resync sweep is requested on every reconnect, the legacy `keel-events` service/config/secret are removed by the deploy script.

## Status (Go port)

`keel serve` and `keel agent` replace the Convex functions, `apps/worker` and `scripts/deploy-worker.sh` named in the entries above. Where they went: `convex/swarm.ts` → `internal/app/swarm.go` (apply, remove) and `internal/app/observe.go` (`observeNode`, `observeAll`, `observeServers`) over `internal/adapters/swarm`; `convex/events.ts` and the `/worker/events` route of `convex/http.ts` → `internal/app/events.go` and `internal/transport/http/deploy_raw.go`; `reconcile.ts` → `internal/app/reconcile.go`; `deployments.ts` → `internal/app/deployments.go`; `apps/worker/src/*` → `internal/agent/*`. Per-function map: [`docs/go/spec/INDEX.md`](./go/spec/INDEX.md). What changed in the gotchas above:

- Agent release: no deploy script. `keel serve` creates or updates `keel-agent` on every start when `KEEL_AGENT_IMAGE` is set (compose sets it to the control plane's own image), pinned by digest when Docker knows one and never resolved against a registry by Swarm. A locally built image on more than one node still needs a registry for the others to pull it.
- The agent's `KEEL_URL` is `KEEL_AGENT_CONTROL_URL` (compose: `http://<KEEL_ADDR>:<KEEL_WEB_PORT>`), else `KEEL_SITE_URL`. One listener for the dashboard, the API and the worker routes; no `:3211`.
- Token rotation: delete `KEEL_WORKER_TOKEN` from `/opt/keel/.env` and re-run `install.sh`, which generates a new one. `keel serve` gets it from compose, writes it into the config it pushes to `keel proxy` (the token of its certificate reports), and its start-up pass updates `keel-agent` with it. The agent gets it as a Swarm secret named after its hash (`keel-agent-token-<hash>`, `EnsureAgent`); a new token makes a new secret and the previous one is removed.
- Resume state: `keel-agent` keeps its `state.json` on its own named volume per node (`keel-agent-state` at `/var/lib/keel-agent`, `internal/agent/state.go`; `KEEL_STATE` overrides the path). Nothing reads the Bun worker's `keel-worker-state`.
- Docker: the agent, like `keel serve`, finds the socket through moby's `client.FromEnv` (`DOCKER_HOST`, default `/var/run/docker.sock`); `DOCKER_SOCKET` is gone.
- Logs: the agent's own lines are `log/slog` text (start, shutdown, failures), not the Bun worker's `[scope] msg {json}` lines.
- Local dev: `keel serve` talks to the host Docker socket directly (`DOCKER_HOST` unset). No compose needed.
