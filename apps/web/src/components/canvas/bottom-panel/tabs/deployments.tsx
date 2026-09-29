import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useQuery } from "convex/react";
import { useMemo } from "react";

import { formatElapsed, timeAgo } from "../../format";
import { asNodeId, toDeployment } from "../../mapping";
import { SectionLabel } from "../../primitives";
import { statusLabel } from "../../status";
import { useCanvasDispatch } from "../../store";
import type { Deployment, InfraNode, NodeStatus } from "../../types";
import { useDeploymentLink } from "../../use-deployment-link";
import { useNow } from "../../use-now";
import { LogStream } from "../log-stream";
import { PanelMain, PanelRow, PanelSidebar } from "../panel-frame";
import { StepIcon } from "../step-icon";

const isErrorLine = (text: string) => /error:|crash loop|timed out|node deleted/.test(text);

type Pill = { label: string; tone: "success" | "primary" | "danger" | "muted" };

const pillTone = {
  success: "bg-success-soft text-success",
  primary: "bg-primary-soft text-primary",
  danger: "bg-danger-soft text-danger",
  muted: "bg-surface-2 text-muted-foreground",
} as const;

/** The newest deployment describes what is running now, so its pill follows the node. */
const currentPill: Record<NodeStatus, Pill> = {
  healthy: { label: "Active", tone: "success" },
  done: { label: "Completed", tone: "success" },
  deploying: { label: "Deploying", tone: "primary" },
  stopping: { label: "Stopping", tone: "muted" },
  error: { label: "Failed", tone: "danger" },
  stopped: { label: "Stopped", tone: "muted" },
  pending: { label: "Pending", tone: "muted" },
};

function pillFor(d: Deployment, current: boolean, nodeStatus: NodeStatus): Pill {
  if (current && d.status !== "running") return currentPill[nodeStatus];
  if (d.status === "running") return currentPill.deploying;
  if (d.status === "failed") return { label: "Failed", tone: "muted" };
  return { label: "Removed", tone: "muted" };
}

function StatusPill({ pill, className }: { pill: Pill; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center rounded-sm px-1.5 text-[10px] font-semibold tracking-[0.06em] uppercase",
        pillTone[pill.tone],
        className,
      )}
    >
      {pill.label}
    </span>
  );
}

/** One line of facts that used to be the Overview tab: image · port · replicas · status. */
function MetaStrip({ node, now }: { node: InfraNode; now: number }) {
  const facts: React.ReactNode[] = [];
  if (node.type === "volume") {
    facts.push(`${node.data.sizeGb} GB`, "not mounted in v1");
  } else {
    const d = node.data;
    facts.push(
      <span key="image" className="text-ink">
        {d.image ?? "no image"}
      </span>,
    );
    if (d.port) facts.push(`port ${d.port}`);
    if (d.status === "done") {
      facts.push(d.finishedAt ? `completed ${timeAgo(d.finishedAt, now)}` : "completed");
    } else {
      facts.push(`${d.running}/${d.replicas} ${d.replicas === 1 ? "replica" : "replicas"}`);
    }
  }
  const error = node.type !== "volume" ? node.data.error : undefined;
  return (
    <div className="flex h-8 shrink-0 items-center justify-between gap-4 border-b border-line px-5 font-mono text-2xs text-faint">
      <span className="flex min-w-0 items-center gap-2 truncate">
        {facts.map((f, i) => (
          <span key={i} className="flex items-center gap-2">
            {i > 0 && <span aria-hidden>·</span>}
            {f}
          </span>
        ))}
      </span>
      <span className={cn("shrink-0 truncate", error && "text-danger")}>
        {error ?? statusLabel[node.data.status]}
      </span>
    </div>
  );
}

function CurrentCard({
  d,
  pill,
  active,
  now,
  onSelect,
  onLogs,
}: {
  d: Deployment;
  pill: Pill;
  active: boolean;
  now: number;
  onSelect: () => void;
  onLogs?: () => void;
}) {
  const duration = (d.finishedAt ?? now) - d.startedAt;
  return (
    <div
      className={cn(
        "flex shrink-0 items-center gap-3 rounded-md border px-3 py-2.5",
        active ? "border-primary/40 bg-primary-soft/60" : "border-line hover:bg-surface-2",
      )}
    >
      <button
        type="button"
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center gap-3 text-left"
      >
        <StatusPill pill={pill} />
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-xs font-medium text-ink">{d.message}</span>
          <span className="font-mono text-2xs text-faint">
            {timeAgo(d.startedAt, now)} ·{" "}
            {d.status === "running" ? "running" : formatElapsed(duration)}
          </span>
        </span>
      </button>
      {onLogs && (
        <button
          type="button"
          onClick={onLogs}
          className="h-6 shrink-0 rounded-sm border border-line bg-bg px-2 text-2xs text-ink hover:bg-surface-2"
        >
          View logs
        </button>
      )}
    </div>
  );
}

