import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { ServiceData, ServiceNode as ServiceNodeType } from "../types";
import { NodeShell } from "./node-shell";
import { RuntimeLine } from "./runtime-line";

/**
 * Public domain while the tunnel is live (a link; `nodrag nopan` + stopPropagation so React Flow
 * neither drags nor swallows the click), its state while it is not, else the image.
 */
function Subtitle({ data }: { data: ServiceData }) {
  switch (data.ingress?.state) {
    case "live":
      return (
        <a
          href={data.publicUrl}
          target="_blank"
          rel="noreferrer"
          title={`${data.publicUrl} · temporary URL, changes when the tunnel restarts`}
          className="nodrag nopan font-mono text-primary hover:underline"
          onClick={(e) => e.stopPropagation()}
        >
          {data.domain}
        </a>
      );
    case "starting":
      return <>Exposing…</>;
    case "failed":
      return <span className="text-danger">Expose failed</span>;
    default:
      return data.image ? <span className="font-mono">{data.image}</span> : null;
  }
}

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
      subtitle={<Subtitle data={data} />}
      status={data.status}
      selected={selected}
    >
      <RuntimeLine id={id} data={data} />
    </NodeShell>
  );
});
