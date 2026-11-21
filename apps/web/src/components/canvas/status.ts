import type { NodeStatus } from "./types";

export const statusDotClass: Record<NodeStatus, string> = {
  healthy: "bg-success",
  done: "bg-success",
  deploying: "bg-primary",
  stopping: "bg-faint",
  error: "bg-danger",
  stopped: "bg-faint",
  pending: "border border-faint bg-transparent",
};
