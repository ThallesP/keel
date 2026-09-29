import { v } from "convex/values";

import { internalMutation, query } from "./_generated/server";
import { ownedEnvironment } from "./access";
import { DEPLOYABLE, deriveStatus, type NodeStatus } from "./status";

/** Ship-button + status-bar numbers: dirty node count, status counts, Swarm server count. */
export const summary = query({
  args: { environmentId: v.id("environments") },
  handler: async (ctx, { environmentId }) => {
    if (!(await ownedEnvironment(ctx, environmentId))) return null;
    const nodes = await ctx.db
      .query("nodes")
      .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
      .collect();
    const counts: Partial<Record<NodeStatus, number>> = {};
    let pendingChanges = 0;
    for (const n of nodes) {
      if (n.type === "group") continue;
      if (n.dirty && DEPLOYABLE.has(n.type)) pendingChanges += 1;
      const status = deriveStatus(n);
      counts[status] = (counts[status] ?? 0) + 1;
    }
    const cluster = await ctx.db.query("cluster").first();
    return { pendingChanges, counts, servers: cluster?.servers ?? 0 };
  },
});

export const setServers = internalMutation({
  args: { servers: v.number() },
  handler: async (ctx, { servers }) => {
    const row = await ctx.db.query("cluster").first();
    if (row) await ctx.db.patch(row._id, { servers, at: Date.now() });
    else await ctx.db.insert("cluster", { servers, at: Date.now() });
  },
});
