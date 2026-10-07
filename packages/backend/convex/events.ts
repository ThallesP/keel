import { type Infer, v } from "convex/values";

import { internal } from "./_generated/api";
import { internalMutation, type MutationCtx } from "./_generated/server";
import { scheduleObserveFor } from "./nodesInternal";

// Docker events, forwarded by the `keel-worker` global service (apps/worker) to
// POST /worker/events. The HTTP route validates and hands the parsed objects here; this
// mutation maps each event to a Convex node and schedules a debounced, single-service observe.
// No Docker access.

const SERVICE_PREFIX = "svc-";

/** What the HTTP route keeps from a raw Docker event; enough to name the node it concerns. */
export const dockerEvent = v.object({
  type: v.string(), // container | service | node
  action: v.string(), // create | start | die | update | ...
  name: v.optional(v.string()), // Actor.Attributes.name: service name, node hostname, container name
  serviceName: v.optional(v.string()), // com.docker.swarm.service.name label on container events
  time: v.optional(v.number()),
});

type DockerEvent = Infer<typeof dockerEvent>;

/** `svc-<convexNodeId>` → the raw id part, else null (user's own container, other service). */
function nodeIdFromEvent(e: DockerEvent): string | null {
  const name = e.type === "container" ? e.serviceName : e.name;
  if (!name?.startsWith(SERVICE_PREFIX)) return null;
  return name.slice(SERVICE_PREFIX.length);
}

async function ingestOne(ctx: MutationCtx, e: DockerEvent, seen: Set<string>) {
  if (e.type === "node") {
    if (!seen.has("node")) {
      seen.add("node");
      await ctx.scheduler.runAfter(0, internal.swarm.observeSwarmNodes, {});
    }
    return;
  }
  if (e.type !== "container" && e.type !== "service") return;
  const id = nodeIdFromEvent(e);
  if (!id || seen.has(id)) return;
  seen.add(id);
  // Ignores ids that are not ours (orphans); coalesces with a scan already pending for this node.
  const scheduled = await scheduleObserveFor(ctx, id);
  console.log(
    `event ${e.type} ${e.action} ${e.serviceName ?? e.name} → observeNode ${scheduled ? "scheduled" : "skipped"}`,
  );
}

export const ingest = internalMutation({
  args: {
    events: v.array(dockerEvent),
    // Worker (re)started: events emitted while it was down may be gone, so sweep once.
    resync: v.optional(v.boolean()),
  },
  handler: async (ctx, { events, resync }) => {
    if (resync) await ctx.scheduler.runAfter(0, internal.swarm.observe, {});
    const seen = new Set<string>();
    for (const e of events) await ingestOne(ctx, e, seen);
  },
});
