# Volumes — persistent data on Swarm

> How Keel keeps user data alive when nodes die, and how it moves data between nodes. Decided 2026-09-13. Depends on [`workers.md`](./workers.md) (Swarm as reconciler, Convex as control plane). Read this before touching anything that mounts a volume, schedules a backup, or moves a service between servers.

## Decision

**Pin + backup.** A volume lives on exactly one node. The service that mounts it is pinned to that node with one replica. Durability comes from scheduled backups to a second place. Node death is handled by restoring onto another node, on user click. Same model as Railway, Fly, Coolify and Dokploy.

**No distributed storage.** No Ceph, Longhorn, GlusterFS, DRBD, NFS-as-primary, JuiceFS. All of them are k8s-shaped, need 3+ nodes, eat RAM, or corrupt databases. Wrong for the cheap-VPS / homelab audience.

**Migration is a first-class workflow.** Two-pass rsync over the overlay: copy while the service is live, stop, copy the delta, start on the new node. Downtime is the delta pass plus a container boot, independent of volume size. Railway does the same and lands at 30–45s. Coolify and Dokploy have no move at all.

Two kinds of "node disappeared", two different answers:

| Case | Behavior |
|---|---|
| Blip (reboot, tailnet drop) | Wait. Task sits `pending`, data intact. Canvas: "waiting for node X". |
| Dead (VPS deleted, disk gone) | User clicks "Restore to node B". Restore from last backup. Only a second copy helps here. |

Never auto-reschedule a stateful service. If Swarm moves it, the new node gets an empty volume. When the old node comes back you have two Postgres instances with different data. Pinning kills that whole class of bug.

## Why not the alternatives

| Option | Why not |
|---|---|
| Ceph/Rook, Longhorn, Portworx | k8s-shaped, 3+ nodes, RAM hungry. Kills the cheap-VPS story. |
| GlusterFS | Dying, Red Hat dropped it. Corrupts DBs. |
| NFS as primary | Single point of failure anyway. Postgres footgun, SQLite locking broken. Fine as a backup *target*. |
| DRBD / LINSTOR | Works, but kernel module and real ops burden. Wrong audience. |
| JuiceFS, SeaweedFS | FUSE plus own metadata DB. Slow for DBs, one more thing to run. |
| Swarm volume plugins | Ecosystem dead. rexray archived, StorageOS gone. |
| Swarm Cluster Volumes (CSI, Docker 23+) | Moves the *attachment*, not the data. Needs a shared backend underneath. No snapshot, no clone. Plugin per node. Useless for local disk. |
| Fly-style block clone (`dm-clone` over iSCSI) | Gold standard, near-zero downtime. Needs LVM thin pools on every node. Too heavy for v1. Steal the ordering (stop → attach → boot), not the mechanism. |

Block-level replication of a live Postgres dir is also worse than DB-native backup: it replicates corruption and gives no point-in-time recovery.

## Rules

1. A `volume` node on the canvas has a `nodeId`. That is the source of truth for placement.
2. Edge service → volume means: mount it, pin the service to the volume's node, force `replicas: 1`. If the service already has a different `pinNodeId`, the edge wins and the canvas says so.
3. A `database` node is a service with an implicit volume. Same rules.
4. Dragging a stateful service to another server is the migration workflow below, never a free repin.
5. Stateful services get `UpdateConfig.Order: "stop-first"`. `start-first` (the default in `workers.md`) runs two containers on the same volume during a rollout. Postgres refuses the second postmaster, the update fails, Swarm rolls back, loop. Stateless keeps `start-first`.
6. Stateful services get `StopGracePeriod` of 60s or more. Docker's default 10s SIGKILLs a Postgres mid-checkpoint. A dirty data dir is what the final migration pass would then copy.
7. `nodes.status` gains `removed`. `down` means Swarm cannot reach it. `removed` means the user declared it gone. Restore-elsewhere is offered on `removed` and on user click, never triggered by `down`.

## Backup

### By kind

| Kind | Method | v1 | Later |
|---|---|---|---|
| `postgres` / `mysql` / `mongo` | DB-native dump over the overlay. Backup job connects to `svc-<id>:5432`, runs `pg_dump`. No exec on the worker. | nightly dump | WAL-G / pgBackRest for point-in-time recovery |
| `sqlite` | Litestream sidecar pinned to the same node, same volume. Near-zero RPO. | skip | yes |
| `generic` | restic snapshot of the volume. Encrypted, dedup, one Go binary. | yes | — |
| `redis` | RDB lives in the volume. It's a cache. | skip | optional |

