import { internalQuery } from "./_generated/server";

// What the per-node worker (apps/worker) needs from the control plane. Served by
// GET /worker/config (http.ts) behind the worker bearer token; polled every 30s and on start.
// Keep this small and stable: every node holds a copy, and a change here is a worker release.

export const config = internalQuery({
  args: {},
  handler: async (ctx) => {
    const sinks = await ctx.db.query("logSinks").collect();
    if (sinks.length === 0) return { sinks: [] };
    const environments = await ctx.db.query("environments").collect();
    const nodes = await ctx.db.query("nodes").collect();
    const projectOfEnv = new Map(environments.map((e) => [e._id, e.projectId]));
    return {
      sinks: sinks.map((row) => {
        const serviceIds = nodes
          .filter((n) => n.desired && projectOfEnv.get(n.environmentId) === row.projectId)
          .map((n) => n._id as string);
        return { projectId: row.projectId as string, serviceIds, sink: row.sink };
      }),
    };
  },
});
