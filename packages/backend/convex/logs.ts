"use node";

import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import { action } from "./_generated/server";
import { axiomLines, axiomRecent, axiomTail } from "./logProviders/axiom";
import { dockerTail } from "./logProviders/docker";
import type { LogSink } from "./schema";
import { rangeWindow, timeRange } from "./timeRange";
import type { ProjectLine, ProjectTail, Tail } from "./logProviders/types";

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

type Scope = { sink: LogSink | null; serviceIds: string[] } | null;

/**
 * Last `tail` lines across every service of an environment, optionally filtered by a substring
 * and limited to the last `range`. Backs the Observability page; needs a log store (Axiom),
 * Docker has no cross-service query.
 */
export const recent = action({
  args: {
    environmentId: v.id("environments"),
    search: v.optional(v.string()),
    tail: v.optional(v.number()),
    range: v.optional(timeRange),
  },
  handler: async (ctx, { environmentId, search = "", tail = 300, range }): Promise<ProjectTail> => {
    const scope: Scope = await ctx.runQuery(internal.logSinks.forEnvironment, { environmentId });
    if (!scope) throw new ConvexError("Environment not found");
    if (scope.sink?.kind !== "axiom") throw new ConvexError("Connect Axiom to search all logs");
    const n = Math.min(Math.max(1, Math.floor(tail)), 1000);
    // The same bucket-aligned start as traces.overview, so lines and requests cover one window.
    const from = range ? rangeWindow(range).from : undefined;
    try {
      return await axiomRecent(scope.sink, scope.serviceIds, n, search.slice(0, 200), from);
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
  },
});

const AROUND_MS = 30_000;

/**
 * Every service's lines within 30s either side of `at`, oldest first: the surrounding context of
 * a log line that names no trace. Axiom only, like `recent`.
 */
export const around = action({
  args: { environmentId: v.id("environments"), at: v.number() },
  handler: async (ctx, { environmentId, at }): Promise<ProjectLine[]> => {
    const scope: Scope = await ctx.runQuery(internal.logSinks.forEnvironment, { environmentId });
    if (!scope) throw new ConvexError("Environment not found");
    if (scope.sink?.kind !== "axiom") throw new ConvexError("Connect Axiom to search all logs");
    try {
      return await axiomLines(scope.sink, scope.serviceIds, {
        n: 500,
        from: at - AROUND_MS,
        to: at + AROUND_MS,
        oldestFirst: true,
      });
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
  },
});
