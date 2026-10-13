import type { MetricsOverview } from "@/gen/api";
import { errorMessage } from "@/lib/api";

import { LineChart, type LineSeries } from "../charts/line-chart";
import { formatBytes, formatBytesShort, formatCores } from "../format";
import { type ServiceLabel, useServices } from "../logs/chrome";
import type { Hover } from "../charts/layout";
import type { TimeRange } from "../time-range";
import { PanelMessage } from "./panel-message";
import { useMetrics } from "./use-metrics";

type Resource = {
  title: string;
  format: (v: number) => string;
  formatAxis: (v: number) => string;
  series: (overview: MetricsOverview, label: (nodeId: string) => ServiceLabel) => LineSeries[];
};

export type ResourceKind = "cpu" | "memory" | "network";

const resources: Record<ResourceKind, Resource> = {
  cpu: {
    title: "CPU (vCPU)",
    format: (v) => `${formatCores(v)} vCPU`,
    formatAxis: (v) => String(Number(v.toPrecision(2))),
    series: (o, label) =>
      o.services.map((s) => ({
        key: s.nodeId,
        label: label(s.nodeId).text,
        color: label(s.nodeId).color,
        values: s.buckets.map((b) => b.cpuCores),
      })),
  },
  memory: {
    title: "Memory",
    format: formatBytes,
    formatAxis: formatBytesShort,
    series: (o, label) =>
      o.services.map((s) => ({
        key: s.nodeId,
        label: label(s.nodeId).text,
        color: label(s.nodeId).color,
        values: s.buckets.map((b) => b.memoryBytes),
      })),
  },
  network: {
    title: "Network (bytes/s)",
    format: (v) => `${formatBytes(v)}/s`,
    formatAxis: formatBytesShort,
    series: (o) => [
      {
        key: "in",
        label: "In",
        color: "var(--color-primary)",
        values: o.total.map((b) => b.rxBytesPerSec),
      },
      {
        key: "out",
        label: "Out",
        color: "#86a0ff",
        values: o.total.map((b) => b.txBytesPerSec),
        dashed: true,
      },
    ],
  },
};

export type ResourcePanelProps = {
  kind: ResourceKind;
  nodeId: string;
  range: TimeRange;
  hover: Hover;
  onHover: (i: Hover) => void;
};

export function ResourcePanel({ kind, nodeId, range, hover, onHover }: ResourcePanelProps) {
  const { data: overview, error } = useMetrics(range, nodeId);
  const services = useServices();
  const resource = resources[kind];
  const title = nodeId ? `${resource.title} · ${services.ofLine(nodeId).text}` : resource.title;

  if (!overview) {
    return <PanelMessage title={title}>{error ? errorMessage(error) : "Loading…"}</PanelMessage>;
  }
  return (
    <LineChart
      title={title}
      times={overview.total.map((b) => b.time)}
      bucketMs={overview.bucketMs}
      series={resource.series(overview, services.ofLine)}
      format={resource.format}
      formatAxis={resource.formatAxis}
      hover={hover}
      onHover={onHover}
      marks={overview.deployments}
      empty="No samples in this range"
    />
  );
}
