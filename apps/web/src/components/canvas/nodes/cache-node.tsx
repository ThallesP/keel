import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { CacheNode as CacheNodeType } from "../types";
import { NodeShell } from "./node-shell";
import { RuntimeLine } from "./runtime-line";

export const CacheNode = memo(function CacheNode({ id, data, selected }: NodeProps<CacheNodeType>) {
  return (
    <NodeShell
      id={id}
      type="cache"
      name={data.name}
      subtitle={data.engine}
      status={data.status}
      selected={selected}
    >
      <RuntimeLine id={id} data={data} />
    </NodeShell>
  );
});
