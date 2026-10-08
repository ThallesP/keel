import { useNodesState } from "@xyflow/react";
import { useEffect, useSyncExternalStore } from "react";

import { useListNodes } from "@/api/gen";

import { useCanvasActions } from "./actions";
import { useEnvironment } from "./environment";
import { toCanvasNodes } from "./mapping";
import type { CanvasNode } from "./types";

/**
 * The environment's node list (kept live by realtime invalidation) → React Flow state, with the
 * moves and deletes still in flight laid over it. Selection, drag and measurements stay local.
 */
export function useSyncedGraph() {
  const { environmentId } = useEnvironment();
  const { data } = useListNodes({ path: { id: environmentId } });
  const [nodes, setNodes, onNodesChange] = useNodesState<CanvasNode>([]);
  const { overlay } = useCanvasActions();
  // A move or delete settling, or a create queuing its id, merges again with the list we have.
  const overlayVersion = useSyncExternalStore(overlay.subscribe, overlay.getVersion);

  useEffect(() => {
    if (!data) return;
    const views = overlay.apply(data.nodes ?? []);
    // Nodes this client just created: select them (and only them) once, as they arrive.
    const select = overlay.takeArrivals(views);
    setNodes((prev) => {
      const prevById = new Map(prev.map((n) => [n.id, n]));
      return toCanvasNodes(views).map((next) => {
        const old = prevById.get(next.id);
        if (!old) return select.has(next.id) ? ({ ...next, selected: true } as CanvasNode) : next;
        return {
          ...next,
          selected: select.size > 0 ? select.has(next.id) : old.selected,
          dragging: old.dragging,
          measured: old.measured,
          position: old.dragging ? old.position : next.position,
        } as CanvasNode;
      });
    });
  }, [data, overlayVersion, overlay, setNodes]);

  return { nodes, onNodesChange };
}
