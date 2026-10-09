import type { TraceBucket } from "@/api/gen";

import { tickLabel } from "./labels";
import { BASE, type Band } from "./layout";

export function XAxis({
  x,
  buckets,
  bucketMs,
}: {
  x: Band;
  buckets: TraceBucket[];
  bucketMs: number;
}) {
  const n = buckets.length;
  if (n === 0) return null;
  const middle = Math.floor(n / 2);
  const marks = [
    { i: 0, anchor: "start", at: x.start(0) },
    { i: middle, anchor: "middle", at: x.center(middle) },
    { i: n - 1, anchor: "end", at: x.start(n - 1) + x.band },
  ] as const;
  return (
    <g>
      {marks.map(({ i, anchor, at }) => (
        <text
          key={i}
          x={at}
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
