import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Attribute, Span, Trace } from "@my-better-t-app/backend/convex/traces";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useAction } from "convex/react";
import { ArrowLeft } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";

import { useEnvironment } from "../environment";
import { errorMessage } from "../errors";
import { formatDuration, formatTimestamp } from "../format";
import { SectionLabel } from "../primitives";

/**
 * One trace: every span as a waterfall row (indented under its parent, bar placed on the trace's
 * timeline) and the selected span's attributes, resource and events on the right. Error spans
 * get the danger tone plus a dot, never colour alone.
 */

type Row = { span: Span; depth: number };

/** Depth-first under each root, siblings by start. A span whose parent is missing is a root. */
function tree(spans: Span[]): Row[] {
  const ids = new Set(spans.map((s) => s.spanId));
  const children = new Map<string, Span[]>();
  const roots: Span[] = [];
  for (const s of spans) {
    if (s.parentId && s.parentId !== s.spanId && ids.has(s.parentId)) {
      const list = children.get(s.parentId) ?? [];
      list.push(s);
      children.set(s.parentId, list);
    } else {
      roots.push(s);
    }
  }
  const byStart = (a: Span, b: Span) => a.start - b.start;
  const rows: Row[] = [];
  const seen = new Set<string>();
  const walk = (span: Span, depth: number) => {
    if (seen.has(span.spanId)) return;
    seen.add(span.spanId);
    rows.push({ span, depth });
    for (const child of (children.get(span.spanId) ?? []).sort(byStart)) walk(child, depth + 1);
  };
  for (const root of roots.sort(byStart)) walk(root, 0);
  // A parent cycle is unreachable from any root; still show those spans.
  for (const s of spans) if (!seen.has(s.spanId)) rows.push({ span: s, depth: 0 });
  return rows;
}

export function TraceDetail({
  traceId,
  at,
  onBack,
}: {
  traceId: string;
  at?: number;
  onBack: () => void;
}) {
  const { environmentId } = useEnvironment();
  const get = useAction(api.traces.get);
  const [trace, setTrace] = useState<Trace | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setTrace(null);
    setError(null);
    setSelected(null);
    get({ environmentId, traceId, at })
      .then((t) => !cancelled && setTrace(t))
      .catch((err) => !cancelled && setError(errorMessage(err)));
    return () => {
      cancelled = true;
    };
  }, [environmentId, traceId, at, get]);

  const rows = useMemo(() => (trace ? tree(trace.spans) : []), [trace]);

  const back = (
    <button
      type="button"
      onClick={onBack}
      className="flex items-center gap-1 text-2xs text-muted-foreground hover:text-ink"
    >
      <ArrowLeft size={12} strokeWidth={1.6} aria-hidden />
      All traces
    </button>
  );
  if (error || !trace || rows.length === 0) {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 px-5 py-3">
        {back}
        <p className={cn("text-xs", error ? "text-danger" : "text-faint")}>
          {error ?? (trace ? `No spans for trace ${traceId} in the last week.` : "Loading trace…")}
        </p>
      </div>
    );
  }

  const start = Math.min(...rows.map((r) => r.span.start));
  const end = Math.max(...rows.map((r) => r.span.start + r.span.duration));
  const total = Math.max(end - start, 0.001);
  const services = [...new Set(rows.map((r) => r.span.service).filter(Boolean))];
  const errors = rows.filter((r) => r.span.status === "error").length;
  const root = rows[0]!.span;
  const current = rows.find((r) => r.span.spanId === selected)?.span ?? root;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-end justify-between gap-6 border-b border-line px-5 pt-3 pb-3">
        <div className="min-w-0">
          {back}
          <h2 className="mt-2 truncate text-md font-semibold tracking-[-0.02em] text-ink">
            {root.name || "(unnamed)"}
          </h2>
          <p className="mt-0.5 flex items-center gap-1.5 font-mono text-2xs text-faint">
            {errors > 0 && <span className="size-1.5 rounded-full bg-danger" />}
            {[
              formatTimestamp(start),
              formatDuration(total),
              `${rows.length} ${rows.length === 1 ? "span" : "spans"}`,
              services.join(", "),
              errors > 0 && `${errors} ${errors === 1 ? "error" : "errors"}`,
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
          selected={current.spanId}
          onSelect={setSelected}
        />
        <aside className="w-[360px] shrink-0 overflow-auto border-l border-line">
          <SpanDetail span={current} traceStart={start} />
        </aside>
      </div>
    </div>
  );
}

const NAME_COL = "w-[38%] min-w-[240px] max-w-[440px] shrink-0";
// Hairline at every quarter of the timeline, behind the bars.
const QUARTERS =
  "bg-[linear-gradient(to_right,var(--color-line)_1px,transparent_1px)] bg-[length:25%_100%]";

