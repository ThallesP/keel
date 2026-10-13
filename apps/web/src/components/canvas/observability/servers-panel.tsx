import { cn } from "@my-better-t-app/ui/lib/utils";

import type { ServerUsage } from "@/gen/api";
import { errorMessage } from "@/lib/api";

import { formatBytes, formatCores } from "../format";
import { useServices } from "../logs/chrome";
import { PanelMessage } from "./panel-message";
import type { PanelViewProps } from "./types";
import { useMetrics } from "./use-metrics";

export function ServersPanel({ range }: PanelViewProps) {
  const { data: overview, error } = useMetrics(range, "");
  if (!overview) {
    return <PanelMessage title="Servers">{error ? errorMessage(error) : "Loading…"}</PanelMessage>;
  }
  if (overview.servers.length === 0) {
    return (
      <PanelMessage title="Servers">
        No service of this environment ran in the last minute.
      </PanelMessage>
    );
  }
  return (
    <figure className="flex min-w-0 flex-col rounded-lg border border-line px-4 pt-3 pb-3">
      <figcaption className="flex items-baseline justify-between">
        <span className="text-xs font-medium text-ink">Servers</span>
        <span className="text-2xs text-faint">running this environment now</span>
      </figcaption>
      <ul className="mt-3 flex flex-col gap-4">
        {overview.servers.map((s) => (
          <ServerRow key={s.id} server={s} />
        ))}
      </ul>
    </figure>
  );
}

function ServerRow({ server }: { server: ServerUsage }) {
  const services = useServices();
  return (
    <li className="flex flex-col gap-1.5">
      <span className="flex items-baseline justify-between gap-3">
        <span className="truncate font-mono text-2xs text-ink">{server.name}</span>
        <span className="truncate text-2xs">
          {server.nodeIds.map((id) => {
            const label = services.ofLine(id);
            return (
              <span key={id} className={cn("ml-2", label.tone)}>
                {label.text}
              </span>
            );
          })}
        </span>
      </span>
      <Meter
        label="CPU"
        used={server.cpuCores}
        total={server.cpus}
        text={`${formatCores(server.cpuCores)} of ${server.cpus} vCPU`}
      />
      <Meter
        label="Memory"
        used={server.memoryBytes}
        total={server.memoryTotalBytes}
        text={`${formatBytes(server.memoryBytes)} of ${formatBytes(server.memoryTotalBytes)}`}
      />
      <Meter
        label="Disk"
        used={server.diskBytes}
        total={server.diskTotalBytes}
        text={`${formatBytes(server.diskBytes)} of ${formatBytes(server.diskTotalBytes)}`}
      />
    </li>
  );
}

function Meter({
  label,
  used,
  total,
  text,
}: {
  label: string;
  used: number;
  total: number;
  text: string;
}) {
  const share = total > 0 ? Math.min(1, used / total) : 0;
  return (
    <span className="grid grid-cols-[52px_1fr_150px] items-center gap-3 text-2xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="h-1.5 overflow-hidden rounded-full bg-surface-2">
        <span
          className={cn("block h-full rounded-full", share >= 0.9 ? "bg-danger" : "bg-primary")}
          style={{ width: `${share * 100}%` }}
        />
      </span>
      <span className="text-right font-mono text-faint tabular-nums">{text}</span>
    </span>
  );
}
