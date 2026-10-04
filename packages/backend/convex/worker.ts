import type { Doc } from "./_generated/dataModel";
import { internalQuery } from "./_generated/server";
import { sinkOf } from "./logSinks";

// What the per-node worker (apps/worker) needs from the control plane. Served by
// GET /worker/config (http.ts) behind the worker bearer token; polled every 30s and on start.
// Keep this small and stable: every node holds a copy, and a change here is a worker release.

export const config = internalQuery({
  args: {},
  handler: async (ctx) => {
    if (!(await ctx.db.query("logSinks").first())) return { sinks: [] };
    const projects = await ctx.db.query("projects").collect();
    // Sinks are per organization; the worker routes per project, so each project gets an entry
    // with its organization's sink (the worker shares one queue between equal sinks).
    const sinkOfOrg = new Map<string, Doc<"logSinks"> | null>();
    for (const { organizationId } of projects) {
      if (organizationId && !sinkOfOrg.has(organizationId)) {
        sinkOfOrg.set(organizationId, await sinkOf(ctx, organizationId));
      }
    }
    const environments = await ctx.db.query("environments").collect();
    const nodes = await ctx.db.query("nodes").collect();
    const projectOfEnv = new Map(environments.map((e) => [e._id, e.projectId]));
    return {
      sinks: projects.flatMap((project) => {
        const row = project.organizationId && sinkOfOrg.get(project.organizationId);
        if (!row) return [];
        const serviceIds = nodes
          .filter((n) => n.desired && projectOfEnv.get(n.environmentId) === project._id)
          .map((n) => n._id as string);
        // `since`: when the organization connected this sink. A container the worker has no
        // resume point for is read from here, so nothing written between a container's start and
        // the worker's next poll is skipped, and nothing older than the connect is replayed.
        return [
          {
            projectId: project._id as string,
            serviceIds,
            sink: row.sink,
            since: row._creationTime,
          },
        ];
      }),
    };
  },
});
