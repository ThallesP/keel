import { cn } from "@my-better-t-app/ui/lib/utils";
import { useNodes } from "@xyflow/react";

import { IconTile } from "../nodes/icons";
import { StatusDot } from "../primitives";
import { useCanvasDispatch, useCanvasUi } from "../store";
import type { CanvasNode, InfraNode, PanelTab } from "../types";
import { PanelFrame } from "./panel-frame";
import { DeploymentsTab } from "./tabs/deployments";
import { LogsTab } from "./tabs/logs";
import { VariablesTab } from "./tabs/variables";

const tabs: { id: PanelTab; label: string }[] = [
  { id: "deployments", label: "Deployments" },
  { id: "variables", label: "Variables" },
  { id: "logs", label: "Logs" },
];

function metaFor(node: InfraNode): string {
  switch (node.type) {
    case "service":
      if (node.data.status === "done") return "completed";
      return `${node.data.running}/${node.data.replicas} ${node.data.replicas === 1 ? "replica" : "replicas"}`;
    case "database":
    case "cache":
      return node.data.image ?? node.data.engine;
    case "volume":
      return `${node.data.sizeGb} GB`;
  }
}

function TabContent({ node, tab }: { node: InfraNode; tab: PanelTab }) {
  switch (tab) {
    case "deployments":
      return <DeploymentsTab node={node} />;
    case "variables":
      return <VariablesTab node={node} />;
    case "logs":
      return <LogsTab node={node} />;
  }
}

/** Node detail panel. Rendered only when a node has been selected at least once. */
export function BottomPanel() {
  const { panelNodeId, panelTab, panelCollapsed } = useCanvasUi();
  const dispatch = useCanvasDispatch();
  const nodes = useNodes<CanvasNode>();
  const node = nodes.find((n) => n.id === panelNodeId);
  if (!node || node.type === "group") return null;

  return (
    <PanelFrame
      collapsed={panelCollapsed}
      onToggle={() => dispatch({ type: "collapsePanel", collapsed: !panelCollapsed })}
      header={
        <>
          <span className="flex items-center gap-2">
            <IconTile type={node.type} size={22} className="rounded-md" />
            <span className="text-sm font-semibold text-ink">{node.data.name}</span>
            <StatusDot status={node.data.status} size={6} />
          </span>
          <nav className="flex h-11 items-center gap-[18px]" aria-label="Panel tabs">
            {tabs.map((t) => (
              <button
                key={t.id}
                type="button"
                aria-current={t.id === panelTab ? "page" : undefined}
                onClick={() => dispatch({ type: "setTab", tab: t.id })}
                className={cn(
                  "flex h-11 items-center border-b-2 text-sm",
                  t.id === panelTab && !panelCollapsed
                    ? "border-ink font-medium text-ink"
                    : "border-transparent text-muted-foreground hover:text-ink",
                )}
              >
                {t.label}
              </button>
            ))}
          </nav>
        </>
      }
      // Open panel carries the facts in the Deployments meta strip; only the strip needs them.
      meta={
        panelCollapsed && <span className="font-mono text-2xs text-faint">{metaFor(node)}</span>
      }
    >
      <TabContent node={node} tab={panelTab} />
    </PanelFrame>
  );
}
