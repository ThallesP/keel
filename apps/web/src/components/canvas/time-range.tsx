import type { GetMetricsQuery } from "@/gen/api";

import { Segmented } from "./segmented";

export type TimeRange = GetMetricsQuery["range"];

export const RANGES: { id: TimeRange; label: string; long: string }[] = [
  { id: "15m", label: "15m", long: "15 minutes" },
  { id: "1h", label: "1h", long: "hour" },
  { id: "24h", label: "24h", long: "24 hours" },
  { id: "7d", label: "7d", long: "7 days" },
];

export function RangePicker({
  value,
  onChange,
}: {
  value: TimeRange;
  onChange: (range: TimeRange) => void;
}) {
  return (
    <Segmented
      options={RANGES.map((r) => ({ ...r, title: `Last ${r.long}` }))}
      value={value}
      onChange={onChange}
      label="Time range"
      mono
    />
  );
}
