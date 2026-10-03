import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import type {
  TraceOverview,
  TraceRange,
  TraceSummary,
} from "@my-better-t-app/backend/convex/traces";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useAction } from "convex/react";
import { useEffect, useState } from "react";

import { useEnvironment } from "../environment";
import { errorMessage } from "../errors";
import { formatDuration, formatTimestamp } from "../format";
import { SectionLabel } from "../primitives";
import { useDebounced } from "../use-debounced";
import { TracesReconnect } from "./axiom-gate";
import { type Hover, LatencyChart, RequestsChart, StatRow } from "./charts";
import { DisconnectButton, PageHeader, route, SearchField, type Sink, viaAxiom } from "./chrome";
import { TraceDetail } from "./trace";

/**
 * Traces tab: request rate, errors and latency of the project's OpenTelemetry traces, the latest
 * requests, and one trace as a waterfall (`&trace=<id>`). A request is a root span. Reads the
 * sink's traces dataset (convex/traces.ts); whatever exports spans there shows up.
 */

const RANGES: { id: TraceRange; label: string; long: string }[] = [
  { id: "15m", label: "15m", long: "15 minutes" },
  { id: "1h", label: "1h", long: "hour" },
  { id: "24h", label: "24h", long: "24 hours" },
  { id: "7d", label: "7d", long: "7 days" },
];
const POLL_MS = 15_000;

export function TracesView({ sink }: { sink: Sink }) {
  const { trace } = route.useSearch();
  const navigate = route.useNavigate();
  const [range, setRange] = useState<TraceRange>("1h");
  const [search, setSearch] = useState("");
  // When the opened trace started, if it was opened from the list: narrows its lookup.
  const [at, setAt] = useState<number>();

  if (!sink.traces) {
    return (
      <div className="flex h-full flex-col bg-bg">
        <PageHeader tab="traces" meta={viaAxiom(sink, sink.dataset)}>
          <DisconnectButton />
        </PageHeader>
        <TracesReconnect />
      </div>
    );
  }

  const open = (t: TraceSummary) => {
    setAt(t.start);
    void navigate({ search: (prev) => ({ ...prev, trace: t.traceId }) });
  };
  const close = () => {
    setAt(undefined);
    void navigate({ search: (prev) => ({ ...prev, trace: undefined }) });
  };

  return (
    <div className="flex h-full flex-col bg-bg">
      <PageHeader tab="traces" meta={viaAxiom(sink, sink.traces)}>
        {!trace && (
          <>
            <SearchField
              value={search}
              onChange={setSearch}
              placeholder="Filter by name or service"
            />
            <RangePicker value={range} onChange={setRange} />
          </>
        )}
        <DisconnectButton />
      </PageHeader>
      {trace && <TraceDetail traceId={trace} at={at} onBack={close} />}
      {/* Stays mounted under an open trace, so going back keeps the list and the scroll. */}
      <Overview
        hidden={Boolean(trace)}
        sink={sink}
        dataset={sink.traces}
        range={range}
        search={search}
        onOpen={open}
      />
    </div>
  );
}

function RangePicker({
  value,
  onChange,
}: {
  value: TraceRange;
  onChange: (range: TraceRange) => void;
}) {
  return (
    <span className="flex h-7 items-center rounded-md border border-line p-0.5" role="group">
      {RANGES.map((r) => (
        <button
          key={r.id}
          type="button"
          aria-pressed={r.id === value}
          aria-label={`Last ${r.long}`}
          onClick={() => onChange(r.id)}
          className={cn(
            "h-full rounded-sm px-2 font-mono text-2xs",
            r.id === value ? "bg-surface-2 text-ink" : "text-muted-foreground hover:text-ink",
          )}
        >
          {r.label}
        </button>
      ))}
    </span>
  );
}

/**
 * Polled while visible. A new range or filter keeps the previous numbers up, dimmed, until its
 * own arrive: no skeleton, no layout jump.
 */
function useOverview(
  environmentId: Id<"environments">,
  range: TraceRange,
  search: string,
  active: boolean,
) {
  const overview = useAction(api.traces.overview);
  const key = `${range}|${search}`;
  const [result, setResult] = useState<{ key: string; data: TraceOverview } | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    const load = async () => {
      try {
        const data = await overview({ environmentId, range, search });
        if (!cancelled) {
          setResult({ key, data });
          setError(null);
        }
      } catch (err) {
        if (!cancelled) setError(errorMessage(err));
      }
    };
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [environmentId, range, search, key, active, overview]);
  return { data: result?.data ?? null, stale: result !== null && result.key !== key, error };
}

