import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { VolumeNode as VolumeNodeType } from "../types";
import { NodeShell } from "./node-shell";

/** Volumes are persisted but not mounted in v1 (docs/volumes.md is a separate design). */
export const VolumeNode = memo(function VolumeNode({
  id,
  data,
  selected,
}: NodeProps<VolumeNodeType>) {
  return (
    <NodeShell
      id={id}
      type="volume"
      name={data.name}
      subtitle={`${data.sizeGb} GB`}
      status={data.status}
      selected={selected}
    >
      <span className="flex items-center gap-2 text-xs text-faint">
        <span className="inline-block size-[6px] rounded-full border border-faint" />
        Not mounted
      </span>
    </NodeShell>
  );
});
