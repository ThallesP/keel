"use node";

import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import { action } from "./_generated/server";
import { axiomTail } from "./logProviders/axiom";
import { dockerTail } from "./logProviders/docker";
import type { LogSink } from "./schema";
import type { Tail } from "./logProviders/types";

export type { LogLine, LogSource, Replica, Tail } from "./logProviders/types";

/**
 * Last `tail` lines of a node's service, from whatever provider the project uses: the project's
 * log sink when one is connected (logSinks.ts), else `docker service logs` on the manager.
 * Polled by the Logs tab; no table involved.
 */
export const tail = action({
  args: { nodeId: v.id("nodes"), tail: v.optional(v.number()) },
  handler: async (ctx, { nodeId, tail = 200 }): Promise<Tail> => {
    const scope: { sink: LogSink | null } | null = await ctx.runQuery(internal.logSinks.forNode, {
      nodeId,
    });
    if (!scope) throw new ConvexError("Node not found");
    const n = Math.min(Math.max(1, Math.floor(tail)), 1000);
    if (scope.sink?.kind === "axiom") return axiomTail(scope.sink, nodeId, n);
    return dockerTail(nodeId, n);
  },
});
