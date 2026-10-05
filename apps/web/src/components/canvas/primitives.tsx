import { cn } from "@my-better-t-app/ui/lib/utils";
import { statusDotClass } from "./status";
import type { NodeStatus } from "./types";

export function StatusDot({
  status,
  size = 7,
  className,
}: {
  status: NodeStatus;
  size?: number;
  className?: string;
}) {
  return (
    <span
      aria-label={status}
      className={cn("inline-block shrink-0 rounded-full", statusDotClass[status], className)}
      style={{ width: size, height: size }}
    />
  );
}

/** Keyboard hint, e.g. `⌘K`. Mono 11px, faint. */
export function Kbd({ children, className }: { children: React.ReactNode; className?: string }) {
  return <kbd className={cn("font-mono text-2xs text-faint", className)}>{children}</kbd>;
}

/** 11px / 600 / 0.06em uppercase section label. */
export function SectionLabel({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn("text-2xs font-semibold tracking-[0.06em] text-faint uppercase", className)}
    >
      {children}
    </span>
  );
}

/** Small 12px spinner drawn as a dashed primary ring. */
export function Spinner({ size = 12, className }: { size?: number; className?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 12 12"
      className={cn("shrink-0 animate-spin text-primary", className)}
      aria-hidden
    >
      <circle
        cx="6"
        cy="6"
        r="4.2"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeDasharray="14 8"
      />
    </svg>
  );
}

/** Shared white floating surface: toolbar buttons, controls, node toolbar. */
export const floatingSurface =
  "flex items-center rounded-md border border-line bg-bg text-ink shadow-[0_1px_2px_rgba(11,18,32,0.06)]";

/** A rail page's 44px header: its title left, the view's controls right. */
export function PageHeader({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div className="flex h-11 shrink-0 items-center justify-between gap-4 border-b border-line px-5">
      <span className="text-sm font-medium text-ink">{title}</span>
      {children && <span className="flex shrink-0 items-center gap-3.5 text-2xs">{children}</span>}
    </div>
  );
}
