import { type AxiomConfig, query } from "../logProviders/axiom";
import { RANGES, rangeWindow, type TimeRange } from "../timeRange";
import type {
  Attribute,
  Span,
  SpanEvent,
  SpanStatus,
  TraceBucket,
  TraceOverview,
  TraceSummary,
} from "./types";

// Axiom trace provider, read side. Spans reach the sink's traces dataset over OTLP
// (`/v1/traces`, `X-Axiom-Dataset`), one row per span: trace_id, span_id, parent_span_id, name,
// kind, duration (nanoseconds), error, status.code, status.message, service.name, scope.name,
// attributes.* (with an `attributes.custom` map for anything outside the semantic conventions),
// resource.*, events. A field no span has carried yet does not exist in the dataset and APL
// rejects it with 400 "invalid field", so optional ones go through ensure_field.
//
// A "request" is a root span (no parent): rate, errors and latency count those only, as in
// Axiom's own trace dashboards. Aggregating every span would mix in each request's children.

const MIN = 60_000;
const HOUR = 60 * MIN;
const LIST = 100;
const MAX_SPANS = 2000;
/** How far back a trace is looked for when the caller does not say when it started. */
const TRACE_WINDOW_MS = 7 * 24 * HOUR;

export const TRACE_ID_RE = /^[0-9a-f]{16,32}$/i;

const ROOT = `where isempty(ensure_field("parent_span_id", typeof(string)))`;
// `error` is Axiom's flag; status.code is "ERROR" (or STATUS_CODE_ERROR) per OTel otherwise.
const FAILED = `ensure_field("error", typeof(bool)) == true or ensure_field("status.code", typeof(string)) contains "error"`;
const STATS = `requests = count(), errors = countif(failed), p50 = percentile(duration, 50), p95 = percentile(duration, 95), p99 = percentile(duration, 99)`;

type Row = Record<string, unknown>;

/** Like logProviders/axiom tailQuery: a dataset no span reached yet has no fields; that is "empty". */
async function spans(
  cfg: AxiomConfig,
  apl: string,
  sinceMs: number,
  untilMs?: number,
): Promise<Row[]> {
  try {
    return await query(cfg, apl, sinceMs, untilMs);
  } catch (err) {
    if (err instanceof Error && /Axiom 400.*invalid field/.test(err.message)) return [];
    throw err;
  }
}

/** APL string literal. */
const lit = (s: string) => `"${s.replace(/[\\"]/g, "\\$&")}"`;

/** Root spans, optionally only those whose name or service contains `search`. */
function roots(cfg: AxiomConfig, search: string) {
  const term = search.trim();
  const match = term
    ? ` | where name contains ${lit(term)} or ensure_field("service.name", typeof(string)) contains ${lit(term)}`
    : "";
  return `['${cfg.dataset}'] | ${ROOT}${match}`;
}

/**
 * The latest `limit` requests in [from, to), newest first, each with the span and error counts
 * of its whole trace.
 */
async function requests(
  cfg: AxiomConfig,
  search: string,
  from: number,
  to: number | undefined,
  limit: number,
): Promise<TraceSummary[]> {
  const latest = await spans(
    cfg,
    `${roots(cfg, search)} | sort by _time desc | limit ${limit}`,
    from,
    to,
  );
  const ids = [...new Set(latest.map((r) => str(r.trace_id)).filter((id) => TRACE_ID_RE.test(id)))];
  const perTrace = new Map<string, { spans: number; errors: number }>();
  if (ids.length > 0) {
    // A trace's other spans start after its root, so `from` holds them; `to` might not.
    const rows = await spans(
      cfg,
      `['${cfg.dataset}'] | where trace_id in (${ids.map(lit).join(", ")}) | extend failed = ${FAILED} | summarize spans = count(), errors = countif(failed) by trace_id`,
      from,
    );
    for (const r of rows) {
      perTrace.set(str(r.trace_id), { spans: num(r.spans), errors: num(r.errors) });
    }
  }
  return latest.map((r) => {
    const traceId = str(r.trace_id);
    const counts = perTrace.get(traceId);
    const error = statusOf(r) === "error";
    const http = Number(attr(r, "http.response.status_code") ?? attr(r, "http.status_code"));
    return {
      traceId,
      name: str(r.name),
      service: str(pick(r, "service.name")),
      kind: kindOf(r.kind),
      start: timeOf(r._time),
      duration: durationOf(r.duration),
      httpStatus: Number.isFinite(http) && http > 0 ? http : null,
      spans: counts?.spans ?? 1,
      errors: counts?.errors ?? (error ? 1 : 0),
      error,
    };
  });
}

