import type { DeployMark } from "@/gen/api";

import { ChartCard } from "./chart-card";
import { DataTable } from "./data-table";
import { DeployMarks } from "./deploy-marks";
import { Grid } from "./grid";
import { HitColumns } from "./hit-columns";
import { rangeLabel } from "./labels";
import { BASE, band, HEIGHT, type Hover, niceMax, PAD_T, PLOT_H } from "./layout";
import { LegendKey } from "./legend-key";
import { Series } from "./series";
import { Tooltip } from "./tooltip";
import { TooltipRow } from "./tooltip-row";
import { useWidth } from "./use-width";
import { XAxis } from "./x-axis";

export type LineSeries = {
  key: string;
  label: string;
  color: string;
  values: (number | null)[];
  dashed?: boolean;
};

export type LineChartProps = {
  title: string;
  times: number[];
  bucketMs: number;
  series: LineSeries[];
  format: (value: number) => string;
  formatAxis: (value: number) => string;
  hover: Hover;
  onHover: (i: Hover) => void;
  marks: DeployMark[];
  empty: string;
};

export function LineChart({
  title,
  times,
  bucketMs,
  series,
  format,
  formatAxis,
  hover,
  onHover,
  marks,
  empty,
}: LineChartProps) {
  const [ref, width] = useWidth();
  const max = niceMax(Math.max(0, ...series.flatMap((s) => s.values.map((v) => v ?? 0))));
  const ticks = [0, max / 2, max];
  const buckets = times.map((time) => ({ time }));
  const x = band(width, times.length);
  const y = (v: number) => BASE - (v / max) * PLOT_H;
  const hoveredTime = hover === null ? undefined : times[hover];
  const hasData = series.some((s) => s.values.some((v) => v !== null));

  return (
    <ChartCard
      title={title}
      legend={series.map((s) => (
        <LegendKey key={s.key} swatch="line" color={s.color} label={s.label} />
      ))}
      table={
        <DataTable
          caption={`${title} per bucket`}
          head={["Time", ...series.map((s) => s.label)]}
          rows={times.map((t, i) => [
            rangeLabel(t, bucketMs),
            ...series.map((s) => {
              const v = s.values[i];
              return v === null || v === undefined ? "—" : format(v);
            }),
          ])}
        />
      }
    >
      <div ref={ref} className="relative" onPointerLeave={() => onHover(null)}>
        {width > 0 && (
          <svg width={width} height={HEIGHT} aria-hidden className="block">
            <Grid width={width} ticks={ticks.map((t) => [BASE - y(t), t ? formatAxis(t) : "0"])} />
            {series.map((s) => (
              <Series
                key={s.key}
                color={s.color}
                dashed={s.dashed}
                points={s.values.map((v, i) => (v === null ? null : [x.center(i), y(v)]))}
              />
            ))}
            {hover !== null && (
              <line
                x1={x.center(hover)}
                x2={x.center(hover)}
                y1={PAD_T}
                y2={BASE}
                stroke="var(--color-faint)"
                strokeWidth={1}
              />
            )}
            <XAxis x={x} buckets={buckets} bucketMs={bucketMs} />
            <HitColumns x={x} count={times.length} onHover={onHover} />
            {times[0] !== undefined && (
              <DeployMarks x={x} start={times[0]} bucketMs={bucketMs} marks={marks} />
            )}
          </svg>
        )}
        {!hasData && (
          <p className="pointer-events-none absolute inset-x-0 top-10 text-center text-2xs text-faint">
            {empty}
          </p>
        )}
        {hoveredTime !== undefined && hover !== null && hasData && (
          <Tooltip x={x.center(hover)} width={width} title={rangeLabel(hoveredTime, bucketMs)}>
            {series.map((s) => {
              const v = s.values[hover];
              if (v === null || v === undefined) return null;
              return (
                <TooltipRow key={s.key} color={s.color} value={format(v)}>
                  {s.label}
                </TooltipRow>
              );
            })}
          </Tooltip>
        )}
      </div>
    </ChartCard>
  );
}
