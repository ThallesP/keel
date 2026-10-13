import { cn } from "@my-better-t-app/ui/lib/utils";
import { getRouteApi } from "@tanstack/react-router";
import { Braces, ChartLine, ScrollText, Settings, Workflow, type LucideIcon } from "lucide-react";

import { notWired } from "./not-wired";

type Item = { label: string; icon: LucideIcon; active?: boolean; onClick?: () => void };

const route = getRouteApi("/_auth/p/$projectId");

function RailButton({ label, icon: Icon, active, onClick }: Item) {
  return (
    <button
      type="button"
      aria-label={label}
      aria-current={active ? "page" : undefined}
      title={label}
      onClick={() => !active && (onClick ?? (() => notWired(label)))()}
      className={cn(
        "flex size-8 items-center justify-center rounded-md",
        active
          ? "bg-primary-soft text-primary"
          : "text-muted-foreground hover:bg-surface-2 hover:text-ink",
      )}
    >
      <Icon size={16} strokeWidth={1.5} aria-hidden />
    </button>
  );
}

/**
 * Canvas, Observability (panels of metrics, requests and logs), Logs (requests and logs in one
 * stream) and Settings are views of the same project (`?view=`); the rest are still mockup.
 */
export function Rail() {
  const { view } = route.useSearch();
  const navigate = route.useNavigate();
  const show = (next: "observability" | "logs" | "settings" | undefined) =>
    void navigate({
      search: (prev) => ({ ...prev, view: next, trace: undefined, around: undefined }),
    });
  const top: Item[] = [
    { label: "Canvas", icon: Workflow, active: !view, onClick: () => show(undefined) },
    {
      label: "Observability",
      icon: ChartLine,
      active: view === "observability",
      onClick: () => show("observability"),
    },
    { label: "Logs", icon: ScrollText, active: view === "logs", onClick: () => show("logs") },
    { label: "Variables", icon: Braces },
  ];
  return (
    <nav className="flex w-[52px] shrink-0 flex-col items-center justify-between border-r border-line bg-bg py-3">
      <div className="flex flex-col gap-1.5">
        {top.map((item) => (
          <RailButton key={item.label} {...item} />
        ))}
      </div>
      <div className="flex flex-col gap-1.5">
        <RailButton
          label="Settings"
          icon={Settings}
          active={view === "settings"}
          onClick={() => show("settings")}
        />
      </div>
    </nav>
  );
}