/** Requests, errors and latency over the range, per bucket, and the latest requests. */
export async function axiomTraceOverview(
  cfg: AxiomConfig,
  range: TimeRange,
  search: string,
): Promise<TraceOverview> {
  const { bin, binMs } = RANGES[range];
  // Totals and series stop where the last bucket does, so the totals are the buckets' sum (a span
  // stamped ahead of this clock would otherwise count in the totals but in no bucket).
  const { from, to, count } = rangeWindow(range);
  const matching = roots(cfg, search);

  const [totals, series, traces] = await Promise.all([
    spans(cfg, `${matching} | extend failed = ${FAILED} | summarize ${STATS}`, from, to),
    spans(
      cfg,
      `${matching} | extend failed = ${FAILED} | summarize ${STATS} by bin(_time, ${bin})`,
      from,
      to,
    ),
    requests(cfg, search, from, undefined, LIST),
  ]);

  const byBucket = new Map<number, TraceBucket>();
  for (const r of series) {
    const time = Math.floor(timeOf(r._time ?? Object.values(r)[0]) / binMs) * binMs;
    byBucket.set(time, { time, ...statsOf(r) });
  }
  const buckets: TraceBucket[] = Array.from({ length: count }, (_, i) => {
    const time = from + i * binMs;
    return byBucket.get(time) ?? { time, requests: 0, errors: 0, p50: null, p95: null, p99: null };
  });

  const total = totals[0];
  return {
    source: "axiom",
    from,
    to,
    bucketMs: binMs,
    stats: total ? statsOf(total) : { requests: 0, errors: 0, p50: null, p95: null, p99: null },
    buckets,
    traces,
  };
}

/** Requests that started in [from, to), newest first: what was going on around a log line. */
export async function axiomRequestsBetween(cfg: AxiomConfig, from: number, to: number) {
  return requests(cfg, "", from, to, LIST);
}

/**
 * Every span of one trace, oldest first. `at` (a moment inside the trace, if known: its root's
 * start, or the time of a line it wrote) narrows the query window to the hour before it, so a
 * long trace opened from a late line still gets its root; without it the last week is searched.
 * The query is an equality on trace_id, so the wider window costs little.
 */
export async function axiomTraceSpans(
  cfg: AxiomConfig,
  traceId: string,
  at?: number,
): Promise<Span[]> {
  const since = at ? at - HOUR : Date.now() - TRACE_WINDOW_MS;
  const rows = await spans(
    cfg,
    `['${cfg.dataset}'] | where trace_id == ${lit(traceId)} | sort by _time asc | limit ${MAX_SPANS}`,
    since,
  );
  return rows.map(spanOf);
}

// ── Parsing ─────────────────────────────────────────────────────────────────────────────────
//
// Rows come back with every field of the dataset as a column, dotted names flat
// (`attributes.http.method`), maps as objects (`attributes.custom`). The helpers below also take
// nested objects and string-typed numbers, so a change in how Axiom serialises does not blank
// the page.

const str = (x: unknown) => (typeof x === "string" ? x : x == null ? "" : String(x));
const num = (x: unknown) => (typeof x === "number" ? x : Number(x) || 0);

/** `path` from a row: a flat dotted key, or the same path through nested objects/maps. */
function pick(obj: unknown, path: string): unknown {
  if (obj === null || typeof obj !== "object") return undefined;
  const o = obj as Row;
  if (path in o) return o[path];
  for (let i = path.indexOf("."); i > 0; i = path.indexOf(".", i + 1)) {
    const head = path.slice(0, i);
    if (head in o) {
      const found = pick(o[head], path.slice(i + 1));
      if (found !== undefined) return found;
    }
  }
  return undefined;
}

/** A span attribute, whether Axiom filed it under its semantic conventions or `custom`. */
const attr = (row: Row, name: string) =>
  pick(row, `attributes.${name}`) ?? pick(row, `attributes.custom.${name}`);

/** RFC3339 with up to nanoseconds (Date.parse keeps milliseconds), or epoch ns / µs / ms. */
function timeOf(x: unknown): number {
  if (typeof x === "number") return x > 1e17 ? x / 1e6 : x > 1e14 ? x / 1e3 : x;
  if (typeof x !== "string" || !x) return 0;
  if (/^\d+$/.test(x)) return timeOf(Number(x));
  const ms = Date.parse(x);
  if (Number.isNaN(ms)) return 0;
  const frac = /\.\d{3}(\d+)/.exec(x)?.[1];
  return frac ? ms + Number(`0.${frac}`) : ms;
}

