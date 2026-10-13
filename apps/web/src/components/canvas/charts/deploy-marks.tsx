import type { DeployMark } from "@/gen/api";

import { formatTimestamp } from "../format";
import { BASE, type Band, PAD_T } from "./layout";

const statusColor: Record<DeployMark["status"], string> = {
  running: "var(--color-primary)",
  success: "var(--color-faint)",
  failed: "var(--color-danger)",
};

export function DeployMarks({
  x,
  start,
  bucketMs,
  marks,
}: {
  x: Band;
  start: number;
  bucketMs: number;
  marks: DeployMark[];
}) {
  return (
    <g>
      {marks.map((m) => {
        const at = x.start(0) + ((m.at - start) / bucketMs) * x.band;
        return (
          <g key={m.id}>
            <title>{`Ship at ${formatTimestamp(m.at)}: ${m.message}`}</title>
            <line
              x1={at}
              x2={at}
              y1={PAD_T}
              y2={BASE}
              stroke={statusColor[m.status]}
              strokeWidth={1}
              strokeDasharray="2 3"
            />
            <circle cx={at} cy={PAD_T} r={3} fill={statusColor[m.status]} />
            <rect x={at - 4} y={0} width={8} height={BASE} fill="transparent" />
          </g>
        );
      })}
    </g>
  );
}