Never restic a live Postgres data dir. Torn writes. If a `generic` volume looks like it holds a DB (`PG_VERSION`, `ibdata1`, `*.sqlite` at the root), warn and suggest the right kind.

### Targets

- **Another node in the cluster.** `restic/rest-server` as a Swarm service pinned to node B, storage in a local volume on B, reachable over the `keel` overlay. Zero external accounts. Default for the multi-node homelab user. Uses the mesh we already have.
- **S3-compatible.** B2, R2, Hetzner Object Storage, a NAS running MinIO or Garage. Default for the single-node user.
- restic repo password is generated per volume, stored in Convex, shown once. Lose it, lose the backups. Say so in the UI.

### Mechanics

- Backup and restore are Swarm jobs: `Mode: { ReplicatedJob: { MaxConcurrent: 1, TotalCompletions: 1 } }`, placement pinned to the node, volume mounted `ro` for backup, `rw` for restore, image `keel/dataplane`. Runs, exits 0, task state goes `complete`. `observe` sees it. No always-on sidecar. Fits "Convex is the loop, Swarm is the hands".
- Convex cron drives the schedule. Each run inserts a `backups` row and schedules the job.
- Restore-to-other-node: create the volume on B → run the restore job pinned to B → patch `volumes.nodeId` → `apply` the service with the new pin. The old volume on A, if A ever returns, is an orphan. See cleanup below.
- Usage: the manager cannot inspect worker volumes. The backup job reports `du -sb`. Canvas shows `3.2 GB used · as of last backup`. Good enough for v1.
- "Test restore" button (later): restore the latest snapshot onto a scratch volume on another node, run the service healthcheck against it, report. Backups are only real if restored. Cheap DX win nobody in this class offers.

## Migration

Move a volume, and the service pinned to it, from node A to node B.

### Flow

```
 A live ──► warm 1     rsync full A→B            minutes, no downtime
 A live ──► warm 2..n  rsync delta               until delta < threshold or n = 3
 stop A                replicas=0, await observed.running == 0
 A down ──► final      rsync delta [--checksum]  seconds
 verify                rsync --dry-run, expect zero diff
 commit                volumes.nodeId=B, pin=B, replicas=1, await observed ok on B
 A orphan              keep N days, then wipe
```

Downtime is the final pass plus container boot. The warm loop makes the final pass tiny. Skip the loop for small volumes; one warm pass is enough under a few GB.

### Steps as a Convex workflow

```
migrateVolume(volumeId, toNodeId)
 1. preflight   job@B: df on the docker root. Abort if free < usedBytes * 1.2.
 2. src         service mig-<id>-src @A: volume mounted ro, rsync --daemon on the
                overlay, auth from a Swarm secret created for this migration.
 3. warm        job@B: rsync -a --delete rsync://mig-<id>-src/vol/ /vol/
                Repeat while the transferred bytes of the last pass exceed the
                threshold, max 3 passes.
 4. stop        desired.replicas = 0 → apply → awaitEvent observed.running == 0.
 5. final       job@B: same rsync, plus --checksum when volume.kind is a database.
 6. verify      job@B: rsync --dry-run --itemize-changes [--checksum]. Any output = abort.
 7. commit      volumes.nodeId = B, service pin = B, replicas = 1 → apply →
                awaitEvent observed.state == "ok" && observed.nodeIds == [B].
 8. cleanup     remove mig-<id>-src and the secret. Insert an orphans row for A.
```

Abort at any step before 7: replicas back to 1 on A, delete the partial volume on B, remove the src service and secret. A was never written. Zero risk until commit. After commit, forward only.

### Why this shape

- **A is mounted `ro` the whole time.** The migration cannot damage the source.
- **B pulls, A serves.** The job on B exits 0 when done and `observe` reports `complete`. The src on A is a dumb daemon removed at the end. No push, no callback.
- **Volume is filled before the task lands.** Repinning first makes Docker create an empty volume on B. Postgres would `initdb` fresh and the user would think the migration ate their data. Order is not negotiable.
- **Same volume name on both nodes.** Swarm volume names are node-local. The `apply` spec changes only the placement constraint.
- **Transport is the overlay.** Both nodes are already on `keel`, already WireGuard via Tailscale. No SSH, no new ports, no key distribution. Do not enable overlay encryption (`--opt encrypted` is IPsec, slow, and redundant).