function Waterfall({
  rows,
  start,
  total,
  selected,
  onSelect,
}: {
  rows: Row[];
  start: number;
  total: number;
  selected: string;
  onSelect: (spanId: string) => void;
}) {
  return (
    <div className="min-w-0 flex-1 overflow-auto">
      <div className="sticky top-0 z-10 flex h-8 items-center border-b border-line bg-bg">
        <span className={cn(NAME_COL, "pl-5")}>
          <SectionLabel>Span</SectionLabel>
        </span>
        <span className="relative mr-5 h-full flex-1">
          {[0, 0.25, 0.5, 0.75, 1].map((q) => (
            <span
              key={q}
              className={cn(
                "absolute top-1/2 -translate-y-1/2 font-mono text-2xs text-faint tabular-nums",
                q === 1 ? "-translate-x-full" : q > 0 && "-translate-x-1/2",
              )}
              style={{ left: `${q * 100}%` }}
            >
              {q === 0 ? "0" : formatDuration(total * q)}
            </span>
          ))}
        </span>
      </div>
      {rows.map(({ span, depth }) => {
        const left = ((span.start - start) / total) * 100;
        const width = (span.duration / total) * 100;
        const failed = span.status === "error";
        // Duration label after the bar; near the right edge, inside a bar wide enough to hold
        // it, else before the bar.
        const place = left + width <= 80 ? "after" : width >= 20 ? "inside" : "before";
        return (
          <button
            key={span.spanId}
            type="button"
            aria-current={span.spanId === selected ? "true" : undefined}
            onClick={() => onSelect(span.spanId)}
            className={cn(
              "flex h-7 w-full items-center text-left text-xs",
              span.spanId === selected ? "bg-primary-soft" : "hover:bg-surface-2",
            )}
          >
            <span
              className={cn(NAME_COL, "flex min-w-0 items-center gap-2 pr-3")}
              style={{ paddingLeft: 20 + depth * 14 }}
            >
              {failed && (
                <span className="size-1.5 shrink-0 rounded-full bg-danger" aria-label="error" />
              )}
              <span className="max-w-[45%] shrink-0 truncate font-mono text-2xs text-faint">
                {span.service}
              </span>
              <span className="truncate text-ink">{span.name || "(unnamed)"}</span>
            </span>
            <span className={cn("relative mr-5 h-full flex-1", QUARTERS)}>
              <span
                className={cn(
                  "absolute top-1/2 h-2.5 -translate-y-1/2 rounded-sm",
                  failed ? "bg-danger" : "bg-primary",
                )}
                style={{ left: `${left}%`, width: `max(2px, ${width}%)` }}
              />
              <span
                className={cn(
                  "absolute top-1/2 -translate-y-1/2 font-mono text-2xs whitespace-nowrap tabular-nums",
                  place === "inside" ? "text-white" : "text-muted-foreground",
                )}
                style={
                  place === "after"
                    ? { left: `calc(${left + width}% + 6px)` }
                    : place === "inside"
                      ? { right: `calc(${100 - left - width}% + 6px)` }
                      : { right: `calc(${100 - left}% + 6px)` }
                }
              >
                {formatDuration(span.duration)}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}

function SpanDetail({ span, traceStart }: { span: Span; traceStart: number }) {
  const status = span.status === "error" ? "error" : span.status === "ok" ? "ok" : "unset";
  return (
    <div className="flex flex-col gap-5 px-4 py-3">
      <div>
        <div className="text-sm font-semibold break-words text-ink">{span.name || "(unnamed)"}</div>
        <div className="mt-0.5 text-2xs text-muted-foreground">
          {[span.service, span.kind].filter(Boolean).join(" · ")}
        </div>
      </div>
      <Pairs
        pairs={[
          ["duration", formatDuration(span.duration)],
          ["starts at", `+${formatDuration(span.start - traceStart)}`],
          ["status", status],
          ["span", span.spanId],
          ["parent", span.parentId || "—"],
          ["scope", span.scope || "—"],
        ]}
      />
      {span.statusMessage && (
        <p className="rounded-md bg-danger-soft px-2.5 py-2 font-mono text-2xs break-words whitespace-pre-wrap text-danger">
          {span.statusMessage}
        </p>
      )}
      <Section title="Attributes" empty="No attributes">
        {span.attributes.length > 0 && <Pairs pairs={span.attributes} />}
      </Section>
      {span.events.length > 0 && (
        <Section title="Events">
          {span.events.map((e, i) => (
            <div key={`${e.time}:${i}`} className="flex flex-col gap-1.5">
              <div className="flex items-baseline justify-between gap-3 text-xs">
                <span className="truncate text-ink">{e.name || "(unnamed)"}</span>
                <span className="shrink-0 font-mono text-2xs text-faint">
                  +{formatDuration(Math.max(0, e.time - traceStart))}
                </span>
              </div>
              {e.attributes.length > 0 && <Pairs pairs={e.attributes} />}
            </div>
          ))}
        </Section>
      )}
      <Section title="Resource" empty="No resource attributes">
        {span.resource.length > 0 && <Pairs pairs={span.resource} />}
      </Section>
    </div>
  );
}

function Section({
  title,
  empty,
  children,
}: {
  title: string;
  empty?: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-2">
      <SectionLabel>{title}</SectionLabel>
      {children || <p className="text-2xs text-faint">{empty}</p>}
    </section>
  );
}

/** Key / value list in mono; long values (stack traces, SQL) wrap instead of scrolling. */
function Pairs({ pairs }: { pairs: Attribute[] }) {
  return (
    <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-3 gap-y-1 font-mono text-2xs">
      {pairs.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="break-all text-faint">{k}</dt>
          <dd className="break-all whitespace-pre-wrap text-ink">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
