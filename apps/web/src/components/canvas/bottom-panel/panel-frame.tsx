import { cn } from "@my-better-t-app/ui/lib/utils";
import { ChevronDown } from "lucide-react";

type Props = {
  collapsed: boolean;
  onToggle: () => void;
  /** Left cluster: icon + name + tabs. */
  header: React.ReactNode;
  /** Right cluster before the chevron: mono meta / Cancel etc. */
  meta?: React.ReactNode;
  children: React.ReactNode;
};

/** 320px open / 44px collapsed strip. */
export function PanelFrame({ collapsed, onToggle, header, meta, children }: Props) {
  return (
    <section
      aria-label="Detail panel"
      className={cn(
        "flex shrink-0 flex-col border-t border-line bg-bg shadow-[0_-6px_24px_rgba(11,18,32,0.06)] transition-[height] duration-150",
        collapsed ? "h-11" : "h-80",
      )}
    >
      <div className="flex h-11 shrink-0 items-center justify-between border-b border-line px-5">
        <div className="flex min-w-0 items-center gap-[22px]">{header}</div>
        <div className="flex items-center gap-3.5">
          {meta}
          <button
            type="button"
            aria-label={collapsed ? "Expand panel" : "Collapse panel"}
            aria-expanded={!collapsed}
            onClick={onToggle}
            className="flex size-6 items-center justify-center rounded-sm text-muted-foreground hover:bg-surface-2"
          >
            <ChevronDown
              size={12}
              strokeWidth={1.6}
              className={cn("transition-transform", collapsed && "rotate-180")}
              aria-hidden
            />
          </button>
        </div>
      </div>
      {!collapsed && <div className="flex min-h-0 flex-1">{children}</div>}
    </section>
  );
}

/** 300px left column (Deployments widens it to 380px). */
export function PanelSidebar({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex w-[300px] shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-line p-3",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** Main column; wide, scrolls in both axes. */
export function PanelMain({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex min-w-0 flex-1 flex-col overflow-auto px-5 py-3", className)}>
      {children}
    </div>
  );
}

/** 34px list row with optional active tint. */
export function PanelRow({
  active,
  onClick,
  children,
  className,
}: {
  active?: boolean;
  onClick?: () => void;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex h-[34px] shrink-0 items-center gap-2.5 rounded-md px-2 text-left text-xs",
        active ? "bg-primary-soft" : "hover:bg-surface-2",
        className,
      )}
    >
      {children}
    </button>
  );
}