### rsync details that matter

- **`--checksum` on the final pass for database kinds.** rsync's quick check is size plus mtime. Postgres can write into the middle of a file in the same second a warm pass read it. Size and mtime match, the delta pass skips it, the copy is corrupt. WAL replay would repair this on a crash start, but a clean shutdown means no replay. Raised on pgsql-hackers by Momjian in 2012, still true. The cost is reading every file on both sides, which is fine on a stopped source over WireGuard. Generic volumes skip it.
- **Clean shutdown is required.** `docker stop` sends SIGTERM, Postgres does a fast shutdown and flushes. This is where `StopGracePeriod` from the rules above earns its keep.
- **Never `--inplace`.** Default rsync writes a temp file and renames. An interrupted pass leaves B mixed but every file atomic, and A is untouched, so retry. `--inplace` breaks that.
- **`--delete` on every pass.** Postgres rotates WAL, SQLite drops journals. Without it B accumulates ghost files.
- **Trailing slashes.** `rsync src/ dst/`. Combined with `--delete`, the wrong form wipes the destination. Hardcode the paths in the job image, never template them.
- **Excludes for Postgres:** `postmaster.pid`, `postmaster.opts`, `pg_replslot/*`. Nothing else. Config files travel with the volume.

### Swarm gotchas

- `observe` currently filters on the `keel.service` label. Jobs carry `keel.job` instead, with a separate branch that tracks `Status.State === "complete" | "failed"` and writes `jobs.observed`.
- Jobs get `RestartPolicy.Condition: "on-failure", MaxAttempts: 3`. rsync is idempotent, a retry resumes. `Delay` 5s.
- The manager cannot `docker volume rm` on a worker. Cleanup runs a job on the node that bind-mounts `/var/run/docker.sock` and removes the volume by name. Same trick gives a real `du` for `observed.usedBytes`. Every such job is labeled and uses our image. Never expose this from a public action, same rule as `workers.md`.
- Job tasks join the overlay like any other task. The src service needs `EndpointSpec.Mode: "dnsrr"` so the job resolves `mig-<id>-src` to the task IP, not a VIP. VIP works too, dnsrr is one less moving part.
- `TotalCompletions: 1` and `MaxConcurrent: 1`. Two rsyncs into the same target is a race.

### Not zero downtime, and that is fine

A writer must stop for a consistent final pass. True zero needs a Postgres streaming replica promoted on B, or a Fly-style lazy block clone. Both are later, both are kind-specific. 30s is Railway's bar. Match it. Coolify and Dokploy have nothing.

## Schema

