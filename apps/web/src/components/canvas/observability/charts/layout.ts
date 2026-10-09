import type { TraceBucket } from "@/api/gen";

export const PAD_T = 8;
export const PLOT_H = 96;
const AXIS_H = 22;
export const LEFT = 48;
export const RIGHT = 6;
export const HEIGHT = PAD_T + PLOT_H + AXIS_H;
export const BASE = PAD_T + PLOT_H;

export type Hover = number | null;

export type ChartProps = {
  buckets: TraceBucket[];
  bucketMs: number;
  hover: Hover;
  onHover: (i: Hover) => void;
};

export function band(width: number, count: number) {
  const size = Math.max(1, width - LEFT - RIGHT) / Math.max(1, count);
  return {
    band: size,
    start: (i: number) => LEFT + i * size,
    center: (i: number) => LEFT + (i + 0.5) * size,
  };
}

export type Band = ReturnType<typeof band>;

export function niceMax(v: number) {
  if (v <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(v));
  return ([1, 2, 5, 10].find((m) => m * exp >= v) ?? 10) * exp;
}
