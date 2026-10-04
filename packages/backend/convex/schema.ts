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

// Where an organization's container logs go and where the Logs tab reads them from. `docker` (no
// row) is the default: the Logs tab reads `docker service logs` from the manager and nothing is
// shipped. A sink row means the per-node worker streams every container line of every project in
// the organization there and the Logs tab queries the sink instead. Token is a secret: never
// leaves the server except to the worker (bearer-protected /worker/config). The same sink holds
// the projects' OpenTelemetry traces (docs/logs.md "Traces").
export const logSink = v.union(
  v.object({
    kind: v.literal("axiom"),
    // Ingest/query host, `api.axiom.co` or `api.eu.axiom.co` (a full origin is accepted for tests).
    domain: v.string(),
    dataset: v.string(),
    // Traces dataset: Axiom wants a dedicated dataset per OTel signal. Same token as `dataset`.
    // Unset on sinks connected before traces existed; the Observability page asks to sign in again.
    traces: v.optional(v.string()),
    token: v.string(),
    // Axiom org name, when connected through Sign in with Axiom. Display only.
    org: v.optional(v.string()),
  }),
);

/** An Axiom org as Sign in with Axiom lists it (logProviders/axiom.ts axiomOrgs). */
export const axiomOrg = v.object({
  id: v.string(),
  name: v.string(),
  // API host its data lives on.
  domain: v.string(),
  // Its plan's dataset cap (`license.maxDatasets`; 3 on the free Personal plan).
  maxDatasets: v.optional(v.number()),
});

export type NodeType = Infer<typeof nodeType>;
export type LogSink = Infer<typeof logSink>;
export type Desired = Infer<typeof desired>;
export type Observed = Infer<typeof observed>;
export type DeployStep = Infer<typeof deployStep>;

export default defineSchema({
  projects: defineTable({
    name: v.string(),
    slug: v.string(),
    // The organization this project belongs to: a Better Auth organization-plugin row id (see
    // auth.ts, access.ts). Unset only on rows from before organizations existed; projects.ensureDefault
    // adopts those into the install's organization and clears `ownerId`.
    organizationId: v.optional(v.string()),
    ownerId: v.optional(v.string()),
  })
    .index("by_organization", ["organizationId"])
    .index("by_slug", ["organizationId", "slug"]),

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

  // At most one per organization; every project of it ships there. See logSinks.ts and
  // docs/logs.md.
  logSinks: defineTable({
    // A Better Auth organization id, like projects.organizationId. Unset only on rows from before
    // sinks were per organization, which name their `projectId` instead: until a sign-in or
    // Disconnect replaces them, the newest of an organization's stands in for its sink (sinkOf).
    organizationId: v.optional(v.string()),
    projectId: v.optional(v.id("projects")),
    sink: logSink,
  }).index("by_organization", ["organizationId"]),

  // Sign in with Axiom: the OAuth client this install registered with Axiom (DCR), one per
  // callback URL. Public client ids, not secrets. See logProviders/axiom.ts.
  axiomClients: defineTable({
    redirectUri: v.string(),
    clientId: v.string(),
  }).index("by_redirect", ["redirectUri"]),

  // Sign in with Axiom, between beginAxiomSignIn and Axiom's redirect back: the PKCE verifier
  // for `state`. Single use, 10 minutes. See logSinks.ts.
  axiomSignIns: defineTable({
    organizationId: v.string(),
    clientId: v.string(),
    state: v.string(),
    verifier: v.string(),
    redirectUri: v.string(),
  })
    .index("by_state", ["state"])
    .index("by_organization", ["organizationId"]),

  // Sign in with Axiom, between the code exchange and the user picking one of several orgs.
  // Holds the personal token for at most 10 minutes; deleted on pick. See logSinks.ts.
  axiomPending: defineTable({
    organizationId: v.string(),
    token: v.string(),
    orgs: v.array(axiomOrg),
  }).index("by_organization", ["organizationId"]),
});
