import { ChartCard } from "./chart-card";
import { DataTable } from "./data-table";
import { Grid } from "./grid";
import { HitColumns } from "./hit-columns";
import { rangeLabel } from "./labels";
import { BASE, type Band, band, type ChartProps, HEIGHT, niceMax, PAD_T, PLOT_H } from "./layout";
import { LegendKey } from "./legend-key";
import { Tooltip } from "./tooltip";
import { TooltipRow } from "./tooltip-row";
import { useWidth } from "./use-width";
import { XAxis } from "./x-axis";

const countMax = (v: number) => Math.max(2, Math.round(niceMax(v)));

function column(x: number, y: number, w: number, h: number, rounded: boolean) {
  const r = rounded ? Math.min(4, w / 2, h) : 0;
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}

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
          <LegendKey swatch="rect" color="var(--color-primary)" label="Requests" />
          <LegendKey swatch="rect" color="var(--color-danger)" label="Errors" />
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

function HoverBand({ x, i }: { x: Band; i: number }) {
  return (
    <rect x={x.start(i)} y={PAD_T} width={x.band} height={PLOT_H} fill="var(--color-surface-2)" />
  );
}
