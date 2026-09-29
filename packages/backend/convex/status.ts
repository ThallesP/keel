import type { Doc } from "./_generated/dataModel";

// `done`: a one-shot image ran and every task exited 0. Nothing is running, nothing is wrong.
// `stopping`: scaled to 0, Swarm has not yet confirmed every task is gone.
export type NodeStatus =
  | "healthy"
  | "done"
  | "deploying"
  | "stopping"
  | "error"
  | "stopped"
  | "pending";

type Shape = Pick<Doc<"nodes">, "desired" | "observed"> & { applyError?: string };

/** Observed matches desired and every replica runs, or every replica ran to completion. */
export function converged({ desired, observed }: Shape): boolean {
  if (!desired || !observed) return false;
  if (desired.replicas === 0) return observed.running === 0;
  if (observed.revision !== desired.revision) return false;
  if (observed.state === "completed") return (observed.completed ?? 0) >= desired.replicas;
  return observed.state === "ok" && observed.running >= desired.replicas;
}

export function deriveStatus(node: Shape): NodeStatus {
  const { desired, observed } = node;
  if (!desired || desired.revision === 0) return "pending"; // never shipped
  if (node.applyError) return "error"; // pull / spec failure; cleared on the next ship
  if (!observed) return "deploying"; // shipped, observe has not seen it yet
  // At 0 replicas the service spec label still carries the revision, so observe catches up
  // even though Swarm drops the task history. revision 0 means the service itself is gone.
  if (desired.replicas === 0) {
    if (observed.running > 0) return "stopping";
    if (observed.revision === 0 || observed.revision >= desired.revision) return "stopped";
    return "stopping";
  }
  if (observed.revision < desired.revision) return "deploying";
  if (observed.state === "crashloop" || observed.state === "failed") return "error";
  if (converged(node)) return observed.state === "completed" ? "done" : "healthy";
  return "deploying";
}

export const DEPLOYABLE = new Set<Doc<"nodes">["type"]>(["service", "database", "cache"]);
