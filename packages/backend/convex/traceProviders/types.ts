// Shared shape of what the Traces tab renders, whatever store backs it. Every trace provider
// (Axiom today, later ClickHouse) parses its OpenTelemetry spans into this; the tab never knows
// which one answered. Times are epoch milliseconds and durations milliseconds, both fractional:
// spans are often well under a millisecond and the waterfall needs that precision.

export type TraceSource = "axiom";

export type TraceRange = "15m" | "1h" | "24h" | "7d";

export type SpanStatus = "ok" | "error" | "unset";

/** One OTel attribute, value rendered as text. Lists are sorted by key. */
export type Attribute = [key: string, value: string];

export type SpanEvent = { time: number; name: string; attributes: Attribute[] };

export type Span = {
  spanId: string;
  /** "" for a root span. */
  parentId: string;
  name: string;
  /** OTel `service.name`. */
  service: string;
  /** server | client | internal | producer | consumer, or "" when unset. */
  kind: string;
  start: number;
  duration: number;
  status: SpanStatus;
  statusMessage: string;
  /** Instrumentation scope (library) that recorded the span. */
  scope: string;
  attributes: Attribute[];
  resource: Attribute[];
  events: SpanEvent[];
};

/** A trace in the list: its root span, plus counts over every span of the trace. */
export type TraceSummary = {
  traceId: string;
  name: string;
  service: string;
  kind: string;
  start: number;
  duration: number;
  /** HTTP response status of the root span, when it has one. */
  httpStatus: number | null;
  spans: number;
  errors: number;
  error: boolean;
};

/** Root spans ("requests") in one time bucket. Percentiles are null in an empty bucket. */
export type TraceBucket = {
  time: number;
  requests: number;
  errors: number;
  p50: number | null;
  p95: number | null;
  p99: number | null;
};

export type TraceStats = Omit<TraceBucket, "time">;

export type TraceOverview = {
  source: TraceSource;
  /** [from, to) of the range the numbers cover. */
  from: number;
  to: number;
  bucketMs: number;
  stats: TraceStats;
  /** Every bucket of the range, oldest first, empty ones included. */
  buckets: TraceBucket[];
  /** Most recent root spans first. */
  traces: TraceSummary[];
};

export type Trace = { source: TraceSource; traceId: string; spans: Span[] };
