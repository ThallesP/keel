import type { RuntimeNode } from "../../types";
import { NetworkingSection } from "./networking";
import { TracingSection } from "./tracing";

/**
 * A node's settings that are not variables, one section each: public networking for everything
 * Swarm runs, tracing for services. The panel shows this tab for services, databases and caches.
 */
export function SettingsTab({ node }: { node: RuntimeNode }) {
  return (
    <div className="flex min-w-0 flex-1 flex-col overflow-auto">
      <NetworkingSection node={node} />
      {node.type === "service" && <TracingSection nodeId={node.id} />}
    </div>
  );
}
