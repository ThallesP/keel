import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { internalMutation, mutation, type MutationCtx, query } from "./_generated/server";
import { ownedEnvironment, ownedNode, requireEnvironment } from "./access";
import type { DeployStep } from "./schema";
import { DEPLOYABLE } from "./status";

const MAX_LOG = 500;
/**
 * The one timer in the deploy path. Observation is event-driven (events.ts), so a deployment
 * whose tasks never produce a Docker event (Swarm `pending`, image that never pulls) needs
 * exactly one scheduled check to fail instead of spinning forever.
 */
export const DEPLOY_TIMEOUT_MS = 5 * 60_000;

export type BeginOptions = {
  /** Restrict to these nodes; otherwise every dirty deployable node ships. */
  only?: Id<"nodes">[];
  /** Re-pull images from the registry (explicit Redeploy). Default: use the local cache. */
  refresh?: boolean;
  /** Verb for the deployment message, e.g. "stop", "start". */
  verb?: string;
};

/**
 * Bumps desired.revision on the affected nodes, records one step per node, schedules apply.
 * Throws when a deployment is already running or nothing is affected.
 */
export async function beginDeployment(
  ctx: MutationCtx,
  environmentId: Id<"environments">,
  { only, refresh = false, verb }: BeginOptions = {},
) {
  const running = await ctx.db
    .query("deployments")
    .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
    .filter((q) => q.eq(q.field("status"), "running"))
    .first();
  if (running) throw new ConvexError("A deployment is already running");

  const nodes = await ctx.db
    .query("nodes")
    .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
    .collect();
  const wanted = only && new Set<Id<"nodes">>(only);
  const affected = nodes.filter(
    (n) => n.desired && DEPLOYABLE.has(n.type) && (wanted ? wanted.has(n._id) : n.dirty),
  );
  if (affected.length === 0) throw new ConvexError("Nothing to ship");

  const now = Date.now();
  for (const n of affected) {
    await ctx.db.patch(n._id, {
      desired: { ...n.desired!, revision: n.desired!.revision + 1 },
      dirty: false,
      shippedAt: now,
      applyError: undefined,
    });
  }

  const steps: DeployStep[] = [
    ...affected.map<DeployStep>((n) => ({ nodeId: n._id, label: n.name, status: "pending" })),
    { label: "health checks", status: "pending" },
  ];
  const word = verb ?? (wanted ? (refresh ? "redeploy" : "deploy") : "ship");
  const message = `${word} ${affected.map((n) => n.name).join(", ")}`;
  const id = await ctx.db.insert("deployments", {
    environmentId,
    message,
    status: "running",
    startedAt: now,
    steps,
    log: [],
  });
  for (const n of affected) {
    await ctx.scheduler.runAfter(0, internal.swarm.apply, {
      id: n._id,
      deploymentId: id,
      pull: refresh,
    });
  }
  await ctx.scheduler.runAfter(DEPLOY_TIMEOUT_MS, internal.reconcile.timeoutDeployment, {
    deploymentId: id,
  });
  return id;
}

export const start = mutation({
  args: {
    environmentId: v.id("environments"),
    only: v.optional(v.array(v.id("nodes"))),
    refresh: v.optional(v.boolean()),
  },
  handler: async (ctx, { environmentId, only, refresh }) => {
    await requireEnvironment(ctx, environmentId);
    return await beginDeployment(ctx, environmentId, { only, refresh });
  },
});

export const latest = query({
  args: { environmentId: v.id("environments") },
  handler: async (ctx, { environmentId }) => {
    if (!(await ownedEnvironment(ctx, environmentId))) return null;
    return await ctx.db
      .query("deployments")
      .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
      .order("desc")
      .first();
  },
});

/** One deployment by id; null when malformed, missing or not owned (the id comes from a URL). */
export const get = query({
  args: { id: v.string() },
  handler: async (ctx, { id: raw }) => {
    const id = ctx.db.normalizeId("deployments", raw);
    const d = id ? await ctx.db.get(id) : null;
    if (!d || !(await ownedEnvironment(ctx, d.environmentId))) return null;
    return d;
  },
});

export const listForNode = query({
  args: { nodeId: v.id("nodes") },
  handler: async (ctx, { nodeId }) => {
    const scope = await ownedNode(ctx, nodeId);
    if (!scope) return [];
    const recent = await ctx.db
      .query("deployments")
      .withIndex("by_environment", (q) => q.eq("environmentId", scope.node.environmentId))
      .order("desc")
      .take(50);
    return recent.filter((d) => d.steps.some((s) => s.nodeId === nodeId)).slice(0, 20);
  },
});

// Internal progress writers, called from swarm.ts.

async function patchStep(
  ctx: MutationCtx,
  deploymentId: Id<"deployments">,
  nodeId: Id<"nodes">,
  change: Partial<DeployStep>,
  text?: string,
) {
  const d = await ctx.db.get(deploymentId);
  if (!d) return;
  const failed = change.status === "failed";
  // A failed apply fails the deployment; the trailing health-checks step goes with it.
  const steps = d.steps.map((s) =>
    s.nodeId === nodeId
      ? { ...s, ...change }
      : failed && !s.nodeId
        ? { ...s, status: "failed" as const, finishedAt: Date.now() }
        : s,
  );
  const log = text ? [...d.log, { at: Date.now(), nodeId, text }].slice(-MAX_LOG) : d.log;
  await ctx.db.patch(deploymentId, {
    steps,
    log,
    ...(failed && d.status === "running" ? { status: "failed", finishedAt: Date.now() } : {}),
  });
}

export const stepRunning = internalMutation({
  args: { deploymentId: v.id("deployments"), nodeId: v.id("nodes"), text: v.string() },
  handler: (ctx, { deploymentId, nodeId, text }) =>
    patchStep(ctx, deploymentId, nodeId, { status: "running", startedAt: Date.now() }, text),
});

export const stepLog = internalMutation({
  args: { deploymentId: v.id("deployments"), nodeId: v.id("nodes"), text: v.string() },
  handler: (ctx, { deploymentId, nodeId, text }) => patchStep(ctx, deploymentId, nodeId, {}, text),
});

export const stepApplied = internalMutation({
  args: { deploymentId: v.id("deployments"), nodeId: v.id("nodes"), text: v.string() },
  handler: (ctx, { deploymentId, nodeId, text }) =>
    patchStep(ctx, deploymentId, nodeId, { appliedAt: Date.now() }, text),
});

export const stepFailed = internalMutation({
  args: { deploymentId: v.id("deployments"), nodeId: v.id("nodes"), text: v.string() },
  handler: (ctx, { deploymentId, nodeId, text }) =>
    patchStep(ctx, deploymentId, nodeId, { status: "failed", finishedAt: Date.now() }, text),
});
