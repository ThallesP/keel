import { cn } from "@my-better-t-app/ui/lib/utils";
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";

import type { TraceBucket, TraceStats } from "@/api/gen";

import { formatCount, formatDuration } from "../format";

// The Observability page's request numbers: a KPI row and two small charts over the same buckets, hand-drawn in
// SVG. One hover drives both charts (the crosshair finds the same bucket in each). Requests wear
// the accent, errors the danger tone (a status, labelled), latency percentiles one blue ramp
// light → dark (p50 → p99); both sets pass the colour-vision checks of the dataviz method.

const RAMP = { p50: "#86a0ff", p95: "var(--color-primary)", p99: "#0d2585" } as const;
/** Top to bottom, as the lines stack; the legend reads the other way. */
const PERCENTILES = ["p99", "p95", "p50"] as const;
const LEGEND = ["p50", "p95", "p99"] as const;

const PAD_T = 8;
const PLOT_H = 96;
const AXIS_H = 22;
const LEFT = 48;
const RIGHT = 6;
const HEIGHT = PAD_T + PLOT_H + AXIS_H;
const BASE = PAD_T + PLOT_H;

export type Hover = number | null;

type ChartProps = {
  buckets: TraceBucket[];
  bucketMs: number;
  hover: Hover;
  onHover: (i: Hover) => void;
};

// ── KPI row ─────────────────────────────────────────────────────────────────────────────────

export function StatRow({ stats, rangeMs }: { stats: TraceStats; rangeMs: number }) {
  const rate = stats.requests / (rangeMs / 60_000);
  const errorRate = stats.requests ? (stats.errors / stats.requests) * 100 : 0;
  const latency = (ms: number | null) => (ms === null ? "—" : formatDuration(ms));
  return (
    <div className="grid grid-cols-5 gap-3">
      <Stat
        label="Requests"
        value={formatCount(stats.requests)}
        title={stats.requests.toLocaleString()}
        note={`${rate < 10 ? rate.toFixed(1) : Math.round(rate)}/min`}
      />
      <Stat
        label="Error rate"
        value={`${errorRate.toFixed(errorRate > 0 && errorRate < 10 ? 1 : 0)}%`}
        note={
          <span className="flex items-center gap-1.5">
            {stats.errors > 0 && <span className="size-1.5 rounded-full bg-danger" />}
            {stats.errors.toLocaleString()} {stats.errors === 1 ? "error" : "errors"}
          </span>
        }
      />
      <Stat label="p50" value={latency(stats.p50)} note="median" />
      <Stat label="p95" value={latency(stats.p95)} note="95th percentile" />
      <Stat label="p99" value={latency(stats.p99)} note="99th percentile" />
    </div>
  );
}

function Stat({
  label,
  value,
  note,
  title,
}: {
  label: string;
  value: string;
  note: ReactNode;
  title?: string;
}) {
  return (
    <div className="rounded-lg border border-line px-4 py-3">
      <div className="text-2xs text-muted-foreground">{label}</div>
      <div className="mt-1 text-lg font-semibold tracking-[-0.02em] text-ink" title={title}>
        {value}
      </div>
      <div className="mt-0.5 text-2xs text-faint">{note}</div>
    </div>
  );
}

// ── Charts ──────────────────────────────────────────────────────────────────────────────────

