import { defineSchema, defineTable } from "convex/server";
import { type Infer, v } from "convex/values";

export const nodeType = v.union(
  v.literal("service"),
  v.literal("database"),
  v.literal("cache"),
  v.literal("volume"),
  v.literal("group"),
);

// What the user asked for. Only mutations triggered by user actions write this.
// `revision: 0` means never shipped. Env is computed at apply time from `variables`, with
// `${{ node.KEY }}` references expanded (see variables.computeEnv), so it is not stored here.
export const desired = v.object({
  image: v.string(),
  revision: v.number(),
  replicas: v.number(),
  port: v.optional(v.number()),
});

// What Swarm reports. Only swarm.observeNode / swarm.observe write this.
// `failed`: the last `service update` was rolled back or paused by Swarm (new task died inside
// UpdateConfig.Monitor); `revision` is the revision that failed, the service runs the previous one.
// `completed`: every task of this revision exited 0 (a one-shot image such as a migration).
// Swarm leaves such tasks as `complete` and, with RestartPolicy on-failure, never restarts them.
export const observed = v.object({
  revision: v.number(),
  running: v.number(),
  // Tasks of this revision that exited 0. Set with `state: "completed"`.
  completed: v.optional(v.number()),
  // When the last of those tasks finished (Swarm task Status.Timestamp).
  finishedAt: v.optional(v.number()),
  state: v.union(
    v.literal("ok"),
    v.literal("updating"),
    v.literal("crashloop"),
    v.literal("pending"),
    v.literal("failed"),
    v.literal("completed"),
  ),
  nodeIds: v.array(v.string()),
  error: v.optional(v.string()),
  at: v.number(),
});

// Public ingress, set by nodes.expose / nodes.unexpose and applied right away by
// swarm.applyIngress (not by Ship: it is its own Swarm service and never touches the app's).
// `quick-tunnel`: a Cloudflare Quick Tunnel, no account, temporary trycloudflare.com URL.
// A named Cloudflare tunnel (stable custom hostnames) is the next provider. See docs/networking.md.
export const publicIngress = v.object({
  provider: v.literal("quick-tunnel"),
  at: v.number(),
});

// What the ingress service reports. Written only by nodesInternal.setIngress (from swarm.*).
export const ingress = v.object({
  state: v.union(v.literal("starting"), v.literal("live"), v.literal("failed")),
  url: v.optional(v.string()), // https://<random>.trycloudflare.com
  error: v.optional(v.string()),
  at: v.number(),
});

export type Ingress = Infer<typeof ingress>;

export const position = v.object({ x: v.number(), y: v.number() });

// Type-specific, non-deploy settings. Volumes: sizeGb. Groups: width/height.
export const config = v.object({
  sizeGb: v.optional(v.number()),
  width: v.optional(v.number()),
  height: v.optional(v.number()),
});

export const stepStatus = v.union(
  v.literal("pending"),
  v.literal("running"),
  v.literal("done"),
  v.literal("failed"),
);

export const deployStep = v.object({
  nodeId: v.optional(v.id("nodes")), // undefined for the final "health checks" step
  label: v.string(),
  status: stepStatus,
  startedAt: v.optional(v.number()),
  // Set once swarm.apply has created/updated the service; observe takes it from there.
  appliedAt: v.optional(v.number()),
  finishedAt: v.optional(v.number()),
});

export const logLine = v.object({
  at: v.number(),
  nodeId: v.optional(v.id("nodes")),
  text: v.string(),
});

// Where a project's container logs go and where the Logs tab reads them from. `docker` (no row)
// is the default: the Logs tab reads `docker service logs` from the manager and nothing is
// shipped. A sink row means the per-node worker streams every container line of the project
// there and the Logs tab queries the sink instead. Token is a secret: never leaves the server
// except to the worker (bearer-protected /worker/config).
export const logSink = v.union(
  v.object({
    kind: v.literal("axiom"),
    // Ingest/query host, `api.axiom.co` or `api.eu.axiom.co` (a full origin is accepted for tests).
    domain: v.string(),
    dataset: v.string(),
    token: v.string(),
  }),
);

export type NodeType = Infer<typeof nodeType>;
export type LogSink = Infer<typeof logSink>;
export type Desired = Infer<typeof desired>;
export type Observed = Infer<typeof observed>;
export type DeployStep = Infer<typeof deployStep>;

export default defineSchema({
  projects: defineTable({
    name: v.string(),
    slug: v.string(),
    ownerId: v.string(),
  })
    .index("by_owner", ["ownerId"])
    .index("by_slug", ["ownerId", "slug"]),

  environments: defineTable({
    projectId: v.id("projects"),
    name: v.string(),
    isProduction: v.boolean(),
  }).index("by_project", ["projectId"]),

  nodes: defineTable({
    environmentId: v.id("environments"),
    type: nodeType,
    name: v.string(),
    parentId: v.optional(v.id("nodes")),
    position,
    config,
    // service | database | cache only. volume and group never reach Swarm in v1.
    desired: v.optional(desired),
    observed: v.optional(observed),
    // service only: exposed to the internet. Top-level on purpose: `desired` is revision-bumped
    // by Ship and `observed` is replaced wholesale by every scan.
    public: v.optional(publicIngress),
    ingress: v.optional(ingress),
    // Last revision observe saw fully converged.
    deployedRevision: v.optional(v.number()),
    // Config/vars (or a variable it references) changed since the last ship. Drives "Ship · N changes".
    dirty: v.optional(v.boolean()),
    // When the current desired.revision was shipped. Drives the "deploying · 41s" line.
    shippedAt: v.optional(v.number()),
    // Last swarm.apply failure (pull error, bad spec) or a deployment timeout. Cleared on the next ship.
    applyError: v.optional(v.string()),
    // Learned by observe: the last run exited 0 and nothing kept running. swarm.apply then uses
    // UpdateConfig.FailureAction `continue`, because Swarm treats exit 0 inside the 5s Monitor
    // window as a failed update and would roll a Redeploy back to the previous revision.
    oneShot: v.optional(v.boolean()),
    // Debounce for event-driven observation: the pending swarm.observeNode for this node, so a
    // burst of Docker events fans out into one Docker scan. Cleared by observeNode when it runs.
    observeScheduled: v.optional(v.id("_scheduled_functions")),
  }).index("by_environment", ["environmentId"]),

  deployments: defineTable({
    environmentId: v.id("environments"),
    sha: v.optional(v.string()),
    message: v.string(),
    status: v.union(v.literal("running"), v.literal("success"), v.literal("failed")),
    startedAt: v.number(),
    finishedAt: v.optional(v.number()),
    steps: v.array(deployStep),
    log: v.array(logLine),
  })
    .index("by_environment", ["environmentId"])
    .index("by_status", ["status"]),

  variables: defineTable({
    nodeId: v.id("nodes"),
    key: v.string(),
    // May reference other nodes' variables: `${{ postgres.DATABASE_URL }}` (see variables.ts).
    value: v.string(),
    secret: v.boolean(),
  }).index("by_node", ["nodeId"]),

  // Single row: ready Swarm nodes. Written by swarm.observe and, on `node` events, observeServers.
  cluster: defineTable({ servers: v.number(), at: v.number() }),

  // At most one per project. See logSinks.ts and docs/logs.md.
  logSinks: defineTable({
    projectId: v.id("projects"),
    sink: logSink,
  }).index("by_project", ["projectId"]),
});
