import type { ReactNode } from "react";

import type { TraceStats } from "@/api/gen";

import { formatCount, formatDuration } from "../../format";

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
