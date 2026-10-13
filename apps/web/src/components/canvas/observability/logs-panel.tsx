import { keepPreviousData } from "@tanstack/react-query";

import { useListEnvironmentLogs } from "@/gen/api";
import { errorMessage } from "@/lib/api";

import { useEnvironment } from "../environment";
import { route } from "../logs/chrome";
import { EventStream, lineEvent, type StreamEvent } from "../logs/stream";
import { PanelMessage } from "./panel-message";
import type { PanelViewProps } from "./types";

export function LogsPanel({ panel, range }: PanelViewProps) {
  const { environmentId } = useEnvironment();
  const navigate = route.useNavigate();
  const { data, error } = useListEnvironmentLogs(
    { path: { id: environmentId }, query: { range, search: panel.search, tail: 50 } },
    {
      query: {
        refetchInterval: 15_000,
        retry: false,
        placeholderData: keepPreviousData,
        meta: { realtime: false },
      },
    },
  );
  const title = panel.search ? `Logs matching “${panel.search}”` : "Logs";

  const open = (e: StreamEvent) => {
    const opened =
      e.kind === "log" && e.ref
        ? { trace: e.ref.traceId, around: undefined }
        : { trace: undefined, around: e.time };
    void navigate({ search: (prev) => ({ ...prev, view: "logs", ...opened }) });
  };

  if (!data) {
    return <PanelMessage title={title}>{error ? errorMessage(error) : "Loading…"}</PanelMessage>;
  }
  if (data.lines.length === 0) {
    return <PanelMessage title={title}>Nothing in this range.</PanelMessage>;
  }
  return (
    <figure className="flex min-w-0 flex-col rounded-lg border border-line pt-3 pb-1">
      <figcaption className="flex items-baseline justify-between px-4">
        <span className="text-xs font-medium text-ink">{title}</span>
        <span className="text-2xs text-faint">newest first · opens in Logs</span>
      </figcaption>
      <div className="mt-2 max-h-60 overflow-y-auto">
        <EventStream events={data.lines.map(lineEvent)} onOpen={open} />
      </div>
    </figure>
  );
}
