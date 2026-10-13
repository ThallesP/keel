import { formatDuration } from "../format";
import { ChartCard } from "./chart-card";
import { DataTable } from "./data-table";
import { DeployMarks } from "./deploy-marks";
import { Grid } from "./grid";
import { HitColumns } from "./hit-columns";
import { rangeLabel } from "./labels";
import { BASE, band, type ChartProps, HEIGHT, niceMax, PAD_T, PLOT_H } from "./layout";
import { LegendKey } from "./legend-key";
import { Series } from "./series";
import { Tooltip } from "./tooltip";
import { TooltipRow } from "./tooltip-row";
import { useWidth } from "./use-width";
import { XAxis } from "./x-axis";

const RAMP = { p50: "#86a0ff", p95: "var(--color-primary)", p99: "#0d2585" } as const;
const PERCENTILES = ["p99", "p95", "p50"] as const;
const LEGEND = ["p50", "p95", "p99"] as const;

const axisDuration = (ms: number) => formatDuration(ms).replace(/\.0+(?=[^\d.])/, "");

export function LatencyChart({ buckets, bucketMs, hover, onHover, marks = [] }: ChartProps) {
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
        <LegendKey key={p} swatch="line" color={RAMP[p]} label={p} />
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
            {buckets[0] && (
              <DeployMarks x={x} start={buckets[0].time} bucketMs={bucketMs} marks={marks} />
            )}
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
