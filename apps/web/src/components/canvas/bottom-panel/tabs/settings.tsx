import { asNodeId } from "../../mapping";
import type { InfraNode } from "../../types";
import { TracingSection } from "./tracing";

/**
 * A service's settings that are not variables, one section each. So far only tracing; the
 * panel shows this tab for services alone.
 */
export function SettingsTab({ node }: { node: InfraNode }) {
  return (
    <div className="flex min-w-0 flex-1 flex-col overflow-auto">
      <TracingSection nodeId={asNodeId(node.id)} />
    </div>
  );
}
