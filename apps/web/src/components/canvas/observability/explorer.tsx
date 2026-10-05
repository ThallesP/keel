import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import type { ProjectLine } from "@my-better-t-app/backend/convex/logs";
import type { TimeRange, TraceOverview } from "@my-better-t-app/backend/convex/traces";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useAction } from "convex/react";
import { useEffect, useMemo, useState } from "react";

import { CopyPrompt } from "../copy-prompt";
import { useEnvironment } from "../environment";
import { errorMessage } from "../errors";
import { formatTimestamp } from "../format";
import { PageHeader, SectionLabel } from "../primitives";
import { useDebounced } from "../use-debounced";
import { TracesBanner } from "./axiom-gate";
import { type Hover, LatencyChart, RequestsChart, StatRow } from "./charts";
import { route, SearchField, type Sink } from "./chrome";
import { LogContext } from "./log-context";
import { Logbook } from "./logbook";
import { EventStream, mergeEvents, type StreamEvent } from "./stream";
import { TraceDetail } from "./trace";

/**
 * The Observability page with a sink: requests and logs in one place (ClickStack-style, no
 * separate tabs). On top, request rate, errors and latency from the traces; below, one stream of
 * log lines and requests. A row opens full screen: a request, or a line that names a trace, as
 * the trace waterfall with its log lines inline (`&trace=<id>`); any other line as the lines
 * around it plus the requests of that minute (`&around=<ms>`).
 */

const RANGES: { id: TimeRange; label: string; long: string }[] = [
  { id: "15m", label: "15m", long: "15 minutes" },
  { id: "1h", label: "1h", long: "hour" },
  { id: "24h", label: "24h", long: "24 hours" },
  { id: "7d", label: "7d", long: "7 days" },
];

type Kind = "all" | "requests" | "logs";
const KINDS: { id: Kind; label: string }[] = [
  { id: "all", label: "All" },
  { id: "requests", label: "Requests" },
  { id: "logs", label: "Logs" },
];

const POLL_MS = 10_000;
const LINES = 300;
/** traceProviders/axiom LIST: how many requests overview returns. */
const REQUESTS = 100;

/** What a row was opened from: when, and the line itself if it was one. */
export type Opened = { at: number; line?: ProjectLine };

export function Explorer({ sink }: { sink: Sink }) {
  const { trace, around } = route.useSearch();
  const navigate = route.useNavigate();
  const [range, setRange] = useState<TimeRange>("1h");
  const [search, setSearch] = useState("");
  const [kind, setKind] = useState<Kind>("all");
  const [opened, setOpened] = useState<Opened | null>(null);

  const open = (e: StreamEvent) => {
    if (e.kind === "request") {
      setOpened({ at: e.trace.start });
      void navigate({ search: (prev) => ({ ...prev, trace: e.trace.traceId, around: undefined }) });
    } else if (e.ref) {
      setOpened({ at: e.time, line: e.line });
      void navigate({ search: (prev) => ({ ...prev, trace: e.ref!.traceId, around: undefined }) });
    } else {
      setOpened({ at: e.time, line: e.line });
      void navigate({ search: (prev) => ({ ...prev, trace: undefined, around: e.time }) });
    }
  };
  const close = () => {
    setOpened(null);
    void navigate({ search: (prev) => ({ ...prev, trace: undefined, around: undefined }) });
  };
  const detail = trace !== undefined || around !== undefined;

  return (
    <div className="flex h-full flex-col bg-bg">
      <PageHeader title="Observability">
        {!detail && (
          <>
            <Segmented options={KINDS} value={kind} onChange={setKind} label="Show" />
            <SearchField
              value={search}
              onChange={setSearch}
              placeholder="Filter requests and logs"
            />
            <Segmented
              options={RANGES.map((r) => ({ ...r, label: r.label, title: `Last ${r.long}` }))}
              value={range}
              onChange={setRange}
              label="Time range"
              mono
            />
          </>
        )}
      </PageHeader>
      {trace !== undefined && (
        <TraceDetail
          key={trace}
          traceId={trace}
          at={opened?.at}
          focus={opened?.line}
          onBack={close}
        />
      )}
      {trace === undefined && around !== undefined && (
        <LogContext key={around} at={around} focus={opened?.line} onBack={close} onOpen={open} />
      )}
      {/* Stays mounted under an open row, so going back keeps the stream and its scroll. */}
      <Overview
        hidden={detail}
        sink={sink}
        range={range}
        search={search}
        kind={kind}
        onOpen={open}
      />
    </div>
  );
}

function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
  mono = false,
}: {
  options: { id: T; label: string; title?: string }[];
  value: T;
  onChange: (value: T) => void;
  label: string;
  mono?: boolean;
}) {
  return (
    <span
      className="flex h-7 items-center rounded-md border border-line p-0.5"
      role="group"
      aria-label={label}
    >
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          aria-pressed={o.id === value}
          title={o.title}
          onClick={() => onChange(o.id)}
          className={cn(
            "h-full rounded-sm px-2 text-2xs",
            mono && "font-mono",
            o.id === value ? "bg-surface-2 text-ink" : "text-muted-foreground hover:text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </span>
  );
}

type StreamData = { key: string; lines: ProjectLine[]; overview: TraceOverview | null };

/**
 * Lines and request numbers for the range and filter, polled while visible: the next poll is
 * scheduled once both halves settle, so a slow answer never lands after a newer one. Either half
 * failing keeps the other. A new range or filter keeps the previous data up, dimmed, until its own
 * arrives: no skeleton, no layout jump.
 */
function useStream(
  environmentId: Id<"environments">,
  range: TimeRange,
  search: string,
  withTraces: boolean,
  active: boolean,
) {
  const recent = useAction(api.logs.recent);
  const overview = useAction(api.traces.overview);
  const key = `${range}|${search}|${withTraces}`;
  const [data, setData] = useState<StreamData | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      const [lines, numbers] = await Promise.allSettled([
        recent({ environmentId, range, search, tail: LINES }),
        withTraces ? overview({ environmentId, range, search }) : Promise.resolve(null),
      ]);
      if (cancelled) return;
      setData((prev) => {
        const same = prev?.key === key;
        return {
          key,
          lines: lines.status === "fulfilled" ? lines.value.lines : same ? prev.lines : [],
          overview: numbers.status === "fulfilled" ? numbers.value : same ? prev.overview : null,
        };
      });
      const failed = [lines, numbers].find((r) => r.status === "rejected");
      setError(failed?.status === "rejected" ? errorMessage(failed.reason) : null);
      timer = setTimeout(() => void load(), POLL_MS);
    };
    void load();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [environmentId, range, search, withTraces, key, active, recent, overview]);
  return { data, stale: data !== null && data.key !== key, error };
}