/** Requests per bucket, the failed share stacked on top. */
export function RequestsChart({ buckets, bucketMs, hover, onHover }: ChartProps) {
  const [ref, width] = useWidth();
  const max = countMax(Math.max(0, ...buckets.map((b) => b.requests)));
  const ticks = max % 2 === 0 ? [0, max / 2, max] : [0, max];
  const x = band(width, buckets.length);
  const y = (v: number) => (v / max) * PLOT_H;
  const hovered = hover === null ? undefined : buckets[hover];

  return (
    <ChartCard
      title="Requests"
      legend={
        <>
          <Key swatch="rect" color="var(--color-primary)" label="Requests" />
          <Key swatch="rect" color="var(--color-danger)" label="Errors" />
        </>
      }
      table={
        <DataTable
          caption="Requests and errors per bucket"
          head={["Time", "Requests", "Errors"]}
          rows={buckets.map((b) => [rangeLabel(b.time, bucketMs), b.requests, b.errors])}
        />
      }
    >
      <div ref={ref} className="relative" onPointerLeave={() => onHover(null)}>
        {width > 0 && (
          <svg width={width} height={HEIGHT} aria-hidden className="block">
            <Grid width={width} ticks={ticks.map((t) => [y(t), t.toLocaleString()])} />
            {hover !== null && <HoverBand x={x} i={hover} />}
            {buckets.map((b, i) => {
              if (b.requests === 0) return null;
              const w = Math.min(24, x.band - 2);
              const left = x.center(i) - w / 2;
              const ok = b.requests - b.errors;
              const okH = ok > 0 ? Math.max(2, y(ok)) : 0;
              const errH = b.errors > 0 ? Math.max(2, y(b.errors)) : 0;
              return (
                <g key={b.time}>
                  {okH > 0 && (
                    <path
                      d={column(left, BASE - okH, w, okH, errH === 0)}
                      fill="var(--color-primary)"
                    />
                  )}
                  {errH > 0 && (
                    // 2px surface gap between the two segments of a stack.
                    <path
                      d={column(left, BASE - okH - (okH > 0 ? 2 : 0) - errH, w, errH, true)}
                      fill="var(--color-danger)"
                    />
                  )}
                </g>
              );
            })}
            <XAxis x={x} buckets={buckets} bucketMs={bucketMs} />
            <HitColumns x={x} count={buckets.length} onHover={onHover} />
          </svg>
        )}
        {hovered && hover !== null && (
          <Tooltip x={x.center(hover)} width={width} title={rangeLabel(hovered.time, bucketMs)}>
            <TooltipRow color="var(--color-primary)" value={hovered.requests.toLocaleString()}>
              requests
            </TooltipRow>
            <TooltipRow color="var(--color-danger)" value={hovered.errors.toLocaleString()}>
              errors
            </TooltipRow>
          </Tooltip>
        )}
      </div>
    </ChartCard>
  );
}

/** p50 / p95 / p99 of request duration per bucket. Empty buckets break the lines. */
export function LatencyChart({ buckets, bucketMs, hover, onHover }: ChartProps) {
  const [ref, width] = useWidth();
  const top = Math.max(0, ...buckets.map((b) => b.p99 ?? b.p95 ?? b.p50 ?? 0));
  const max = niceMax(top);
  const ticks = [0, max / 2, max];
  const x = band(width, buckets.length);
  const y = (v: number) => BASE - (v / max) * PLOT_H;
  const hovered = hover === null ? undefined : buckets[hover];

  return (
    <ChartCard
      title="Latency"
      legend={LEGEND.map((p) => (
        <Key key={p} swatch="line" color={RAMP[p]} label={p} />
      ))}
      table={
        <DataTable
          caption="Request duration percentiles per bucket"
          head={["Time", "p50", "p95", "p99"]}
          rows={buckets.map((b) => [
            rangeLabel(b.time, bucketMs),
            ...[b.p50, b.p95, b.p99].map((v) => (v === null ? "—" : formatDuration(v))),
          ])}
        />
      }
    >
      <div ref={ref} className="relative" onPointerLeave={() => onHover(null)}>
        {width > 0 && (
          <svg width={width} height={HEIGHT} aria-hidden className="block">
            <Grid
              width={width}
              ticks={ticks.map((t) => [BASE - y(t), t ? axisDuration(t) : "0"])}
            />
            {LEGEND.map((p) => (
              <Series
                key={p}
                color={RAMP[p]}
                points={buckets.map((b, i) => (b[p] === null ? null : [x.center(i), y(b[p])]))}
              />
            ))}
            {hovered && hover !== null && (
              <g>
                <line
                  x1={x.center(hover)}
                  x2={x.center(hover)}
                  y1={PAD_T}
                  y2={BASE}
                  stroke="var(--color-faint)"
                  strokeWidth={1}
                />
                {PERCENTILES.map((p) => {
                  const v = hovered[p];
                  if (v === null) return null;
                  return (
                    <circle
                      key={p}
                      cx={x.center(hover)}
                      cy={y(v)}
                      r={4}
                      fill={RAMP[p]}
                      stroke="var(--color-bg)"
                      strokeWidth={2}
                    />
                  );
                })}
              </g>
            )}
            <XAxis x={x} buckets={buckets} bucketMs={bucketMs} />
            <HitColumns x={x} count={buckets.length} onHover={onHover} />
          </svg>
        )}
        {hovered && hover !== null && (
          <Tooltip x={x.center(hover)} width={width} title={rangeLabel(hovered.time, bucketMs)}>
            {hovered.requests === 0 ? (
              <span className="text-faint">no requests</span>
            ) : (
              PERCENTILES.map((p) => (
                <TooltipRow key={p} color={RAMP[p]} line value={formatDuration(hovered[p] ?? 0)}>
                  {p}
                </TooltipRow>
              ))
            )}
          </Tooltip>
        )}
      </div>
    </ChartCard>
  );
}

