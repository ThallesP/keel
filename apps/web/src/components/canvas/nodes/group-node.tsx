import { cn } from "@my-better-t-app/ui/lib/utils";
import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { GroupNode as GroupNodeType } from "../types";

/** Dashed container. Children set `parentId` + `extent: "parent"` and move with it. */
export const GroupNode = memo(function GroupNode({ data, selected }: NodeProps<GroupNodeType>) {
  return (
    <div
      className={cn(
        "h-full w-full rounded-[14px] border border-dashed bg-primary/[0.03] px-3.5 py-2.5",
        selected ? "border-primary" : "border-primary/30",
      )}
    >
      <span className="text-2xs font-semibold tracking-[0.06em] text-primary uppercase">
        {data.label}
      </span>
    </div>
  );
});