function Overview({
  hidden,
  sink,
  range,
  search,
  kind,
  onOpen,
}: {
  hidden: boolean;
  sink: Sink;
  range: TimeRange;
  search: string;
  kind: Kind;
  onOpen: (e: StreamEvent) => void;
}) {
  const { environmentId } = useEnvironment();
  const filter = useDebounced(search.trim(), 300);
  const { data, stale, error } = useStream(environmentId, range, filter, !!sink.traces, !hidden);
  const [hover, setHover] = useState<Hover>(null);
  const long = RANGES.find((r) => r.id === range)?.long ?? range;
  const numbers = data?.overview ?? null;

  const { events, since } = useMemo(() => {
    if (!data) return { events: [], since: null };
    const lines =
      kind === "requests" ? null : { items: data.lines, full: data.lines.length >= LINES };
    const requests =
      kind === "logs" || !numbers
        ? null
        : { items: numbers.traces, full: numbers.traces.length >= REQUESTS };
    return mergeEvents(lines, requests);
  }, [data, numbers, kind]);

  // The first load gets the page to itself; later ones keep the previous data up, dimmed.
  if (data === null && !error) {
    return (
      <Logbook
        what={`the last ${long} of ${sink.traces ? "requests and logs" : "logs"}`}
        className={cn(hidden && "hidden")}
      />
    );
  }

  return (
    <div className={cn("min-h-0 flex-1 overflow-auto", hidden && "hidden")}>
      <div className={cn("flex flex-col gap-4 py-4 transition-opacity", stale && "opacity-60")}>
        <div className="flex flex-col gap-4 px-5 empty:hidden">
          {error && <p className="text-xs text-danger">{error}</p>}
          {!sink.traces && <TracesBanner />}
          {sink.traces && numbers && numbers.stats.requests > 0 && (
            <>
              <StatRow stats={numbers.stats} rangeMs={numbers.to - numbers.from} />
              <div className="grid grid-cols-2 gap-3">
                <RequestsChart
                  buckets={numbers.buckets}
                  bucketMs={numbers.bucketMs}
                  hover={hover}
                  onHover={setHover}
                />
                <LatencyChart
                  buckets={numbers.buckets}
                  bucketMs={numbers.bucketMs}
                  hover={hover}
                  onHover={setHover}
                />
              </div>
            </>
          )}
          {sink.traces && numbers && numbers.stats.requests === 0 && !filter && (
            <NoRequests environmentId={environmentId} long={long} />
          )}
        </div>
        <section className="flex flex-col gap-2">
          <div className="flex items-baseline justify-between px-5">
            <SectionLabel>Events</SectionLabel>
            {data && (
              <span className="text-2xs text-faint">
                {counts(events)} · newest first · every row opens its trace or context
              </span>
            )}
          </div>
          {data === null ? null : events.length === 0 ? (
            <p className="px-5 text-xs text-faint">
              {filter
                ? `Nothing matches “${filter}” in the last ${long}.`
                : `Nothing in the last ${long}.`}
            </p>
          ) : (
            <EventStream events={events} onOpen={onOpen} />
          )}
          {since !== null && (
            <p className="px-5 text-2xs text-faint">
              Showing the latest events, back to {formatTimestamp(since)}. Narrow the range or the
              filter to go further back.
            </p>
          )}
        </section>
      </div>
    </div>
  );
}

function counts(events: StreamEvent[]) {
  const requests = events.filter((e) => e.kind === "request").length;
  const lines = events.length - requests;
  const part = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
  return `${part(requests, "request", "requests")} · ${part(lines, "line", "lines")}`;
}

/**
 * No request in range: how to get some. A coding agent instruments the service with the prompt
 * and checks its own work with `keel run` + `keel traces`; then the service's Tracing switch and
 * a Ship send its deployed requests here too (docs/logs.md "Traces").
 */
function NoRequests({ environmentId, long }: { environmentId: Id<"environments">; long: string }) {
  return (
    <div className="flex items-center gap-4 rounded-lg border border-line px-4 py-3 text-xs text-muted-foreground">
      <p className="min-w-0 flex-1">
        <span className="font-medium text-ink">No requests in the last {long}.</span> Paste the
        prompt into your coding agent in a service&apos;s repo: it adds OpenTelemetry, runs the app
        with <code className="font-mono text-2xs text-ink">keel run</code> and checks that its
        requests show up here, marked <span className="font-mono text-2xs">local</span>. Then turn
        on Tracing in the service&apos;s Settings tab and Ship.
      </p>
      <CopyPrompt environmentId={environmentId} />
    </div>
  );
}