// ── Pieces ──────────────────────────────────────────────────────────────────────────────────

function useWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(Math.floor(el.getBoundingClientRect().width));
    const observer = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.floor(entry.contentRect.width));
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

/** Bucket i's slot along the x axis. */
function band(width: number, count: number) {
  const size = Math.max(1, width - LEFT - RIGHT) / Math.max(1, count);
  return {
    band: size,
    start: (i: number) => LEFT + i * size,
    center: (i: number) => LEFT + (i + 0.5) * size,
  };
}

type Band = ReturnType<typeof band>;

/** 1, 2, 5 × 10ⁿ at or above `v`. */
function niceMax(v: number) {
  if (v <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(v));
  return ([1, 2, 5, 10].find((m) => m * exp >= v) ?? 10) * exp;
}

/** Round tick values read better without trailing zeros: "1.00s" → "1s". */
const axisDuration = (ms: number) => formatDuration(ms).replace(/\.0+(?=[^\d.])/, "");

/** A count axis never ticks at a fraction. */
const countMax = (v: number) => Math.max(2, Math.round(niceMax(v)));

/** A column with a 4px rounded data end on top (only on the top segment), square at the base. */
function column(x: number, y: number, w: number, h: number, rounded: boolean) {
  const r = rounded ? Math.min(4, w / 2, h) : 0;
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}

function Grid({ width, ticks }: { width: number; ticks: [number, string][] }) {
  return (
    <g>
      {ticks.map(([offset, label]) => {
        const y = BASE - offset;
        return (
          <g key={label}>
            <line
              x1={LEFT}
              x2={width - RIGHT}
              y1={y}
              y2={y}
              stroke={offset === 0 ? "var(--color-dot)" : "var(--color-line)"}
              strokeWidth={1}
              shapeRendering="crispEdges"
            />
            <text
              x={LEFT - 8}
              y={y}
              dy="0.32em"
              textAnchor="end"
              className="fill-faint font-mono text-2xs tabular-nums"
            >
              {label}
            </text>
          </g>
        );
      })}
    </g>
  );
}

function HoverBand({ x, i }: { x: Band; i: number }) {
  return (
    <rect x={x.start(i)} y={PAD_T} width={x.band} height={PLOT_H} fill="var(--color-surface-2)" />
  );
}

/** First, middle and last bucket times. */
function XAxis({ x, buckets, bucketMs }: { x: Band; buckets: TraceBucket[]; bucketMs: number }) {
  const n = buckets.length;
  if (n === 0) return null;
  const marks: [number, "start" | "middle" | "end"][] = [
    [0, "start"],
    [Math.floor(n / 2), "middle"],
    [n - 1, "end"],
  ];
  return (
    <g>
      {marks.map(([i, anchor]) => (
        <text
          key={i}
          x={anchor === "start" ? x.start(i) : anchor === "end" ? x.start(i) + x.band : x.center(i)}
          y={BASE + 15}
          textAnchor={anchor}
          className="fill-faint font-mono text-2xs tabular-nums"
        >
          {tickLabel(buckets[i]!.time, bucketMs)}
        </text>
      ))}
    </g>
  );
}

/** Full-height transparent columns: the pointer only has to be over the bucket, not the mark. */
function HitColumns({
  x,
  count,
  onHover,
}: {
  x: Band;
  count: number;
  onHover: (i: Hover) => void;
}) {
  return (
    <g>
      {Array.from({ length: count }, (_, i) => (
        <rect
          key={i}
          x={x.start(i)}
          y={0}
          width={x.band}
          height={HEIGHT}
          fill="transparent"
          onPointerEnter={() => onHover(i)}
        />
      ))}
    </g>
  );
}

