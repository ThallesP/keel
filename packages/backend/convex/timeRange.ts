import { type Infer, v } from "convex/values";

// The Observability page's time range, shared by its logs and traces queries.

export const timeRange = v.union(
  v.literal("15m"),
  v.literal("1h"),
  v.literal("24h"),
  v.literal("7d"),
);

export type TimeRange = Infer<typeof timeRange>;

const MIN = 60_000;
const HOUR = 60 * MIN;

/** Range → its length and the bucket the charts use (~30 buckets each). */
export const RANGES: Record<TimeRange, { ms: number; bin: string; binMs: number }> = {
  "15m": { ms: 15 * MIN, bin: "30s", binMs: 30_000 },
  "1h": { ms: HOUR, bin: "2m", binMs: 2 * MIN },
  "24h": { ms: 24 * HOUR, bin: "1h", binMs: HOUR },
  "7d": { ms: 7 * 24 * HOUR, bin: "6h", binMs: 6 * HOUR },
};

/**
 * The window a range covers, aligned like APL's bin(): `count` buckets, the last one holding
 * `now`. The page's log lines and request numbers both start at `from`, so neither list reaches
 * back past the other.
 */
export function rangeWindow(range: TimeRange, now = Date.now()) {
  const { ms, binMs } = RANGES[range];
  const count = Math.round(ms / binMs);
  const from = Math.floor(now / binMs) * binMs - (count - 1) * binMs;
  return { from, to: from + count * binMs, count };
}
