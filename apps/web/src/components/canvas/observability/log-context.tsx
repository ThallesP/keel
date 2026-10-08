import { ArrowLeft } from "lucide-react";
import { useMemo } from "react";

import { useListLogsAround, useListTracesAround } from "@/api/gen";
import type { ProjectLine, TraceSummary } from "@/api/types";
import { stripAnsi } from "@/lib/ansi";
import { errorMessage } from "@/lib/api";

import { useEnvironment } from "../environment";
import { formatDuration, formatLogTime, formatTimestamp } from "../format";
import { SectionLabel } from "../primitives";
import { useServices } from "./chrome";
import { EventStream, lineEvent, requestEvent, type StreamEvent } from "./stream";
import { LineDetail } from "./trace";

/**
 * A log line that names no trace, full screen: every service's lines from 30s either side of it
 * (ClickStack's "surrounding context"), oldest first with the line highlighted, and the requests
 * that started in that minute, each one click from its trace. Without a trace id, time is the
 * only link between a line and the requests it belongs to.
 */
export function LogContext({
  at,
  focus,
  onBack,
  onOpen,
}: {
  at: number;
  /** The line it was opened from; from a link, the first line at `at` stands in. */
  focus?: ProjectLine;
  onBack: () => void;
  onOpen: (event: StreamEvent) => void;
}) {
  const { environmentId } = useEnvironment();
  const services = useServices();
  // Once per (environment, at): what was around a moment does not change.
  const once = { query: { staleTime: Infinity, meta: { realtime: false } } };
  const request = { path: { id: environmentId }, query: { at } };
  const linesAround = useListLogsAround(request, once);
  const requestsAround = useListTracesAround(request, once);
  // Either failing fails the view.
  const failure = linesAround.error ?? requestsAround.error;
  const error = failure ? errorMessage(failure) : null;
  const data = useMemo(
    () =>
      linesAround.data === undefined || requestsAround.data === undefined
        ? null
        : { lines: linesAround.data ?? [], requests: requestsAround.data ?? [] },
    [linesAround.data, requestsAround.data],
  );

  const events = useMemo(() => data?.lines.map(lineEvent) ?? [], [data]);
  const focused = events.find(
    (e) =>
      e.kind === "log" &&
      (focus ? e.line.time === focus.time && e.line.text === focus.text : e.time === at),
  );
  const line = focused?.kind === "log" ? focused.line : focus;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 flex-col border-b border-line px-5 pt-3 pb-3">
        <button
          type="button"
          onClick={onBack}
          className="flex w-fit items-center gap-1 text-2xs text-muted-foreground hover:text-ink"
        >
          <ArrowLeft size={12} strokeWidth={1.6} aria-hidden />
          All events
        </button>
        <h2 className="mt-2 truncate font-mono text-sm font-medium text-ink">
          {line ? stripAnsi(line.text) : `Logs around ${formatTimestamp(at)}`}
        </h2>
        <p className="mt-0.5 font-mono text-2xs text-faint">
          {[
            formatLogTime(at),
            line && services.ofLine(line.serviceId).text,
            line?.stream,
            "names no trace: lines and requests from 30s either side",
          ]
            .filter(Boolean)
            .join(" · ")}
        </p>
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="min-w-0 flex-1 overflow-auto py-2">
          {error ? (
            <p className="px-5 text-xs text-danger">{error}</p>
          ) : data === null ? (
            <p className="px-5 text-xs text-faint">Loading…</p>
          ) : (
            <EventStream events={events} onOpen={onOpen} focusKey={focused?.key} />
          )}
        </div>
        <aside className="flex w-[360px] shrink-0 flex-col overflow-auto border-l border-line">
          {line && <LineDetail line={line} />}
          <section className="flex flex-col gap-2 px-4 py-3">
            <SectionLabel>Requests within 30s</SectionLabel>
            {data === null ? null : data.requests.length === 0 ? (
              <p className="text-2xs text-faint">No requests started in that minute.</p>
            ) : (
              <div className="-mx-4 flex flex-col">
                {data.requests
                  .slice()
                  .sort((a, b) => a.start - b.start)
                  .map((t) => (
                    <RequestRow key={t.traceId} trace={t} onOpen={() => onOpen(requestEvent(t))} />
                  ))}
              </div>
            )}
          </section>
        </aside>
      </div>
    </div>
  );
}

function RequestRow({ trace, onOpen }: { trace: TraceSummary; onOpen: () => void }) {
  const failed = trace.error || trace.errors > 0;
  return (
    <button
      type="button"
      onClick={onOpen}
      title="Open trace"
      className="flex h-7 items-center gap-2.5 px-4 text-left text-xs hover:bg-surface-2"
    >
      <span className="shrink-0 font-mono text-2xs text-faint tabular-nums">
        {formatLogTime(trace.start)}
      </span>
      {failed && <span className="size-1.5 shrink-0 rounded-full bg-danger" aria-label="error" />}
      <span className="min-w-0 flex-1 truncate text-ink">{trace.name || "(unnamed)"}</span>
      <span className="shrink-0 font-mono text-2xs text-muted-foreground tabular-nums">
        {formatDuration(trace.duration)}
      </span>
    </button>
  );
}