/** A 2px line through the non-null points; a point with no neighbour is drawn as a dot. */
function Series({ color, points }: { color: string; points: ([number, number] | null)[] }) {
  const runs: [number, number][][] = [];
  let run: [number, number][] = [];
  for (const p of points) {
    if (p) run.push(p);
    else if (run.length) {
      runs.push(run);
      run = [];
    }
  }
  if (run.length) runs.push(run);
  return (
    <g>
      {runs.map((r) =>
        r.length === 1 ? (
          <circle key={r[0]![0]} cx={r[0]![0]} cy={r[0]![1]} r={2.5} fill={color} />
        ) : (
          <path
            key={r[0]![0]}
            d={r.map(([px, py], i) => `${i ? "L" : "M"}${px},${py}`).join("")}
            fill="none"
            stroke={color}
            strokeWidth={2}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ),
      )}
    </g>
  );
}

function ChartCard({
  title,
  legend,
  table,
  children,
}: {
  title: string;
  legend: ReactNode;
  table: ReactNode;
  children: ReactNode;
}) {
  return (
    <figure className="flex min-w-0 flex-col rounded-lg border border-line px-4 pt-3 pb-1">
      <figcaption className="flex items-center justify-between">
        <span className="text-xs font-medium text-ink">{title}</span>
        <span className="flex items-center gap-3">{legend}</span>
      </figcaption>
      <div className="mt-2">{children}</div>
      {table}
    </figure>
  );
}

function Key({ swatch, color, label }: { swatch: "rect" | "line"; color: string; label: string }) {
  return (
    <span className="flex items-center gap-1.5 text-2xs text-muted-foreground">
      <span
        className={cn(
          "shrink-0",
          swatch === "rect" ? "size-2 rounded-[2px]" : "h-0.5 w-3 rounded-full",
        )}
        style={{ background: color }}
      />
      {label}
    </span>
  );
}

function Tooltip({
  x,
  width,
  title,
  children,
}: {
  x: number;
  width: number;
  title: string;
  children: ReactNode;
}) {
  // Beside the crosshair, on whichever side has room.
  const right = x > width - 170;
  return (
    <div
      className="pointer-events-none absolute top-1 z-10 min-w-32 rounded-md border border-line bg-bg px-2.5 py-2 text-2xs shadow-[0_4px_16px_rgba(11,18,32,0.08)]"
      style={right ? { right: width - x + 10 } : { left: x + 10 }}
    >
      <div className="mb-1 font-mono text-faint">{title}</div>
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  );
}

function TooltipRow({
  color,
  value,
  line = true,
  children,
}: {
  color: string;
  value: string;
  line?: boolean;
  children: ReactNode;
}) {
  return (
    <span className="flex items-center gap-2">
      <span
        className={cn("shrink-0 rounded-full", line ? "h-0.5 w-2.5" : "size-2")}
        style={{ background: color }}
      />
      <span className="font-mono font-medium text-ink tabular-nums">{value}</span>
      <span className="text-muted-foreground">{children}</span>
    </span>
  );
}

/** The chart as a table, for screen readers: every value without hovering. */
function DataTable({
  caption,
  head,
  rows,
}: {
  caption: string;
  head: string[];
  rows: (string | number)[][];
}) {
  return (
    <table className="sr-only">
      <caption>{caption}</caption>
      <thead>
        <tr>
          {head.map((h) => (
            <th key={h}>{h}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={String(r[0])}>
            {r.map((c, i) => (
              <td key={i}>{c}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const pad = (n: number) => String(n).padStart(2, "0");
const clock = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}`;
const day = (d: Date) => d.toLocaleDateString(undefined, { month: "short", day: "numeric" });

/** Axis label: "14:30" within a day, "Oct 2 18:00" for wider buckets. */
function tickLabel(ms: number, bucketMs: number) {
  const d = new Date(ms);
  return bucketMs >= 60 * 60_000 ? `${day(d)} ${clock(d)}` : clock(d);
}

/** Tooltip/table label for a bucket: "14:30–14:32". */
function rangeLabel(ms: number, bucketMs: number) {
  const from = new Date(ms);
  const to = new Date(ms + bucketMs);
  const head = bucketMs >= 60 * 60_000 ? `${day(from)} ` : "";
  const secs = bucketMs < 60_000;
  const t = (d: Date) => (secs ? `${clock(d)}:${pad(d.getSeconds())}` : clock(d));
  return `${head}${t(from)}–${t(to)}`;
}