```ts
volumes: defineTable({
  projectId: v.id("projects"),
  environmentId: v.id("environments"),
  name: v.string(),
  nodeId: v.id("nodes"),                       // where the data is. Source of truth for pinning.
  kind: v.union(
    v.literal("generic"), v.literal("postgres"), v.literal("mysql"),
    v.literal("mongo"), v.literal("sqlite"), v.literal("redis"),
  ),
  sizeLimitBytes: v.optional(v.number()),      // canvas subtitle "Volume · 10 GB". Soft, not enforced by Docker.
  backup: v.optional(v.object({
    target: v.union(
      v.object({ type: v.literal("node"), nodeId: v.id("nodes") }),
      v.object({ type: v.literal("s3"), endpoint: v.string(), bucket: v.string(), credentialId: v.id("credentials") }),
    ),
    schedule: v.string(),                      // cron expr
    retention: v.object({ daily: v.number(), weekly: v.number() }),
    resticPassword: v.string(),                // shown once at creation
  })),
  observed: v.optional(v.object({
    usedBytes: v.number(),
    at: v.number(),
    source: v.union(v.literal("backup"), v.literal("du")),
  })),
}).index("by_node", ["nodeId"]).index("by_project", ["projectId"]),

backups: defineTable({
  volumeId: v.id("volumes"),
  snapshotId: v.optional(v.string()),          // restic snapshot id or dump object key
  sizeBytes: v.optional(v.number()),
  startedAt: v.number(),
  finishedAt: v.optional(v.number()),
  status: v.union(v.literal("running"), v.literal("ok"), v.literal("failed")),
  error: v.optional(v.string()),
}).index("by_volume", ["volumeId", "startedAt"]),

migrations: defineTable({
  volumeId: v.id("volumes"),
  fromNodeId: v.id("nodes"),
  toNodeId: v.id("nodes"),
  step: v.union(
    v.literal("preflight"), v.literal("warm"), v.literal("stopping"),
    v.literal("final"), v.literal("verify"), v.literal("committing"),
    v.literal("done"), v.literal("aborted"), v.literal("failed"),
  ),
  pass: v.number(),                            // warm pass counter
  bytesTotal: v.optional(v.number()),
  bytesDone: v.optional(v.number()),           // from rsync --info=progress2 in the job log
  startedAt: v.number(),
  stoppedAt: v.optional(v.number()),           // when replicas hit 0. downtime = committedAt - stoppedAt
  committedAt: v.optional(v.number()),
  error: v.optional(v.string()),
}).index("by_volume", ["volumeId"]),

orphans: defineTable({                          // volumes left behind on a node after migrate or restore
  nodeId: v.id("nodes"),
  volumeName: v.string(),
  volumeId: v.id("volumes"),
  reason: v.union(v.literal("migrated"), v.literal("restored")),
  wipeAfter: v.number(),                       // default now + 7d. Cron wipes when past and node is ready.
}).index("by_node", ["nodeId"]),

jobs: defineTable({                             // one-shot Swarm jobs. Backup, restore, migrate passes, cleanup.
  kind: v.string(),
  swarmServiceName: v.string(),
  nodeId: v.id("nodes"),
  refId: v.string(),                           // backups / migrations row
  observed: v.optional(v.object({
    state: v.union(v.literal("pending"), v.literal("running"), v.literal("complete"), v.literal("failed")),
    error: v.optional(v.string()),
    at: v.number(),
  })),
}).index("by_ref", ["refId"]),
```

`services.desired` gains:

```ts
volumes: v.array(v.object({ volumeId: v.id("volumes"), mountPath: v.string() })),
stopGraceSeconds: v.optional(v.number()),
```

`apply` derives the placement constraint from the first volume's `nodeId` and forces `replicas ≤ 1` and `Order: "stop-first"` when `volumes` is non-empty. It does not trust `desired.pinNodeId` for stateful services.

Mount spec in `toSpec`:

```ts
Mounts: s.volumes.map((m) => ({
  Type: "volume",
  Source: `vol-${m.volumeId}`,
  Target: m.mountPath,
  VolumeOptions: { Labels: { "keel.volume": m.volumeId } },
})),
StopGracePeriod: (s.stopGraceSeconds ?? 10) * 1_000_000_000,
```

## The dataplane image

One image, `keel/dataplane`, used by every job: rsync, restic, `pg_dump`/`pg_restore`, `mysqldump`, `mongodump`, coreutils. Alpine base. Entrypoint takes a subcommand: `backup`, `restore`, `rsync-src`, `rsync-pull`, `rsync-verify`, `du`, `volume-rm`. Arguments come from env, secrets from `/run/secrets`. Never from a public action.

Migration and disaster recovery share code: restore-to-other-node is the migration flow with restic in place of rsync at step 5, and no source service.

## Canvas

- Volume node subtitle `Volume · 10 GB` from `sizeLimitBytes`, body `3.2 GB used` from `observed.usedBytes`. Add a third line `on hetzner-1` in `--color-ink-muted`.
- Edge service → volume: solid, label is the mount path. Drawing it pins the service. If the service was pinned elsewhere, toast "Pinned to hetzner-1 because of the volume".
- **Move:** drag a volume node onto a server on the canvas, or "Move to…" in the floating toolbar. Modal: `Move volume · 2.1 GB · est. downtime ~30s`. Confirm starts the workflow.
- **Progress:** on the edge between old and new server. `copying 1.4 / 2.1 GB` → `syncing` → `cutover` → done. Abort button visible until `committing`.
- **Node dead:** volume node shows amber `node unreachable`. If the user marks the node `removed`, the volume shows red and a "Restore to…" action listing nodes and the last good backup time.
- **No backup configured:** amber badge on the volume node. Persistent. This is the one nag we allow.

## Not doing in v1

- Postgres streaming replica and promote (zero-downtime move, HA).
- Litestream for SQLite.
- Point-in-time recovery.
- Fly-style block clone.
- Volumes shared between services. One volume, one service.
- Volume size enforcement. `sizeLimitBytes` is a label.
- Garage as a canvas node for apps that want S3.