function Overview({
  hidden,
  sink,
  dataset,
  range,
  search,
  onOpen,
}: {
  hidden: boolean;
  sink: Sink;
  dataset: string;
  range: TraceRange;
  search: string;
  onOpen: (t: TraceSummary) => void;
}) {
  const { environmentId } = useEnvironment();
  const filter = useDebounced(search.trim(), 300);
  const { data, stale, error } = useOverview(environmentId, range, filter, !hidden);
  const [hover, setHover] = useState<Hover>(null);
  const long = RANGES.find((r) => r.id === range)?.long ?? range;

  return (
    <div className={cn("min-h-0 flex-1 overflow-auto", hidden && "hidden")}>
      <div
        className={cn("flex flex-col gap-4 px-5 py-4 transition-opacity", stale && "opacity-60")}
      >
        {error && <p className="text-xs text-danger">{error}</p>}
        {data === null ? (
          !error && <p className="text-xs text-faint">Loading…</p>
        ) : data.stats.requests === 0 && data.traces.length === 0 ? (
          filter ? (
            <p className="text-xs text-faint">
              No requests match “{filter}” in the last {long}.
            </p>
          ) : (
            <NoTraces sink={sink} dataset={dataset} long={long} />
          )
        ) : (
          <>
            <StatRow stats={data.stats} rangeMs={data.to - data.from} />
            <div className="grid grid-cols-2 gap-3">
              <RequestsChart
                buckets={data.buckets}
                bucketMs={data.bucketMs}
                hover={hover}
                onHover={setHover}
              />
              <LatencyChart
                buckets={data.buckets}
                bucketMs={data.bucketMs}
                hover={hover}
                onHover={setHover}
              />
            </div>
            <TraceList traces={data.traces} onOpen={onOpen} />
          </>
        )}
      </div>
    </div>
  );
}

const COLUMNS =
  "grid grid-cols-[112px_minmax(0,1fr)_minmax(0,160px)_88px_56px_minmax(0,220px)] gap-4";

function TraceList({
  traces,
  onOpen,
}: {
  traces: TraceSummary[];
  onOpen: (t: TraceSummary) => void;
}) {
  const longest = Math.max(0, ...traces.map((t) => t.duration));
  return (
    <section className="flex flex-col">
      <div className={cn(COLUMNS, "h-8 items-center border-b border-line px-2")}>
        <SectionLabel>Time</SectionLabel>
        <SectionLabel>Request</SectionLabel>
        <SectionLabel>Service</SectionLabel>
        <SectionLabel>Status</SectionLabel>
        <SectionLabel className="text-right">Spans</SectionLabel>
        <SectionLabel>Duration</SectionLabel>
      </div>
      {traces.map((t) => (
        <button
          key={t.traceId}
          type="button"
          onClick={() => onOpen(t)}
          className={cn(
            COLUMNS,
            "h-9 items-center border-b border-line px-2 text-left text-xs hover:bg-surface-2",
          )}
        >
          <span className="font-mono text-2xs text-faint tabular-nums">
            {formatTimestamp(t.start)}
          </span>
          <span className="truncate text-ink">{t.name || "(unnamed)"}</span>
          <span className="truncate text-muted-foreground">{t.service}</span>
          <TraceStatus trace={t} />
          <span className="text-right font-mono text-2xs text-muted-foreground tabular-nums">
            {t.spans}
          </span>
          <span className="flex min-w-0 items-center gap-2.5">
            <span className="h-1.5 min-w-0 flex-1">
              <span
                className={cn(
                  "block h-full rounded-full",
                  t.error ? "bg-danger/55" : "bg-primary/35",
                )}
                style={{ width: `max(2px, ${longest ? (t.duration / longest) * 100 : 0}%)` }}
              />
            </span>
            <span className="w-14 shrink-0 text-right font-mono text-2xs text-ink tabular-nums">
              {formatDuration(t.duration)}
            </span>
          </span>
        </button>
      ))}
      <p className="pt-2 text-2xs text-faint">Latest {traces.length} requests in this range.</p>
    </section>
  );
}

/** HTTP status when the root span has one; an error is a red dot + label, never colour alone. */
function TraceStatus({ trace }: { trace: TraceSummary }) {
  const failed = trace.error || trace.errors > 0;
  const label = trace.httpStatus ?? (trace.error ? "error" : "ok");
  return (
    <span className="flex items-center gap-1.5 font-mono text-2xs">
      {failed && <span className="size-1.5 shrink-0 rounded-full bg-danger" />}
      <span className={failed ? "text-ink" : "text-muted-foreground"}>{label}</span>
      {!trace.error && trace.errors > 0 && <span className="text-faint">· {trace.errors} err</span>}
    </span>
  );
}

/** Nothing in range: how to get spans into the dataset until Keel forwards OTLP itself. */
function NoTraces({ sink, dataset, long }: { sink: Sink; dataset: string; long: string }) {
  const endpoint = sink.domain.includes("://") ? sink.domain : `https://${sink.domain}`;
  const env = [
    `OTEL_EXPORTER_OTLP_ENDPOINT=${endpoint}`,
    "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
    `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<axiom token>,X-Axiom-Dataset=${dataset}`,
    "OTEL_SERVICE_NAME=<service>",
  ].join("\n");
  return (
    <div className="max-w-[760px] rounded-lg border border-line p-5">
      <h3 className="text-sm font-semibold text-ink">No traces in the last {long}</h3>
      <p className="mt-1.5 text-sm text-muted-foreground">
        Requests show up here once a service exports OpenTelemetry traces to{" "}
        <span className="font-mono text-xs text-ink">{dataset}</span>. Set these variables on a
        service that uses an OpenTelemetry SDK, with an Axiom API token that can ingest into the
        dataset, then Ship:
      </p>
      <pre className="mt-3 overflow-x-auto rounded-md bg-surface-2 px-3 py-2.5 font-mono text-2xs leading-[19px] text-ink select-all">
        {env}
      </pre>
    </div>
  );
}
