import { keepPreviousData } from "@tanstack/react-query";

import { useGetMetrics } from "@/gen/api";

import { useEnvironment } from "../environment";
import type { TimeRange } from "../time-range";

const refetchMs: Record<TimeRange, number> = {
  "15m": 15_000,
  "1h": 15_000,
  "24h": 60_000,
  "7d": 60_000,
};

export function useMetrics(range: TimeRange, nodeId: string) {
  const { environmentId } = useEnvironment();
  return useGetMetrics(
    { path: { id: environmentId }, query: { range, nodeId } },
    {
      query: {
        refetchInterval: refetchMs[range],
        retry: false,
        placeholderData: keepPreviousData,
        meta: { realtime: false },
      },
    },
  );
}
