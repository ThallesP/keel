import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useQuery } from "convex/react";
import { ChevronDown } from "lucide-react";
import { useCallback, useMemo } from "react";

import { Logo } from "@/components/logo";

import { AccountMenu } from "./account-menu";
import { useCanvasActions } from "./actions";
import { useEnvironment } from "./environment";
import { formatElapsed } from "./format";
import { notWired } from "./not-wired";
import { Kbd, Spinner } from "./primitives";
import { ProjectSwitcher } from "./project-switcher";
import { useLatestDeployment, useSummary } from "./use-data";
import { useHotkey } from "./use-hotkey";
import { useNow } from "./use-now";

function ShipButton() {
  const { environmentId } = useEnvironment();
  const summary = useSummary();
  const deployment = useLatestDeployment();
  // Shared with the canvas subscription; no extra round-trip.
  const nodes = useQuery(api.nodes.list, { environmentId });
  const actions = useCanvasActions();
  const now = useNow();

  const pendingChanges = summary?.pendingChanges ?? 0;
  const running = deployment?.status === "running";
  // Retry re-ships the nodes whose step failed. A node deleted since has nothing to retry, and
  // asking for it would fail with "Nothing to ship" every time; once none is left the button
  // is a plain Ship again, which takes every node with pending changes.
  const retry = useMemo(() => {
    if (deployment?.status !== "failed" || !nodes) return [];
    const alive = new Set<string>(nodes.map((n) => n.id));
    return deployment.steps
      .filter((s) => s.status === "failed" && alive.has(s.nodeId))
      .map((s) => s.nodeId);
  }, [deployment, nodes]);
  const failed = retry.length > 0;
  const ship = useCallback(() => {
    if (running) return;
    void actions.ship(failed ? retry : undefined);
  }, [running, failed, retry, actions]);
  useHotkey({ key: "Enter", mod: true }, ship);

  if (running) {
    const done = deployment.steps.filter((s) => s.status === "done").length;
    return (
      <span className="flex h-[30px] items-center gap-2 rounded-md bg-primary-strong pr-3 pl-2.5 text-sm font-medium text-on-primary">
        <Spinner className="text-on-primary" />
        Shipping… {done}/{deployment.steps.length}
        <span className="font-mono text-2xs text-white/70">
          {formatElapsed(now - deployment.startedAt)}
        </span>
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={ship}
      className={cn(
        "flex h-[30px] items-center gap-[7px] rounded-md pr-3 pl-2.5 text-sm font-medium text-on-primary",
        failed ? "bg-danger hover:bg-[#c93333]" : "bg-primary hover:bg-primary-strong",
      )}
    >
      <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden>
        <path
          d="M6 1.5v7M2.8 5.3 6 8.5l3.2-3.2M2 10.5h8"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
      {failed
        ? "Retry"
        : pendingChanges > 0
          ? `Ship · ${pendingChanges} ${pendingChanges === 1 ? "change" : "changes"}`
          : "Ship"}
      <Kbd className="pl-0.5 text-white/70">⌘↵</Kbd>
    </button>
  );
}

export function Topbar() {
  const { environmentName } = useEnvironment();
  return (
    <header className="flex h-12 shrink-0 items-center justify-between border-b border-line bg-bg px-4">
      <div className="flex items-center gap-3.5">
        <Logo />
        <span className="text-md text-line">/</span>
        <ProjectSwitcher />
        <span className="text-md text-line">/</span>
        <button
          type="button"
          onClick={() => notWired("Environment switcher")}
          className="flex h-6 items-center gap-1.5 rounded-md border border-line pr-2 pl-1.5 text-xs font-medium text-ink hover:bg-surface-2"
        >
          <span className="size-1.5 rounded-full bg-success" />
          {environmentName}
          <ChevronDown size={10} strokeWidth={1.6} className="text-muted-foreground" aria-hidden />
        </button>
      </div>
      <div className="flex items-center gap-2.5">
        <ShipButton />
        <AccountMenu />
      </div>
    </header>
  );
}
