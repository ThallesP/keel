import { keepPreviousData } from "@tanstack/react-query";

import { useGetMetrics } from "@/gen/api";

import { useEnvironment } from "../environment";
import type { TimeRange } from "../time-range";

export function useMetrics(range: TimeRange, nodeId: string) {
  const { environmentId } = useEnvironment();
  return useGetMetrics(
    { path: { id: environmentId }, query: { range, nodeId } },
    {
      query: {
        refetchInterval: 15_000,
        retry: false,
        placeholderData: keepPreviousData,
        meta: { realtime: false },
      },
    },
  );
}
