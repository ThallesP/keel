import { cn } from "@my-better-t-app/ui/lib/utils";
import { ArrowLeft } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";

import { type Trace as WireTrace, useGetTrace } from "@/api/gen";
import {
  type Attribute,
  type ProjectLine,
  type Span as WireSpan,
  type SpanEvent as WireSpanEvent,
  asTrace,
} from "@/api/types";
import { AnsiText } from "@/lib/ansi";
import { errorMessage } from "@/lib/api";

import { useEnvironment } from "../environment";
import { formatDuration, formatLogTime, formatTimestamp } from "../format";
import { SectionLabel } from "../primitives";
import { type ServiceLabel, useServices } from "./chrome";
import { lineFields, traceRef } from "./correlate";

/**
 * One trace, full screen: its spans and its log lines in one waterfall (ClickStack-style). Spans
 * nest under their parent; a line nests under the span it names, else under the root, among the
 * span's children by time. Spans are bars on the trace's timeline, lines are points. The selected
 * row's details (attributes, events, resource; or the line and its fields) are on the right.
 * Error spans get the danger tone plus a dot, never colour alone.
 */

// The wire's lists may be null (Go nil slices); the view reads them as empty lists.
type SpanEvent = Omit<WireSpanEvent, "attributes"> & { attributes: Attribute[] };
type Span = Omit<WireSpan, "attributes" | "resource" | "events"> & {
  attributes: Attribute[];
  resource: Attribute[];
  events: SpanEvent[];
};
type Trace = { traceId: string; spans: Span[]; logs: ProjectLine[] };

/** `GET /api/environments/{id}/traces/{traceId}` as the view reads it (a query `select`). */
function toTrace(wire: WireTrace): Trace {
  // asTrace: attributes typed as [key, value] pairs.
  const spans = asTrace(wire)?.spans ?? [];
  return {
    traceId: wire.traceId,
    logs: wire.logs ?? [],
    spans: spans.map((span) => ({
      ...span,
      attributes: span.attributes ?? [],
      resource: span.resource ?? [],
      events: (span.events ?? []).map((e) => ({ ...e, attributes: e.attributes ?? [] })),
    })),
  };
}

type Item =
  | { kind: "span"; key: string; time: number; span: Span }
  | { kind: "log"; key: string; time: number; line: ProjectLine };

type Row = { item: Item; depth: number };

/** Depth-first under each root, children (spans and lines) by time. */
function tree(spans: Span[], logs: ProjectLine[]): Row[] {
  const ids = new Set(spans.map((s) => s.spanId));
  const children = new Map<string, Item[]>();
  const roots: Item[] = [];
  const add = (parent: string | null, item: Item) => {
    if (parent === null) return void roots.push(item);
    const list = children.get(parent) ?? [];
    list.push(item);
    children.set(parent, list);
  };
  for (const span of spans) {
    const parented = span.parentId && span.parentId !== span.spanId && ids.has(span.parentId);
    add(parented ? span.parentId : null, {
      kind: "span",
      key: `span:${span.spanId}`,
      time: span.start,
      span,
    });
  }
  // Lines go under their span; a line naming no known span under the first root span.
  const firstRoot = spans.find((s) => !s.parentId || !ids.has(s.parentId))?.spanId ?? null;
  logs.forEach((line, i) => {
    const spanId = traceRef(line.text)?.spanId;
    const parent = spanId && ids.has(spanId) ? spanId : firstRoot;
    add(parent, {
      kind: "log",
      key: `log:${line.time}:${line.serviceId}:${i}`,
      time: line.time,
      line,
    });
  });

  const byTime = (a: Item, b: Item) => a.time - b.time;
  const rows: Row[] = [];
  const seen = new Set<string>();
  const walk = (item: Item, depth: number) => {
    if (seen.has(item.key)) return;
    seen.add(item.key);
    rows.push({ item, depth });
    if (item.kind === "span") {
      for (const child of (children.get(item.span.spanId) ?? []).sort(byTime))
        walk(child, depth + 1);
    }
  };
  for (const root of roots.sort(byTime)) walk(root, 0);
  // A parent cycle is unreachable from any root; still show those spans.
  for (const list of children.values()) for (const item of list) walk(item, 0);
  return rows;
}

const itemEnd = (item: Item) =>
  item.kind === "span" ? item.span.start + item.span.duration : item.time;

