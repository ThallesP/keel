import { v } from "convex/values";

import { internal } from "./_generated/api";
import { internalMutation, internalQuery, type MutationCtx } from "./_generated/server";
import { ownedNode } from "./access";
import { observed } from "./schema";
import { converged } from "./status";
import { withTracing } from "./tracing";
import { computeEnv } from "./variables";

// Internal, used by swarm.ts and logs.ts.

/** Node id if the signed-in user owns it. Auth propagates from the calling action. */
export const owned = internalQuery({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => (await ownedNode(ctx, id)) !== null,
});

export const listDeployable = internalQuery({
  args: {},
  handler: async (ctx) => {
    const all = await ctx.db.query("nodes").collect();
    return all
      .filter((n) => n.desired && n.desired.revision > 0)
      .map((n) => ({ id: n._id, environmentId: n.environmentId }));
  },
});

/** Everything apply needs: desired + the full env (references expanded, tracing variables added). */
export const applyInput = internalQuery({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const node = await ctx.db.get(id);
    if (!node?.desired) return null;
    return {
      name: node.name,
      desired: node.desired,
      env: await withTracing(ctx, node, await computeEnv(ctx, node)),
      oneShot: node.oneShot ?? false,
    };
  },
});

export const setObserved = internalMutation({
  args: { id: v.id("nodes"), observed },
  handler: async (ctx, { id, observed }) => {
    const node = await ctx.db.get(id);
    if (!node) return; // deleted between listDeployable and here
    const next = { ...node, observed };
    await ctx.db.patch(id, {
      observed,
      deployedRevision:
        converged(next) && observed.revision > 0 ? observed.revision : node.deployedRevision,
      // A run that exits 0 marks the image one-shot; a task that stays up marks it a daemon.
      oneShot: observed.state === "completed" ? true : observed.running > 0 ? false : node.oneShot,
    });
  },
});

/** Long enough to coalesce the ~6 events one rollout emits into one Docker scan. */
const OBSERVE_DEBOUNCE_MS = 500;

/**
 * Debounced: schedules swarm.observeNode unless a scan is already pending that runs no later.
 * A pending scan further out (a settle re-check) is cancelled in favour of the sooner one.
 * Ids that are not ours (orphan Swarm service, a user's own container) are ignored.
 */
export async function scheduleObserveFor(
  ctx: MutationCtx,
  rawId: string,
  { delayMs = OBSERVE_DEBOUNCE_MS, settle }: { delayMs?: number; settle?: number } = {},
) {
  const id = ctx.db.normalizeId("nodes", rawId);
  const node = id && (await ctx.db.get(id));
  if (!id || !node?.desired) return false;
  const pending = node.observeScheduled && (await ctx.db.system.get(node.observeScheduled));
  if (pending?.state.kind === "pending") {
    if (pending.scheduledTime <= Date.now() + delayMs) return false;
    await ctx.scheduler.cancel(pending._id);
  }
  const scheduled = await ctx.scheduler.runAfter(delayMs, internal.swarm.observeNode, {
    id,
    settle,
  });
  await ctx.db.patch(id, { observeScheduled: scheduled });
  return true;
}

export const scheduleObserve = internalMutation({
  args: {
    id: v.id("nodes"),
    delayMs: v.optional(v.number()),
    settle: v.optional(v.number()),
  },
  handler: (ctx, { id, delayMs, settle }) => scheduleObserveFor(ctx, id, { delayMs, settle }),
});

export const clearObserveScheduled = internalMutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    if (await ctx.db.get(id)) await ctx.db.patch(id, { observeScheduled: undefined });
  },
});

export const setApplyError = internalMutation({
  args: { id: v.id("nodes"), error: v.optional(v.string()) },
  handler: async (ctx, { id, error }) => {
    if (await ctx.db.get(id)) await ctx.db.patch(id, { applyError: error });
  },
});
