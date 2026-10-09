import type { Attribute } from "@/api/gen";
import { stripAnsi } from "@/lib/ansi";

// Log ↔ trace correlation, read side. Container lines carry no trace context of their own, but an
// OpenTelemetry-instrumented logger writes the active trace and span into each line it emits:
// JSON (`"trace_id":"…"`, `"traceId"`, `"otelTraceID"`, `"trace.id"`), logfmt (`trace_id=…`) or a
// W3C `traceparent` (`00-<trace>-<span>-01`). Finding that id is all it takes: a trace's lines
// are the ones that contain it (traces.get), and a line opens the trace it names.

export type TraceRef = { traceId: string; spanId: string | null };

const KEY_END = String.raw`["']?\s*[:=]\s*["']?`;
const TRACE_KEY = new RegExp(
  String.raw`(?<![\w.-])(?:trace[_.-]?id|otelTraceID)${KEY_END}([0-9a-f]{32})(?![0-9a-f])`,
  "i",
);
const SPAN_KEY = new RegExp(
  String.raw`(?<![\w.-])(?:span[_.-]?id|otelSpanID)${KEY_END}([0-9a-f]{16})(?![0-9a-f])`,
  "i",
);
const TRACEPARENT = /\b00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}\b/i;
const ZERO = /^0+$/;

/** The trace (and span, when present) a log line names, or null. */
export function traceRef(line: string): TraceRef | null {
  // Colored loggers put codes between key and value (`trace_id ESC[2m=ESC[0m …`).
  const text = stripAnsi(line);
  const parent = TRACEPARENT.exec(text);
  const traceId = TRACE_KEY.exec(text)?.[1] ?? parent?.[1];
  if (!traceId || ZERO.test(traceId)) return null;
  const spanId = SPAN_KEY.exec(text)?.[1] ?? parent?.[2];
  return {
    traceId: traceId.toLowerCase(),
    spanId: spanId && !ZERO.test(spanId) ? spanId.toLowerCase() : null,
  };
}

const LOGFMT = /([\w.-]+)=("(?:[^"\\]|\\.)*"|\S+)/g;

/**
 * A structured line's fields, for the detail panel: a JSON object flattened to dotted keys, or
 * logfmt pairs (two or more). Anything else has no fields; the raw line says it all.
 */
export function lineFields(text: string): Attribute[] {
  const trimmed = stripAnsi(text).trim();
  if (trimmed.startsWith("{")) {
    try {
      const out: Attribute[] = [];
      flatten(out, "", JSON.parse(trimmed));
      return out;
    } catch {
      // Not JSON after all; try logfmt.
    }
  }
  const pairs = [...trimmed.matchAll(LOGFMT)].map(([, key, value]) => ({
    key: key!,
    value: value!.replace(/^"(.*)"$/s, "$1"),
  }));
  return pairs.length >= 2 ? pairs : [];
}

function flatten(out: Attribute[], key: string, value: unknown) {
  if (value !== null && typeof value === "object" && !Array.isArray(value)) {
    for (const [k, v] of Object.entries(value)) flatten(out, key ? `${key}.${k}` : k, v);
  } else if (key) {
    out.push({ key, value: typeof value === "string" ? value : JSON.stringify(value) });
  }
}
