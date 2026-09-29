import { cn } from "@my-better-t-app/ui/lib/utils";
import { ChevronDown } from "lucide-react";
import { useCallback } from "react";

import { AccountMenu } from "./account-menu";
import { useCanvasActions } from "./actions";
import { useEnvironment } from "./environment";
import { formatElapsed } from "./format";
import { notWired } from "./not-wired";
import { Kbd, Spinner } from "./primitives";
import { useLatestDeployment, useSummary } from "./use-data";
import { useHotkey } from "./use-hotkey";
import { useNow } from "./use-now";

function Logo() {
  return (
    <span className="flex items-center gap-2">
      <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden>
        <path d="M3 6.5h16l-2.2 6.5H6.5L3 6.5Z" fill="var(--color-primary)" />
        <path
          d="M6.5 13v3.5h9V13"
          fill="none"
          stroke="var(--color-primary)"
          strokeWidth="1.8"
          strokeLinecap="round"
        />
      </svg>
      <span className="text-[15px] leading-[18px] font-semibold tracking-tight text-ink">keel</span>
    </span>
  );
}

function ShipButton() {
  const summary = useSummary();
  const deployment = useLatestDeployment();
  const actions = useCanvasActions();
  const now = useNow();

  const pendingChanges = summary?.pendingChanges ?? 0;
  const running = deployment?.status === "running";
  const failed = deployment?.status === "failed";
  const ship = useCallback(() => {
    if (running) return;
    // Retry re-ships what failed; a plain Ship takes every node with pending changes.
    const failedIds = failed
      ? deployment.steps.filter((s) => s.status === "failed" && s.nodeId).map((s) => s.nodeId)
      : [];
    void actions.ship(failedIds.length > 0 ? failedIds : undefined);
  }, [running, failed, deployment, actions]);
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
  const { projectName, environmentName } = useEnvironment();
  return (
    <header className="flex h-12 shrink-0 items-center justify-between border-b border-line bg-bg px-4">
      <div className="flex items-center gap-3.5">
        <Logo />
        <span className="text-md text-line">/</span>
        <span className="text-sm font-medium text-ink">{projectName}</span>
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
