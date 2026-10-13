import { keepPreviousData } from "@tanstack/react-query";

import { useGetTraceOverview } from "@/gen/api";
import { errorMessage } from "@/lib/api";

import { LatencyChart } from "../charts/latency-chart";
import { RequestsChart } from "../charts/requests-chart";
import { useEnvironment } from "../environment";
import { PanelMessage } from "./panel-message";
import type { PanelViewProps } from "./types";
import { useMetrics } from "./use-metrics";

export function RequestsPanel({
  kind,
  panel,
  range,
  hover,
  onHover,
}: PanelViewProps & { kind: "requests" | "latency" }) {
  const { environmentId } = useEnvironment();
  const { data: overview, error } = useGetTraceOverview(
    { path: { id: environmentId }, query: { range, search: "", nodeId: panel.nodeId } },
    {
      query: {
        refetchInterval: 15_000,
        retry: false,
        placeholderData: keepPreviousData,
        meta: { realtime: false },
      },
    },
  );
  const { data: marks } = useMetrics(range, panel.nodeId);
  const title = kind === "requests" ? "Requests" : "Latency";

  if (!overview) {
    return <PanelMessage title={title}>{error ? errorMessage(error) : "Loading…"}</PanelMessage>;
  }
  const Chart = kind === "requests" ? RequestsChart : LatencyChart;
  return (
    <Chart
      buckets={overview.buckets}
      bucketMs={overview.bucketMs}
      hover={hover}
      onHover={onHover}
      marks={marks?.deployments}
    />
  );
}
