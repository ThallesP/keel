"use node";

import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import { action } from "./_generated/server";
import { axiomRecent, axiomTail } from "./logProviders/axiom";
import { dockerTail } from "./logProviders/docker";
import type { LogSink } from "./schema";
import type { ProjectTail, Tail } from "./logProviders/types";

export type {
  LogLine,
  LogSource,
  ProjectLine,
  ProjectTail,
  Replica,
  Tail,
} from "./logProviders/types";

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

/**
 * Last `tail` lines across every service of an environment, optionally filtered by a substring.
 * Backs the Logs page; needs a log store (Axiom), Docker has no cross-service query.
 */
export const recent = action({
  args: {
    environmentId: v.id("environments"),
    search: v.optional(v.string()),
    tail: v.optional(v.number()),
  },
  handler: async (ctx, { environmentId, search = "", tail = 300 }): Promise<ProjectTail> => {
    const scope: { sink: LogSink | null; serviceIds: string[] } | null = await ctx.runQuery(
      internal.logSinks.forEnvironment,
      { environmentId },
    );
    if (!scope) throw new ConvexError("Environment not found");
    if (scope.sink?.kind !== "axiom") throw new ConvexError("Connect Axiom to search all logs");
    const n = Math.min(Math.max(1, Math.floor(tail)), 1000);
    try {
      return await axiomRecent(scope.sink, scope.serviceIds, n, search.slice(0, 200));
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
  },
});
