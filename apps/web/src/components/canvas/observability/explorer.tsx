import { cn } from "@my-better-t-app/ui/lib/utils";
import { keepPreviousData } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import { useGetTraceOverview, useListEnvironmentLogs } from "@/api/gen";
import type { ProjectLine, TimeRange, TraceBucket, TraceOverview, TraceSummary } from "@/api/types";
import Loader from "@/components/loader";
import { errorMessage } from "@/lib/api";

import { CopyPrompt } from "../copy-prompt";
import { useEnvironment } from "../environment";
import { formatTimestamp } from "../format";
import { PageHeader, SectionLabel } from "../primitives";
import { useDebounced } from "../use-debounced";
import { TracesBanner } from "./axiom-gate";
import { type Hover, LatencyChart, RequestsChart, StatRow } from "./charts";
import { route, SearchField, type Sink } from "./chrome";
import { StreamEmpty, StreamError } from "./lamp";
import { LogContext } from "./log-context";
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

/** The request numbers, the wire's null lists (Go nil slices) read as empty ones. */
type Numbers = Omit<TraceOverview, "buckets" | "traces"> & {
  buckets: TraceBucket[];
  traces: TraceSummary[];
};

const toNumbers = (o: TraceOverview): Numbers => ({
  ...o,
  buckets: o.buckets ?? [],
  traces: o.traces ?? [],
});

/** `search`: the filter this answer is for, which can lag the field while the next one loads. */
type StreamData = {
  search: string;
  lines: ProjectLine[];
  overview: Numbers | null;
};

/**
 * Lines and request numbers for the range and filter, polled every 10 s while visible (one fetch
 * per half at a time, so a slow answer never lands after a newer one). Either half failing keeps
 * the other; the poll is the retry. A new range or filter keeps the previous data up, dimmed,
 * until its own arrives: no skeleton, no layout jump. Polled rather than pushed: a deploy's
 * stream of writes must not re-run Axiom queries (web-data.md §9.2, §9.5).
 */
function useStream(
  environmentId: string,
  range: TimeRange,
  search: string,
  withTraces: boolean,
  active: boolean,
) {
  const polled = {
    refetchInterval: POLL_MS,
    retry: false,
    placeholderData: keepPreviousData,
    meta: { realtime: false },
  };
  const path = { id: environmentId };
  const logs = useListEnvironmentLogs(
    { path, query: { range, search, tail: LINES } },
    { query: { ...polled, enabled: active } },
  );
  const numbers = useGetTraceOverview(
    { path, query: { range, search } },
    { query: { ...polled, enabled: active && withTraces, select: toNumbers } },
  );

  const settled = (q: { data: unknown; isError: boolean }) => q.data !== undefined || q.isError;
  const loaded = settled(logs) && (!withTraces || settled(numbers));
  const stale = logs.isPlaceholderData || (withTraces && numbers.isPlaceholderData);
  // The filter of the last answer shown in full; while a new one loads, the dimmed data is its.
  const [answered, setAnswered] = useState(search);
  if (loaded && !stale && answered !== search) setAnswered(search);

  const lines = logs.data?.lines;
  const overview = withTraces ? (numbers.data ?? null) : null;
  const shownSearch = stale ? answered : search;
  const data = useMemo<StreamData | null>(
    () => (loaded ? { search: shownSearch, lines: lines ?? [], overview } : null),
    [loaded, shownSearch, lines, overview],
  );
  const failed = logs.error ?? (withTraces ? numbers.error : null);
  return { data, stale, error: failed ? errorMessage(failed) : null };
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

  // The first load is the app's spinner. Nothing to show, empty or failed, gets the page to
  // itself (lamp.tsx); a partial failure is a line above the data, and later loads keep the
  // previous data up, dimmed. A sink from before traces keeps the plain layout either way: its
  // TracesBanner is where a pending Axiom org picker shows.
  const what = `the last ${long} of requests and logs`;
  if (data === null) {
    return (
      <div className={cn("flex min-h-0 flex-1 flex-col", hidden && "hidden")}>
        <Loader />
      </div>
    );
  }
  if (sink.traces && error && data.lines.length === 0 && !numbers) {
    return <StreamError what={what} error={error} className={cn(hidden && "hidden")} />;
  }
  if (
    sink.traces &&
    !error &&
    data.search === "" &&
    data.lines.length === 0 &&
    numbers?.stats.requests === 0
  ) {
    return (
      <StreamEmpty
        long={long}
        environmentId={environmentId}
        stale={stale}
        className={cn(hidden && "hidden")}
      />
    );
  }

  return (
    // Fades in when it takes over from the lamp (and when the stream comes back from a row).
    <div
      className={cn(
        "min-h-0 flex-1 overflow-auto animate-in fade-in-0 duration-300 motion-reduce:animate-none",
        hidden && "hidden",
      )}
    >
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
function NoRequests({ environmentId, long }: { environmentId: string; long: string }) {
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
