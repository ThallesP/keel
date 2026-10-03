import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { action, type ActionCtx } from "./_generated/server";
import type { LogSink } from "./schema";
import { axiomTrace, axiomTraceOverview, TRACE_ID_RE } from "./traceProviders/axiom";
import type { Trace, TraceOverview } from "./traceProviders/types";

export type {
  Attribute,
  Span,
  SpanEvent,
  SpanStatus,
  Trace,
  TraceBucket,
  TraceOverview,
  TraceRange,
  TraceStats,
  TraceSummary,
} from "./traceProviders/types";

// OpenTelemetry traces of a project, read from its sink's traces dataset (docs/logs.md
// "Traces"). Read side only: spans get there from whatever exports them; Keel does not forward
// OTLP yet. Traces need a store, so unlike logs there is no Docker fallback. Not scoped to the
// environment yet: spans carry no Keel ids until Keel sets the OTel resource of its services.

const traceRange = v.union(v.literal("15m"), v.literal("1h"), v.literal("24h"), v.literal("7d"));

/** The environment's sink, pointed at its traces dataset. */
async function tracesSink(ctx: ActionCtx, environmentId: Id<"environments">) {
  const scope: { sink: LogSink | null } | null = await ctx.runQuery(
    internal.logSinks.forEnvironment,
    { environmentId },
  );
  if (!scope) throw new ConvexError("Environment not found");
  if (scope.sink?.kind !== "axiom") throw new ConvexError("Connect Axiom to see traces");
  if (!scope.sink.traces) throw new ConvexError("Sign in with Axiom again to turn on traces");
  return { ...scope.sink, dataset: scope.sink.traces };
}

const fail = (err: unknown): never => {
  throw new ConvexError(err instanceof Error ? err.message : String(err));
};

/** Request rate, errors and latency over `range`, and the latest requests. Backs the Traces tab. */
export const overview = action({
  args: {
    environmentId: v.id("environments"),
    range: traceRange,
    search: v.optional(v.string()),
  },
  handler: async (ctx, { environmentId, range, search = "" }): Promise<TraceOverview> => {
    const sink = await tracesSink(ctx, environmentId);
    return axiomTraceOverview(sink, range, search.slice(0, 200)).catch(fail);
  },
});

/** Every span of one trace. `at`: when its root started, if known (narrows the search). */
export const get = action({
  args: {
    environmentId: v.id("environments"),
    traceId: v.string(),
    at: v.optional(v.number()),
  },
  handler: async (ctx, { environmentId, traceId, at }): Promise<Trace> => {
    if (!TRACE_ID_RE.test(traceId)) throw new ConvexError("Not a trace id");
    const sink = await tracesSink(ctx, environmentId);
    return axiomTrace(sink, traceId.toLowerCase(), at).catch(fail);
  },
});
