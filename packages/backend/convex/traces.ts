import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { action, type ActionCtx } from "./_generated/server";
import { axiomLines } from "./logProviders/axiom";
import type { LogSink } from "./schema";
import { timeRange } from "./timeRange";
import {
  axiomRequestsBetween,
  axiomTraceOverview,
  axiomTraceSpans,
  TRACE_ID_RE,
} from "./traceProviders/axiom";
import type { Trace, TraceOverview, TraceSummary } from "./traceProviders/types";
import { NO_SINK, NO_TRACES } from "./tracing";

export type {
  Attribute,
  Span,
  SpanEvent,
  SpanStatus,
  Trace,
  TraceBucket,
  TraceOverview,
  TraceStats,
  TraceSummary,
} from "./traceProviders/types";
export type { TimeRange } from "./timeRange";

// OpenTelemetry traces of an environment, read from the traces dataset of its organization's
// sink, joined with the log lines that name them (docs/logs.md "Traces"). Spans get there through
// the OTLP relay (otlp.ts) from services with tracing on and from `keel run`, both tagged with
// the Keel service id (tracing.ts), which is what scopes requests to the environment here. A
// trace opened by id is not scoped: the id is the key. Traces need a store, so unlike logs there
// is no Docker fallback.

type Scope = { sink: LogSink | null; serviceIds: string[] } | null;

/** The environment's Axiom sink and the services its logs are scoped to. */
async function axiomScope(ctx: ActionCtx, environmentId: Id<"environments">) {
  const scope: Scope = await ctx.runQuery(internal.logSinks.forEnvironment, { environmentId });
  if (!scope) throw new ConvexError("Environment not found");
  if (scope.sink?.kind !== "axiom") throw new ConvexError(NO_SINK);
  const { traces, ...logs } = scope.sink;
  return {
    logs,
    // The same sink pointed at its traces dataset; null on sinks from before traces.
    traces: traces ? { ...logs, dataset: traces } : null,
    serviceIds: scope.serviceIds,
  };
}

const fail = (err: unknown): never => {
  throw new ConvexError(err instanceof Error ? err.message : String(err));
};

/**
 * Request rate, errors and latency over `range`, and the latest requests, of the environment's
 * services or of one of them (`nodeId`, for `keel traces <service>`).
 */
export const overview = action({
  args: {
    environmentId: v.id("environments"),
    range: timeRange,
    search: v.optional(v.string()),
    nodeId: v.optional(v.id("nodes")),
  },
  handler: async (ctx, { environmentId, range, search = "", nodeId }): Promise<TraceOverview> => {
    const { traces, serviceIds } = await axiomScope(ctx, environmentId);
    if (!traces) throw new ConvexError(NO_TRACES);
    if (nodeId && !serviceIds.includes(nodeId)) throw new ConvexError("Node not found");
    const ids = nodeId ? [nodeId] : serviceIds;
    return axiomTraceOverview(traces, ids, range, search.slice(0, 200)).catch(fail);
  },
});

/** Slack around a trace's spans when looking for its log lines (clocks, buffered writes). */
const LOG_SLACK_MS = 5_000;
/** Without spans, how far either side of `at` a trace's log lines are looked for. */
const LOG_WINDOW_MS = 15 * 60_000;

/**
 * Every span of one trace, plus the environment's log lines that name its id. `at`: a moment
 * inside the trace, if known (its root's start, or the time of the log line it was opened from).
 */
export const get = action({
  args: {
    environmentId: v.id("environments"),
    traceId: v.string(),
    at: v.optional(v.number()),
  },
  handler: async (ctx, { environmentId, traceId, at }): Promise<Trace> => {
    if (!TRACE_ID_RE.test(traceId)) throw new ConvexError("Not a trace id");
    const id = traceId.toLowerCase();
    const { logs, traces, serviceIds } = await axiomScope(ctx, environmentId);
    try {
      const spans = traces ? await axiomTraceSpans(traces, id, at) : [];
      // The lines can only have been written while the trace ran: look there when its spans say
      // when that was (stretched to `at`, so the line it was opened from is always found, even
      // logged after the root ended or with clock skew), else around `at`, else over the last
      // week like the spans.
      const from = spans.length
        ? Math.min(...spans.map((s) => s.start), at ?? Infinity) - LOG_SLACK_MS
        : at
          ? at - LOG_WINDOW_MS
          : Date.now() - 7 * 24 * 60 * 60_000;
      const to = spans.length
        ? Math.max(...spans.map((s) => s.start + s.duration), at ?? -Infinity) + LOG_SLACK_MS
        : at
          ? at + LOG_WINDOW_MS
          : undefined;
      const lines = await axiomLines(logs, serviceIds, {
        n: 500,
        search: id,
        from,
        to,
        oldestFirst: true,
      });
      return { source: "axiom", traceId: id, spans, logs: lines };
    } catch (err) {
      return fail(err);
    }
  },
});

const AROUND_MS = 30_000;

/** Requests that started within 30s either side of `at`: the traces near a log line. */
export const around = action({
  args: { environmentId: v.id("environments"), at: v.number() },
  handler: async (ctx, { environmentId, at }): Promise<TraceSummary[]> => {
    const { traces, serviceIds } = await axiomScope(ctx, environmentId);
    if (!traces) return [];
    return axiomRequestsBetween(traces, serviceIds, at - AROUND_MS, at + AROUND_MS).catch(fail);
  },
});
