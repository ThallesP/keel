import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { ServiceNode as ServiceNodeType } from "../types";
import { NodeShell } from "./node-shell";
import { RuntimeLine } from "./runtime-line";

/** Header: name, then the public domain or the image. Body: the status line. */
export const ServiceNode = memo(function ServiceNode({
  id,
  data,
  selected,
}: NodeProps<ServiceNodeType>) {
  return (
    <NodeShell
      id={id}
      type="service"
      name={data.name}
      subtitle={
        data.domain ?? (data.image ? <span className="font-mono">{data.image}</span> : undefined)
      }
      status={data.status}
      selected={selected}
    >
      <RuntimeLine id={id} data={data} />
    </NodeShell>
  );
});