export function TraceDetail({
  traceId,
  at,
  focus,
  onBack,
}: {
  traceId: string;
  /** A moment inside the trace, when known: narrows the lookup. */
  at?: number;
  /** The log line it was opened from: selected first. */
  focus?: ProjectLine;
  onBack: () => void;
}) {
  const { environmentId } = useEnvironment();
  // Once per open: no refetch while the view is open, and nothing kept after it closes (gcTime 0).
  // A trace opened before the SDK's batch exporter flushed (about 5 s) would otherwise show its
  // partial answer on every reopen for minutes, where the old view asked again on each open.
  const lookup = useGetTrace(
    { path: { id: environmentId, traceId }, query: at === undefined ? undefined : { at } },
    { query: { staleTime: Infinity, gcTime: 0, meta: { realtime: false }, select: toTrace } },
  );
  const trace = lookup.data ?? null;
  const error = lookup.error ? errorMessage(lookup.error) : null;
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
  // The timeline is the spans' when there are any: a line's clock (the app's, through Docker) is
  // not the SDK's, so a line written as a request began can land a hair before its root span.
  // Lines outside it sit on its edge.
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
  onSelect: (key: string) => void;
}) {
  const services = useServices();
  return (
    <div className="min-w-0 flex-1 overflow-auto">
      <div className="sticky top-0 z-10 flex h-8 items-center border-b border-line bg-bg">
        <span className={cn(NAME_COL, "pl-5")}>
          <SectionLabel>Spans and logs</SectionLabel>
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
      {rows.map(({ item, depth }) => {
        const isSelected = item.key === selected;
        return (
          <button
            key={item.key}
            type="button"
            aria-current={isSelected ? "true" : undefined}
            onClick={() => onSelect(item.key)}
            className={cn(
              "flex h-7 w-full items-center text-left text-xs",
              isSelected ? "bg-primary-soft" : "hover:bg-surface-2",
            )}
          >
            <span
              className={cn(NAME_COL, "flex min-w-0 items-center gap-2 pr-3")}
              style={{ paddingLeft: 20 + depth * 14 }}
            >
              {item.kind === "span" ? (
                <SpanName span={item.span} service={services.ofSpan(item.span.service)} />
              ) : (
                <LineName line={item.line} service={services.ofLine(item.line.serviceId)} />
              )}
            </span>
            <span className={cn("relative mr-5 h-full flex-1", QUARTERS)}>
              {item.kind === "span" ? (
                <SpanBar span={item.span} start={start} total={total} />
              ) : (
                <LinePoint line={item.line} start={start} total={total} />
              )}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function SpanName({ span, service }: { span: Span; service: ServiceLabel }) {
  return (
    <>
      {span.status === "error" && (
        <span className="size-1.5 shrink-0 rounded-full bg-danger" aria-label="error" />
      )}
      <span className={cn("max-w-[45%] shrink-0 truncate font-mono text-2xs", service.tone)}>
        {service.text}
      </span>
      <span className="truncate text-ink">{span.name || "(unnamed)"}</span>
    </>
  );
}

function LineName({ line, service }: { line: ProjectLine; service: ServiceLabel }) {
  return (
    <>
      <span className="shrink-0 rounded-sm bg-surface-2 px-1 font-mono text-[10px] text-muted-foreground">
        log
      </span>
      <span className={cn("max-w-[30%] shrink-0 truncate font-mono text-2xs", service.tone)}>
        {service.text}
      </span>
      <span
        className={cn(
          "truncate font-mono text-2xs",
          line.stream === "stderr" ? "text-warning" : "text-muted-foreground",
        )}
      >
        <AnsiText text={line.text} />
      </span>
    </>
  );
}

function SpanBar({ span, start, total }: { span: Span; start: number; total: number }) {
  const left = ((span.start - start) / total) * 100;
  const width = (span.duration / total) * 100;
  const failed = span.status === "error";
  // Duration label after the bar; near the right edge, inside a bar wide enough to hold it,
  // else before the bar.
  const place = left + width <= 80 ? "after" : width >= 20 ? "inside" : "before";
  return (
    <>
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
    </>
  );
}

/** A line is a moment: an 8px point on the timeline, ringed so it reads over a bar's end. */
function LinePoint({ line, start, total }: { line: ProjectLine; start: number; total: number }) {
  return (
    <span
      className={cn(
        "absolute top-1/2 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-bg",
        line.stream === "stderr" ? "bg-warning" : "bg-muted-foreground",
      )}
      style={{ left: `${Math.min(100, Math.max(0, ((line.time - start) / total) * 100))}%` }}
    />
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

/** A log line: the whole text, when it was written, and its fields when it is structured. */
export function LineDetail({ line, traceStart }: { line: ProjectLine; traceStart?: number }) {
  const services = useServices();
  const fields = lineFields(line.text);
  const facts: Attribute[] = [["time", formatLogTime(line.time)]];
  if (traceStart !== undefined) {
    facts.push(["in trace", `+${formatDuration(Math.max(0, line.time - traceStart))}`]);
  }
  facts.push(["task", line.task || "—"]);
  return (
    <div className="flex flex-col gap-5 px-4 py-3">
      <div>
        <div className="text-sm font-semibold text-ink">Log line</div>
        <div className="mt-0.5 text-2xs text-muted-foreground">
          {services.ofLine(line.serviceId).text} · {line.stream}
        </div>
      </div>
      <p
        className={cn(
          "rounded-md bg-surface-2 px-2.5 py-2 font-mono text-2xs break-all whitespace-pre-wrap",
          line.stream === "stderr" ? "text-warning" : "text-ink",
        )}
      >
        <AnsiText text={line.text} />
      </p>
      <Pairs pairs={facts} />
      {fields.length > 0 && (
        <Section title="Fields">
          <Pairs pairs={fields} />
        </Section>
      )}
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
      {pairs.map(([k, v], i) => (
        <div key={`${k}:${i}`} className="contents">
          <dt className="break-all text-faint">{k}</dt>
          <dd className="break-all whitespace-pre-wrap text-ink">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
