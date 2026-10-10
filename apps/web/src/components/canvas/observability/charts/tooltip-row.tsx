import { cn } from "@my-better-t-app/ui/lib/utils";
import type { ReactNode } from "react";

export function TooltipRow({
  color,
  value,
  line = true,
  children,
}: {
  color: string;
  value: string;
  line?: boolean;
  children: ReactNode;
}) {
  return (
    <span className="flex items-center gap-2">
      <span
        className={cn("shrink-0 rounded-full", line ? "h-0.5 w-2.5" : "size-2")}
        style={{ background: color }}
      />
      <span className="font-mono font-medium text-ink tabular-nums">{value}</span>
      <span className="text-muted-foreground">{children}</span>
    </span>
  );
}
