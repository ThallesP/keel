import { type NodeProps } from "@xyflow/react";
import { memo } from "react";

import type { ServiceData, ServiceNode as ServiceNodeType } from "../types";
import { NodeShell } from "./node-shell";
import { RuntimeLine } from "./runtime-line";

/**
 * The public domain once it serves HTTPS (a link; `nodrag nopan` + stopPropagation so React Flow
 * neither drags nor swallows the click), dimmed while its certificate is on the way, else the image.
 */
function Subtitle({ data }: { data: ServiceData }) {
  const { http } = data;
  switch (http?.state) {
    case "live":
      return (
        <a
          href={http.address}
          target="_blank"
          rel="noreferrer"
          className="nodrag nopan font-mono text-primary hover:underline"
          onClick={(e) => e.stopPropagation()}
        >
          {http.domain}
        </a>
      );
    case "starting":
      return (
        <span className="font-mono text-faint" title="Getting a certificate…">
          {http.domain}
        </span>
      );
    case "failed":
      return (
        <span className="text-danger" title={http.error}>
          Expose failed
        </span>
      );
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
