import { cn } from "@my-better-t-app/ui/lib/utils";
import { useEffect, useRef } from "react";

import type { EnvironmentLogLine, TraceSummary } from "@/gen/api";
import { AnsiText } from "@/lib/ansi";

import { formatDuration, formatLogTime } from "../format";
import { useServices } from "./chrome";
import { type TraceRef, traceRef } from "./correlate";

/**
 * The Observability page's one stream: log lines and requests (root spans) interleaved by time.
 * Every row opens something: a request its trace; a line the trace it names, or else the lines
 * around it. Mono 11px, `white-space: pre`, never wraps; scrolls sideways like any log.
 */

export type StreamEvent =
  | { kind: "request"; key: string; time: number; trace: TraceSummary }
  | { kind: "log"; key: string; time: number; line: EnvironmentLogLine; ref: TraceRef | null };

export const lineEvent = (line: EnvironmentLogLine, i: number): StreamEvent => ({
  kind: "log",
  key: `l:${line.time}:${line.serviceId}:${i}`,
  time: line.time,
  line,
  ref: traceRef(line.text),
});

export const requestEvent = (trace: TraceSummary): StreamEvent => ({
  kind: "request",
  key: `r:${trace.traceId}`,
  time: trace.start,
  trace,
});

/**
 * Newest first. Each list is the latest N of its kind, so a full list only covers back to its
 * oldest entry; past that the other kind would show alone and read as "nothing happened". The
 * merged stream stops at the later of the two cut-offs and says so (`since`).
 */
export function mergeEvents(
  lines: { items: EnvironmentLogLine[]; full: boolean } | null,
  requests: { items: TraceSummary[]; full: boolean } | null,
): { events: StreamEvent[]; since: number | null } {
  const cutoffs: number[] = [];
  if (lines?.full && lines.items.length) cutoffs.push(Math.min(...lines.items.map((l) => l.time)));
  if (requests?.full && requests.items.length) {
    cutoffs.push(Math.min(...requests.items.map((r) => r.start)));
  }
  const since = lines && requests && cutoffs.length ? Math.max(...cutoffs) : null;
  const events = [
    ...(lines?.items.map(lineEvent) ?? []),
    ...(requests?.items.map(requestEvent) ?? []),
  ]
    .filter((e) => since === null || e.time >= since)
    .sort((a, b) => b.time - a.time);
  return { events, since };
}

/** "14:02:07.123" today, "Oct 2 14:02:07.123" on an earlier day. */
function eventTime(ms: number) {
  const d = new Date(ms);
  const day =
    d.toDateString() === new Date().toDateString()
      ? ""
      : `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} `;
  return `${day}${formatLogTime(ms)}`;
}

function openTitle(e: StreamEvent) {
  if (e.kind === "request") return "Open trace";
  return e.ref ? "Open the trace this line names" : "Open the lines around this one";
}

export function EventStream({
  events,
  onOpen,
  focusKey,
}: {
  events: StreamEvent[];
  onOpen: (event: StreamEvent) => void;
  /** Highlighted and scrolled into view (the line a context view is about). */
  focusKey?: string;
}) {
  const services = useServices();
  const focusRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    focusRef.current?.scrollIntoView({ block: "center" });
  }, [focusKey]);

  return (
    <div className="overflow-x-auto font-mono text-2xs whitespace-pre">
      {events.map((e) => {
        const service =
          e.kind === "request"
            ? services.ofSpan(e.trace.service)
            : services.ofLine(e.line.serviceId);
        const focused = e.key === focusKey;
        return (
          <button
            key={e.key}
            ref={focused ? focusRef : undefined}
            type="button"
            onClick={() => onOpen(e)}
            title={openTitle(e)}
            className={cn(
              "flex h-[22px] w-max min-w-full items-center pr-5 text-left",
              focused ? "bg-primary-soft" : "hover:bg-surface-2",
            )}
          >
            <span className="w-[148px] shrink-0 pl-5 text-faint tabular-nums">
              {eventTime(e.time)}
            </span>
            <span className={cn("w-28 shrink-0 truncate pr-3", service.tone)}>{service.text}</span>
            <span className="w-12 shrink-0">
              {e.kind === "request" ? (
                <span className="rounded-sm bg-primary-soft px-1 text-[10px] text-primary">
                  req
                </span>
              ) : (
                e.ref && <span className="text-[10px] text-primary">trace</span>
              )}
            </span>
            {e.kind === "request" ? <RequestText trace={e.trace} /> : <LineText line={e.line} />}
          </button>
        );
      })}
    </div>
  );
}

function RequestText({ trace }: { trace: TraceSummary }) {
  const failed = trace.error || trace.errors > 0;
  const status = trace.httpStatus ?? (trace.error ? "error" : null);
  return (
    <span>
      <span className="text-ink">{trace.name || "(unnamed)"}</span>
      {trace.local && (
        <span
          className="ml-2 rounded-sm bg-surface-2 px-1 text-[10px] text-muted-foreground"
          title="From keel run on someone's machine, not a deploy"
        >
          local
        </span>
      )}
      {status !== null && (
        <span className="pl-3">
          {failed && (
            <span className="mr-1.5 inline-block size-1.5 rounded-full bg-danger align-middle" />
          )}
          <span className={failed ? "text-ink" : "text-muted-foreground"}>{status}</span>
        </span>
      )}
      <span className="pl-3 text-faint">{formatDuration(trace.duration)}</span>
      {trace.spans > 1 && <span className="pl-3 text-faint">{trace.spans} spans</span>}
    </span>
  );
}

function LineText({ line }: { line: EnvironmentLogLine }) {
  return (
    <span className={line.stream === "stderr" ? "text-warning" : "text-muted-foreground"}>
      <AnsiText text={line.text} />
    </span>
  );
}
