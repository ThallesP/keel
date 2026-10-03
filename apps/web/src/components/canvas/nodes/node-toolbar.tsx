import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@my-better-t-app/ui/components/dropdown-menu";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { NodeToolbar as FlowNodeToolbar, Position, useReactFlow } from "@xyflow/react";
import {
  AlignLeft,
  Ellipsis,
  Globe,
  type LucideIcon,
  Play,
  RefreshCw,
  Rocket,
  RotateCw,
  Square,
} from "lucide-react";

import { useCanvasActions, type CanvasActions } from "../actions";
import { Spinner } from "../primitives";
import { useCanvasDispatch, useRenaming } from "../store";
import type { CanvasNode, NodeStatus } from "../types";

const itemClass =
  "flex h-[22px] items-center gap-1.5 rounded-[5px] px-2 text-xs font-medium text-ink hover:bg-surface-2 disabled:pointer-events-none";

type Action = { label: string; icon: LucideIcon; run: (a: CanvasActions, id: string) => void };

const deploy: Action = { label: "Deploy", icon: Rocket, run: (a, id) => void a.start(id) };
const start: Action = { label: "Start", icon: Play, run: (a, id) => void a.start(id) };
const stop: Action = { label: "Stop", icon: Square, run: (a, id) => void a.stop(id) };
const redeploy: Action = {
  label: "Redeploy",
  icon: RotateCw,
  run: (a, id) => void a.redeploy(id, true),
};
/** One-shot image that already exited 0: a Redeploy is a re-run, so call it that. */
const runAgain: Action = {
  label: "Run again",
  icon: Play,
  run: (a, id) => void a.redeploy(id, false),
};
const restart: Action = {
  label: "Restart",
  icon: RefreshCw,
  run: (a, id) => void a.redeploy(id, false),
};

/**
 * Primary actions per runtime state. The bar reads as "what can I do to this right now":
 * never deployed → Deploy; running → Redeploy · Stop; stopped → Start; ran to completion →
 * Run again (nothing to stop). Restart lives in ⋯.
 */
const primary: Record<NodeStatus, Action[]> = {
  pending: [deploy],
  deploying: [],
  stopping: [],
  healthy: [redeploy, stop],
  done: [runAgain],
  error: [redeploy, stop],
  stopped: [start],
};

const secondary: Record<NodeStatus, Action[]> = {
  pending: [],
  deploying: [],
  stopping: [],
  healthy: [restart],
  done: [redeploy],
  error: [restart],
  stopped: [],
};

/** Floating action bar 10px above the selected node. Items follow the node's live status. */
export function NodeToolbar({ nodeId, visible }: { nodeId: string; visible: boolean }) {
  const dispatch = useCanvasDispatch();
  const actions = useCanvasActions();
  const { setRenamingId } = useRenaming();
  const flow = useReactFlow<CanvasNode>();
  const node = flow.getNode(nodeId);
  const runtime =
    node?.type === "service" || node?.type === "database" || node?.type === "cache"
      ? node.data
      : null;
  const status = runtime?.status;
  // Exposing a never-deployed service would only tunnel to nothing.
  const service = node?.type === "service" && node.data.status !== "pending" ? node.data : null;

  return (
    <FlowNodeToolbar isVisible={visible} position={Position.Top} offset={10} className="nopan">
      <div className="flex h-[30px] items-center gap-0.5 rounded-[7px] border border-line bg-bg px-1 shadow-[0_2px_8px_rgba(11,18,32,0.08)]">
        {(status === "deploying" || status === "stopping") && (
          <span className={cn(itemClass, "text-primary hover:bg-transparent")}>
            <Spinner />
            {status === "deploying" ? "Deploying…" : "Stopping…"}
          </span>
        )}
        {status &&
          primary[status].map((a) => (
            <button
              key={a.label}
              type="button"
              className={itemClass}
              onClick={() => a.run(actions, nodeId)}
            >
              <a.icon size={11} strokeWidth={1.6} aria-hidden />
              {a.label}
            </button>
          ))}
        {runtime && status !== "pending" && (
          <button
            type="button"
            className={itemClass}
            onClick={() => dispatch({ type: "openTab", nodeId, tab: "logs" })}
          >
            <AlignLeft size={11} strokeWidth={1.6} aria-hidden />
            Logs
          </button>
        )}
        {service && !service.public && (
          <button type="button" className={itemClass} onClick={() => void actions.expose(nodeId)}>
            <Globe size={11} strokeWidth={1.6} aria-hidden />
            Expose
          </button>
        )}
        {runtime && <span className="mx-0.5 h-4 w-px bg-line" />}
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label="More actions"
            className={cn(itemClass, "w-[22px] justify-center px-0 text-muted-foreground")}
          >
            <Ellipsis size={12} aria-hidden />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-36">
            {status && secondary[status].length > 0 && (
              <>
                {secondary[status].map((a) => (
                  <DropdownMenuItem key={a.label} onClick={() => a.run(actions, nodeId)}>
                    <a.icon size={12} strokeWidth={1.6} aria-hidden />
                    {a.label}
                  </DropdownMenuItem>
                ))}
                <DropdownMenuSeparator />
              </>
            )}
            {service?.public && (
              <DropdownMenuItem onClick={() => void actions.unexpose(nodeId)}>
                <Globe size={12} strokeWidth={1.6} aria-hidden />
                Make private
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={() => setRenamingId(nodeId)}>Rename</DropdownMenuItem>
            <DropdownMenuItem onClick={() => void actions.duplicate(nodeId)}>
              Duplicate
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              onClick={() => void flow.deleteElements({ nodes: [{ id: nodeId }] })}
            >
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </FlowNodeToolbar>
  );
}
