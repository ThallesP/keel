import { getRouteApi } from "@tanstack/react-router";
import {
  Background,
  BackgroundVariant,
  type OnDelete,
  type OnNodeDrag,
  type OnSelectionChangeParams,
  ReactFlow,
  ReactFlowProvider,
  SelectionMode,
  useOnSelectionChange,
  useReactFlow,
} from "@xyflow/react";
import { useCallback, useEffect, useRef } from "react";

import { CanvasActionsProvider, useCanvasActions } from "./actions";
import { BottomPanel } from "./bottom-panel/panel";
import { Controls } from "./controls";
import { EnvironmentProvider, type EnvironmentScope } from "./environment";
import { LogsPage } from "./logs-page";
import { nodeTypes } from "./nodes";
import { Rail } from "./rail";
import { StatusBar } from "./status-bar";
import { CanvasUiProvider, useCanvasDispatch, useCanvasUi, useRenaming } from "./store";
import { Toolbar } from "./toolbar";
import { Topbar } from "./topbar";
import type { CanvasNode } from "./types";
import { useDeploymentLink, useLinkedDeployment } from "./use-deployment-link";
import { useSyncedGraph } from "./use-synced-graph";
import { useHotkey } from "./use-hotkey";

import "./canvas.css";

function Flow() {
  const { nodes, onNodesChange } = useSyncedGraph();
  const dispatch = useCanvasDispatch();
  const { setRenamingId } = useRenaming();
  const actions = useCanvasActions();
  const flow = useReactFlow<CanvasNode>();
  const { panelNodeId } = useCanvasUi();
  const { deploymentId, clear } = useDeploymentLink();

  // Moving to another node drops the linked deployment; the panel falls back to that node's own.
  useOnSelectionChange({
    onChange: useCallback(
      ({ nodes: selected }: OnSelectionChangeParams<CanvasNode>) => {
        const first = selected.find((n) => n.type !== "group");
        if (first && deploymentId && first.id !== panelNodeId) clear();
        dispatch({ type: "select", nodeId: first?.id ?? null });
      },
      [dispatch, deploymentId, panelNodeId, clear],
    ),
  });

  // Positions are written on drag end only, never per move event.
  const onNodeDragStop = useCallback<OnNodeDrag<CanvasNode>>(
    (_event, node, dragged) => {
      for (const n of dragged.length > 0 ? dragged : [node]) actions.move(n.id, n.position);
    },
    [actions],
  );

  const onDelete = useCallback<OnDelete<CanvasNode>>(
    ({ nodes: gone }) => actions.removeNodes(gone.map((n) => n.id)),
    [actions],
  );

  useHotkey(
    { key: "F2" },
    useCallback(() => {
      const selected = flow.getNodes().filter((n) => n.selected && n.type !== "group");
      if (selected.length === 1 && selected[0]) setRenamingId(selected[0].id);
    }, [flow, setRenamingId]),
  );

  useHotkey(
    { key: "Escape" },
    useCallback(() => {
      flow.setNodes((ns) => ns.map((n) => (n.selected ? { ...n, selected: false } : n)));
      dispatch({ type: "select", nodeId: null });
    }, [flow, dispatch]),
  );

  return (
    <ReactFlow<CanvasNode>
      nodes={nodes}
      nodeTypes={nodeTypes}
      onNodesChange={onNodesChange}
      onNodeDragStop={onNodeDragStop}
      onDelete={onDelete}
      nodesConnectable={false}
      fitView
      fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
      minZoom={0.25}
      maxZoom={2}
      panOnScroll
      panOnDrag
      selectionMode={SelectionMode.Partial}
      zoomOnDoubleClick={false}
      deleteKeyCode={["Backspace", "Delete"]}
      className="bg-canvas"
    >
      <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="var(--color-dot)" />
      <Toolbar />
      <Controls />
    </ReactFlow>
  );
}

/**
 * The URL's deployment drives the panel: open the Deployments tab of an affected node (the
 * one already shown if it took part, else the first step) with that deployment selected.
 */
function Panel() {
  const { deploymentId, clear } = useDeploymentLink();
  const deployment = useLinkedDeployment(deploymentId);
  const { panelNodeId } = useCanvasUi();
  const dispatch = useCanvasDispatch();
  const handled = useRef<string | null>(null);
  useEffect(() => {
    if (!deploymentId) {
      handled.current = null;
      return;
    }
    if (deployment === undefined || handled.current === deploymentId) return;
    handled.current = deploymentId;
    const step =
      deployment?.steps.find((s) => s.nodeId === panelNodeId) ??
      deployment?.steps.find((s) => s.nodeId);
    if (step) dispatch({ type: "openTab", nodeId: step.nodeId, tab: "deployments" });
    else clear(); // stale, foreign or node-less: nothing to show
  }, [deploymentId, deployment, panelNodeId, dispatch, clear]);
  return <BottomPanel />;
}

const route = getRouteApi("/_auth/p/$projectId");

/**
 * Full-bleed project canvas: topbar / rail + flow + bottom panel / status bar. Rail views other
 * than the canvas (`?view=logs`) cover the flow and panel; the flow stays mounted underneath so
 * coming back keeps the viewport.
 */
export function Canvas({ scope }: { scope: EnvironmentScope }) {
  const { view } = route.useSearch();
  return (
    <ReactFlowProvider>
      <EnvironmentProvider scope={scope}>
        <CanvasUiProvider>
          <CanvasActionsProvider>
            <div className="flex h-svh flex-col bg-bg text-ink antialiased">
              <Topbar />
              <div className="flex min-h-0 flex-1">
                <Rail />
                <main className="relative flex min-w-0 flex-1 flex-col">
                  <div className="relative flex min-h-0 flex-1 flex-col" inert={view === "logs"}>
                    <div className="relative min-h-0 flex-1">
                      <Flow />
                    </div>
                    <Panel />
                  </div>
                  {view === "logs" && (
                    <div className="absolute inset-0 z-20">
                      <LogsPage />
                    </div>
                  )}
                </main>
              </div>
              <StatusBar />
            </div>
          </CanvasActionsProvider>
        </CanvasUiProvider>
      </EnvironmentProvider>
    </ReactFlowProvider>
  );
}
