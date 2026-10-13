import { cn } from "@my-better-t-app/ui/lib/utils";
import { Check } from "lucide-react";

import { formatElapsed, timeAgo } from "../format";
import { statusDotClass } from "../status";
import { useCanvasDispatch } from "../store";
import type { NodeStatus, RuntimeData } from "../types";
import { useNow } from "../use-now";
import { STACK_DEPTH } from "./node-shell";

const tone: Record<NodeStatus, string> = {
  healthy: "text-success",
  done: "text-success",
  deploying: "text-primary",
  stopping: "text-faint",
  error: "text-danger",
  stopped: "text-faint",
  pending: "text-faint",
};

function Line({ status, children }: { status: NodeStatus; children: React.ReactNode }) {
  return (
    <span className={cn("flex items-center gap-2 text-xs", tone[status])}>
      {status === "done" ? (
        <Check size={11} strokeWidth={2.4} className="shrink-0" aria-hidden />
      ) : (
        <span
          className={cn(
            "inline-block size-[6px] shrink-0 rounded-full",
            statusDotClass[status],
            (status === "deploying" || status === "stopping") && "animate-pulse",
          )}
        />
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

export function RuntimeLine({ id, data }: { id: string; data: RuntimeData }) {
  const now = useNow();
  switch (data.status) {
    case "healthy":
      return (
        <Line status="healthy">
          Online
          {data.replicas > STACK_DEPTH && (
            <span className="pl-1.5 font-mono text-2xs text-faint">×{data.replicas}</span>
          )}
        </Line>
      );
    case "deploying":
      return (
        <Line status="deploying">
          Deploying
          {data.deploy
            ? ` · ${data.deploy.step} ${formatElapsed(now - data.deploy.startedAt)}`
            : ""}
        </Line>
      );
    case "done":
      return (
        <Line status="done">
          Completed{data.finishedAt ? ` ${timeAgo(data.finishedAt, now)}` : ""}
        </Line>
      );
    case "stopping":
      return <Line status="stopping">Stopping…</Line>;
    case "stopped":
      return (
        <Line status="stopped">
          Stopped{data.stoppedAt ? ` ${timeAgo(data.stoppedAt, now)}` : ""}
        </Line>
      );
    case "pending":
      return <Line status="pending">Not deployed</Line>;
    case "error":
      return (
        <>
          <Line status="error">Crashed</Line>
          <ErrorPill id={id} error={data.error} />
        </>
      );
  }
}
