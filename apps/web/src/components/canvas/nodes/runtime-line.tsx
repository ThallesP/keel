import { cn } from "@my-better-t-app/ui/lib/utils";
import { Check } from "lucide-react";

import { formatElapsed, timeAgo } from "../format";
import { statusDotClass } from "../status";
import { useCanvasDispatch } from "../store";
import type { NodeStatus, RuntimeData } from "../types";
import { useNow } from "../use-now";

/**
 * Leading glyph of the status line. One 6px dot per desired replica, filled while that task
 * runs (`●●○` = 2 of 3), so the count is read at a glance instead of as `2/3 replicas`.
 * Single replica or nothing running collapses to the plain status dot.
 */
function ReplicaDots({
  status,
  desired,
  running,
}: {
  status: NodeStatus;
  desired: number;
  running: number;
}) {
  const perTask = (status === "healthy" || status === "error") && desired > 1;
  const count = perTask ? Math.min(desired, 6) : 1;
  return (
    <span
      className="flex items-center gap-[3px]"
      aria-label={perTask ? `${running} of ${desired} replicas running` : status}
    >
      {Array.from({ length: count }, (_, i) => (
        <span
          key={i}
          className={cn(
            "inline-block size-[6px] rounded-full",
            !perTask || i < running ? statusDotClass[status] : "border border-faint",
            (status === "deploying" || status === "stopping") && "animate-pulse",
          )}
        />
      ))}
    </span>
  );
}

const tone: Record<NodeStatus, string> = {
  healthy: "text-success",
  done: "text-success",
  deploying: "text-primary",
  stopping: "text-faint",
  error: "text-danger",
  stopped: "text-faint",
  pending: "text-faint",
};

function Line({
  status,
  data,
  children,
}: {
  status: NodeStatus;
  data: RuntimeData;
  children: React.ReactNode;
}) {
  return (
    <span className={cn("flex items-center gap-2 text-xs", tone[status])}>
      {status === "done" ? (
        <Check size={11} strokeWidth={2.4} className="shrink-0" aria-hidden />
      ) : (
        <ReplicaDots status={status} desired={data.replicas} running={data.running} />
      )}
      <span className="truncate">{children}</span>
    </span>
  );
}

function ErrorPill({ id, error }: { id: string; error?: string }) {
  const dispatch = useCanvasDispatch();
  return (
    <span className="flex items-center gap-2 rounded-md bg-danger-soft px-[9px] py-[6px] font-mono text-2xs text-[#B32626]">
      <span className="min-w-0 flex-1 truncate">{error ?? "crashed"}</span>
      <button
        type="button"
        className="font-sans font-medium hover:underline"
        onClick={() => dispatch({ type: "openTab", nodeId: id, tab: "logs" })}
      >
        Logs
      </button>
    </span>
  );
}

/**
 * Railway-style status line every Swarm-backed node shows: `●●● Online`, `● Deploying · pulling
 * 12s`, `✓ Completed 4s ago`, `● Stopping…`, `● Stopped 2h ago`, `○ Not deployed`. Errors add a pill with the
 * message and a Logs link. Port and counts stay in the panel.
 */
export function RuntimeLine({ id, data }: { id: string; data: RuntimeData }) {
  const now = useNow();
  switch (data.status) {
    case "healthy":
      return (
        <Line status="healthy" data={data}>
          Online
        </Line>
      );
    case "deploying":
      return (
        <Line status="deploying" data={data}>
          Deploying
          {data.deploy
            ? ` · ${data.deploy.step} ${formatElapsed(now - data.deploy.startedAt)}`
            : ""}
        </Line>
      );
    case "done":
      return (
        <Line status="done" data={data}>
          Completed{data.finishedAt ? ` ${timeAgo(data.finishedAt, now)}` : ""}
        </Line>
      );
    case "stopping":
      return (
        <Line status="stopping" data={data}>
          Stopping…
        </Line>
      );
    case "stopped":
      return (
        <Line status="stopped" data={data}>
          Stopped{data.stoppedAt ? ` ${timeAgo(data.stoppedAt, now)}` : ""}
        </Line>
      );
    case "pending":
      return (
        <Line status="pending" data={data}>
          Not deployed
        </Line>
      );
    case "error":
      return (
        <>
          <Line status="error" data={data}>
            Crashed
          </Line>
          <ErrorPill id={id} error={data.error} />
        </>
      );
  }
}
