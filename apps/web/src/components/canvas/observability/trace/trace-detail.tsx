import { cn } from "@my-better-t-app/ui/lib/utils";
import { ArrowLeft } from "lucide-react";
import { useMemo, useState } from "react";

import { type EnvironmentLogLine, useGetTrace } from "@/gen/api";
import { errorMessage } from "@/lib/api";

import { useEnvironment } from "../../environment";
import { formatDuration, formatTimestamp } from "../../format";
import { LineDetail } from "./line-detail";
import { SpanDetail } from "./span-detail";
import { itemEnd, tree } from "./tree";
import { Waterfall } from "./waterfall";

export function TraceDetail({
  traceId,
  at,
  focus,
  onBack,
}: {
  traceId: string;
  at?: number;
  focus?: EnvironmentLogLine;
  onBack: () => void;
}) {
  const { environmentId } = useEnvironment();
  const { data: trace, error: failure } = useGetTrace(
    { path: { id: environmentId, traceId }, query: at === undefined ? undefined : { at } },
    { query: { staleTime: Infinity, gcTime: 0, meta: { realtime: false } } },
  );
  const error = failure ? errorMessage(failure) : null;
  const [selected, setSelected] = useState<string | null>(null);

  const rows = useMemo(() => (trace ? tree(trace.spans, trace.logs) : []), [trace]);

  const back = (
    <button
      type="button"
      onClick={onBack}
      className="flex items-center gap-1 text-2xs text-muted-foreground hover:text-ink"
    >
      <ArrowLeft size={12} strokeWidth={1.6} aria-hidden />
      All events
    </button>
  );
  if (error || !trace || rows.length === 0) {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 px-5 py-3">
        {back}
        <p className={cn("text-xs", error ? "text-danger" : "text-faint")}>
          {error ??
            (trace
              ? `Nothing for trace ${traceId}: no spans in the traces dataset and no log line naming it.`
              : "Loading trace…")}
        </p>
      </div>
    );
  }

  const items = rows.map((r) => r.item);
  const timed = items.some((i) => i.kind === "span")
    ? items.filter((i) => i.kind === "span")
    : items;
  const start = Math.min(...timed.map((i) => i.time));
  const end = Math.max(...timed.map(itemEnd));
  const total = Math.max(end - start, 0.001);
  const spanCount = trace.spans.length;
  const lineCount = trace.logs.length;
  const services = [...new Set(trace.spans.map((s) => s.service).filter(Boolean))];
  const errors = trace.spans.filter((s) => s.status === "error").length;
  const root = items.find((i) => i.kind === "span");
  const focusKey = focus
    ? items.find(
        (i) => i.kind === "log" && i.line.time === focus.time && i.line.text === focus.text,
      )?.key
    : undefined;
  const current = items.find((i) => i.key === (selected ?? focusKey)) ?? root ?? items[0]!;
  const title =
    root?.kind === "span" ? root.span.name || "(unnamed)" : `Trace ${traceId.slice(0, 8)}`;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-end justify-between gap-6 border-b border-line px-5 pt-3 pb-3">
        <div className="min-w-0">
          {back}
          <h2 className="mt-2 truncate text-md font-semibold tracking-[-0.02em] text-ink">
            {title}
          </h2>
          <p className="mt-0.5 flex items-center gap-1.5 font-mono text-2xs text-faint">
            {errors > 0 && <span className="size-1.5 rounded-full bg-danger" />}
            {[
              formatTimestamp(start),
              formatDuration(total),
              `${spanCount} ${spanCount === 1 ? "span" : "spans"}`,
              `${lineCount} ${lineCount === 1 ? "log line" : "log lines"}`,
              services.join(", "),
              errors > 0 && `${errors} ${errors === 1 ? "error" : "errors"}`,
              spanCount === 0 && "no spans in the traces dataset",
            ]
              .filter(Boolean)
              .join(" · ")}
          </p>
        </div>
        <span className="shrink-0 font-mono text-2xs text-faint select-all">{traceId}</span>
      </div>
      <div className="flex min-h-0 flex-1">
        <Waterfall
          rows={rows}
          start={start}
          total={total}
          selected={current.key}
          onSelect={setSelected}
        />
        <aside className="w-[360px] shrink-0 overflow-auto border-l border-line">
          {current.kind === "span" ? (
            <SpanDetail span={current.span} traceStart={start} />
          ) : (
            <LineDetail line={current.line} traceStart={start} />
          )}
        </aside>
      </div>
    </div>
  );
}
