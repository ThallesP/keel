import { cn } from "@my-better-t-app/ui/lib/utils";
import { getRouteApi } from "@tanstack/react-router";
import { AlignLeft, Braces, ChartLine, Settings, Workflow, type LucideIcon } from "lucide-react";

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

/** Canvas and Logs are views of the same project (`?view=`); the rest are still mockup. */
export function Rail() {
  const { view } = route.useSearch();
  const navigate = route.useNavigate();
  const show = (next: "logs" | undefined) =>
    void navigate({ search: (prev) => ({ ...prev, view: next }) });
  const top: Item[] = [
    { label: "Canvas", icon: Workflow, active: !view, onClick: () => show(undefined) },
    { label: "Logs", icon: AlignLeft, active: view === "logs", onClick: () => show("logs") },
    { label: "Metrics", icon: ChartLine },
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
        <RailButton label="Settings" icon={Settings} />
      </div>
    </nav>
  );
}
