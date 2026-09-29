import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useNodesState } from "@xyflow/react";
import { useQuery } from "convex/react";
import { useEffect } from "react";

import { useCanvasActions } from "./actions";
import { useEnvironment } from "./environment";
import { toCanvasNodes } from "./mapping";
import type { CanvasNode } from "./types";

/** Convex subscriptions → React Flow state. Selection, drag and measurements stay local. */
export function useSyncedGraph() {
  const { environmentId } = useEnvironment();
  const nodeDocs = useQuery(api.nodes.list, { environmentId });
  const [nodes, setNodes, onNodesChange] = useNodesState<CanvasNode>([]);
  const { selectOnArrival } = useCanvasActions();

  useEffect(() => {
    if (!nodeDocs) return;
    const arriving = selectOnArrival.current;
    const select = new Set(nodeDocs.filter((d) => arriving.has(d.id)).map((d) => d.id as string));
    for (const id of select) arriving.delete(id);
    setNodes((prev) => {
      const prevById = new Map(prev.map((n) => [n.id, n]));
      return toCanvasNodes(nodeDocs).map((next) => {
        const old = prevById.get(next.id);
        if (!old) return select.has(next.id) ? ({ ...next, selected: true } as CanvasNode) : next;
        return {
          ...next,
          selected: select.size > 0 ? false : old.selected,
          dragging: old.dragging,
          measured: old.measured,
          position: old.dragging ? old.position : next.position,
        } as CanvasNode;
      });
    });
  }, [nodeDocs, setNodes, selectOnArrival]);

  return { nodes, onNodesChange };
}
