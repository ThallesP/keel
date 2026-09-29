import { cn } from "@my-better-t-app/ui/lib/utils";
import { Code, Database, HardDrive, Layers, type LucideIcon } from "lucide-react";

import type { InfraNodeType } from "../types";

const icons: Record<InfraNodeType, LucideIcon> = {
  service: Code,
  database: Database,
  cache: Layers,
  volume: HardDrive,
};

/** 14px stroke icon for a node type. Wrap in `IconTile` for the 26px shell tile. */
export function NodeTypeIcon({
  type,
  className,
  size = 14,
}: {
  type: InfraNodeType;
  className?: string;
  size?: number;
}) {
  const Icon = icons[type];
  return <Icon size={size} strokeWidth={1.5} className={cn("shrink-0", className)} aria-hidden />;
}

/** 26px / 7px-radius surface tile used in node headers and the panel header. */
export function IconTile({
  type,
  className,
  size = 26,
}: {
  type: InfraNodeType;
  className?: string;
  size?: number;
}) {
  return (
    <span
      className={cn(
        "flex shrink-0 items-center justify-center rounded-[7px] bg-surface-2 text-ink",
        className,
      )}
      style={{ width: size, height: size }}
    >
      <NodeTypeIcon type={type} size={Math.round(size * 0.54)} />
    </span>
  );
}
