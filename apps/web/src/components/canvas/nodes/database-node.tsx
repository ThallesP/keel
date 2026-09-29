import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { DatabaseNode as DatabaseNodeType } from "../types";
import { NodeShell } from "./node-shell";
import { RuntimeLine } from "./runtime-line";

export const DatabaseNode = memo(function DatabaseNode({
  id,
  data,
  selected,
}: NodeProps<DatabaseNodeType>) {
  return (
    <NodeShell
      id={id}
      type="database"
      name={data.name}
      subtitle={data.engine}
      status={data.status}
      selected={selected}
    >
      <RuntimeLine id={id} data={data} />
    </NodeShell>
  );
});