function HistoryRow({
  d,
  pill,
  active,
  now,
  onSelect,
}: {
  d: Deployment;
  pill: Pill;
  active: boolean;
  now: number;
  onSelect: () => void;
}) {
  return (
    <PanelRow active={active} onClick={onSelect}>
      <StatusPill pill={pill} className="w-[74px] justify-center" />
      <span className="min-w-0 flex-1 truncate text-ink">{d.message}</span>
      <span className="shrink-0 font-mono text-2xs text-faint">
        {timeAgo(d.startedAt, now).replace(" ago", "")}
      </span>
    </PanelRow>
  );
}

function Detail({ d, now }: { d: Deployment; now: number }) {
  const duration = (d.finishedAt ?? now) - d.startedAt;
  return (
    <PanelMain className="gap-3">
      <div className="flex items-center gap-2.5 text-xs">
        <span className="text-muted-foreground">{d.message}</span>
        <span className="font-mono text-2xs text-faint">{formatElapsed(duration)}</span>
      </div>
      <ol className="flex flex-wrap gap-x-5 gap-y-1.5">
        {d.steps.map((s) => (
          <li key={s.nodeId || s.label} className="flex items-center gap-2 text-xs">
            <StepIcon status={s.status} />
            <span
              className={cn(
                s.status === "pending" ? "text-faint" : "text-ink",
                s.status === "running" && "text-primary",
              )}
            >
              {s.label}
            </span>
            {s.startedAt !== undefined && s.finishedAt !== undefined && (
              <span className="font-mono text-2xs text-faint">
                {formatElapsed(s.finishedAt - s.startedAt)}
              </span>
            )}
          </li>
        ))}
      </ol>
      <LogStream
        following={d.status === "running"}
        cursor={d.status === "running"}
        lines={d.log.map((text, i) => ({
          key: `${d.id}:${i}`,
          text,
          tone: isErrorLine(text) ? "danger" : i === d.log.length - 1 ? "ink" : "muted",
        }))}
      />
    </PanelMain>
  );
}

export function DeploymentsTab({ node }: { node: InfraNode }) {
  const dispatch = useCanvasDispatch();
  const link = useDeploymentLink();
  const now = useNow();
  const docs = useQuery(api.deployments.listForNode, { nodeId: asNodeId(node.id) });
  const rows = useMemo(() => (docs ?? []).map(toDeployment), [docs]);
  const [current, ...history] = rows;
  // Selection is the URL (`?deployment=`), so a row is a link and a reload keeps it.
  const selected = rows.find((r) => r.id === link.deploymentId) ?? current;

  const empty = (text: string) => (
    <div className="flex min-h-0 flex-1 flex-col">
      <MetaStrip node={node} now={now} />
      <p className="px-5 py-4 text-xs text-faint">{text}</p>
    </div>
  );
  if (node.type === "volume") return empty("Volumes are not deployed on their own.");
  if (!current || !selected) {
    return empty(docs === undefined ? "Loading…" : "No deployments yet. Press Deploy on the node.");
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <MetaStrip node={node} now={now} />
      <div className="flex min-h-0 flex-1">
        <PanelSidebar className="w-[380px] gap-1">
          <CurrentCard
            d={current}
            pill={pillFor(current, true, node.data.status)}
            active={current.id === selected.id}
            now={now}
            onSelect={() => link.open(current.id)}
            onLogs={
              node.data.status === "pending"
                ? undefined
                : () => dispatch({ type: "openTab", nodeId: node.id, tab: "logs" })
            }
          />
          {history.length > 0 && <SectionLabel className="px-2 pt-2.5 pb-1">History</SectionLabel>}
          {history.map((d) => (
            <HistoryRow
              key={d.id}
              d={d}
              pill={pillFor(d, false, node.data.status)}
              active={d.id === selected.id}
              now={now}
              onSelect={() => link.open(d.id)}
            />
          ))}
        </PanelSidebar>
        <Detail d={selected} now={now} />
      </div>
    </div>
  );
}