const UNIT_MS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  µs: 1e-3,
  μs: 1e-3,
  ms: 1,
  s: 1000,
  m: MIN,
  h: HOUR,
};

/** Duration in ms from nanoseconds (Axiom's OTel `duration`), `1.5ms`-style or `hh:mm:ss.f`. */
function durationOf(x: unknown): number {
  if (typeof x === "number") return x / 1e6;
  if (typeof x !== "string" || !x) return 0;
  if (/^\d+(\.\d+)?$/.test(x)) return Number(x) / 1e6;
  let total = 0;
  let matched = false;
  for (const m of x.matchAll(/(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|h|m|s)/g)) {
    matched = true;
    total += Number(m[1]) * (UNIT_MS[m[2]!] ?? 0);
  }
  if (matched) return total;
  const t = /^(?:(\d+)\.)?(\d+):(\d+):(\d+(?:\.\d+)?)$/.exec(x);
  if (!t) return 0;
  const hours = Number(t[1] ?? 0) * 24 + Number(t[2]);
  return (hours * 3600 + Number(t[3]) * 60 + Number(t[4])) * 1000;
}

const msOrNull = (x: unknown) => (x == null || x === "" ? null : durationOf(x));

function statsOf(r: Row) {
  return {
    requests: num(r.requests),
    errors: num(r.errors),
    p50: msOrNull(r.p50),
    p95: msOrNull(r.p95),
    p99: msOrNull(r.p99),
  };
}

/** `SPAN_KIND_SERVER`, `Server`, `server` → `server`. */
function kindOf(x: unknown) {
  const kind = str(x)
    .toLowerCase()
    .replace(/^span_kind_/, "");
  return kind === "unspecified" ? "" : kind;
}

function statusOf(row: Row): SpanStatus {
  const code = str(pick(row, "status.code")).toLowerCase();
  if (pick(row, "error") === true || code.includes("error")) return "error";
  return code.includes("ok") ? "ok" : "unset";
}

const text = (x: unknown) =>
  typeof x === "string" ? x : typeof x === "object" ? JSON.stringify(x) : String(x);

/** Leaves of a value as `key.path` → text; objects recurse, arrays and scalars are leaves. */
function flatten(out: Map<string, string>, key: string, value: unknown) {
  if (value == null || value === "") return;
  if (typeof value === "object" && !Array.isArray(value)) {
    for (const [k, v] of Object.entries(value)) flatten(out, key ? `${key}.${k}` : k, v);
  } else if (key) {
    out.set(key, text(value));
  }
}

const sorted = (out: Map<string, string>): Attribute[] =>
  [...out].sort(([a], [b]) => a.localeCompare(b));

/** Everything under `attributes` or `resource`, with Axiom's `custom` map folded back in. */
function collect(row: Row, root: "attributes" | "resource"): Attribute[] {
  const out = new Map<string, string>();
  for (const [k, v] of Object.entries(row)) {
    if (k === root) flatten(out, "", v);
    else if (k.startsWith(`${root}.`)) flatten(out, k.slice(root.length + 1), v);
  }
  const unwrapped = new Map<string, string>();
  for (const [k, v] of out) unwrapped.set(k.replace(/^custom\./, ""), v);
  return sorted(unwrapped);
}

function eventsOf(x: unknown): SpanEvent[] {
  if (!Array.isArray(x)) return [];
  return x
    .filter((e): e is Row => e !== null && typeof e === "object")
    .map((e) => {
      const attributes = new Map<string, string>();
      flatten(attributes, "", e.attributes);
      return {
        time: timeOf(e.time ?? e.timestamp ?? e._time ?? e.timeUnixNano),
        name: str(e.name),
        attributes: sorted(attributes),
      };
    });
}

function spanOf(r: Row): Span {
  return {
    spanId: str(r.span_id),
    parentId: str(r.parent_span_id),
    name: str(r.name),
    service: str(pick(r, "service.name")),
    kind: kindOf(r.kind),
    start: timeOf(r._time),
    duration: durationOf(r.duration),
    status: statusOf(r),
    statusMessage: str(pick(r, "status.message")),
    scope: str(pick(r, "scope.name")),
    attributes: collect(r, "attributes"),
    resource: collect(r, "resource"),
    events: eventsOf(pick(r, "events")),
  };
}
