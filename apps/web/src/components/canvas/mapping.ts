import type { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import type { FunctionReturnType } from "convex/server";

import { formatClock } from "./format";
import type { CanvasNode, Deployment, RuntimeData } from "./types";

// Convex docs → the UI's own types (types.ts stays the source of truth for components).

export type NodeDoc = FunctionReturnType<typeof api.nodes.list>[number];
export type DeploymentDoc = NonNullable<FunctionReturnType<typeof api.deployments.latest>>;

/** React Flow ids are strings; every node id on the canvas is a Convex id. */
export const asNodeId = (id: string) => id as Id<"nodes">;

const ENGINE_NAMES: Record<string, string> = {
  postgres: "Postgres",
  mysql: "MySQL",
  mongo: "MongoDB",
  redis: "Redis",
};

/** "postgres:16" → "Postgres 16", "mysql:8" → "MySQL 8", "redis:7-alpine" → "Redis 7-alpine" */
export function engineLabel(image: string | undefined, fallback: string): string {
  if (!image) return fallback;
  const [repo = "", tag] = image.split("@")[0]!.split(":");
  const name = repo.split("/").pop() ?? repo;
  const pretty = ENGINE_NAMES[name] ?? name.charAt(0).toUpperCase() + name.slice(1);
  return tag && tag !== "latest" ? `${pretty} ${tag}` : pretty;
}

function runtime(n: NodeDoc): RuntimeData {
  return {
    name: n.name,
    status: n.status,
    image: n.image,
    port: n.port,
    replicas: n.replicas,
    running: n.running,
    deploy: n.deploy,
    error: n.error,
    stoppedAt: n.stoppedAt,
    finishedAt: n.finishedAt,
  };
}

export function toCanvasNode(n: NodeDoc): CanvasNode {
  const base = {
    id: n.id as string,
    position: n.position,
    ...(n.parentId ? { parentId: n.parentId as string, extent: "parent" as const } : {}),
  };
  switch (n.type) {
    case "service":
      return { ...base, type: "service", data: runtime(n) };
    case "database":
      return {
        ...base,
        type: "database",
        data: { ...runtime(n), engine: engineLabel(n.image, "Postgres") },
      };
    case "cache":
      return {
        ...base,
        type: "cache",
        data: { ...runtime(n), engine: engineLabel(n.image, "Redis") },
      };
    case "volume":
      return {
        ...base,
        type: "volume",
        data: { name: n.name, status: n.status, sizeGb: n.config.sizeGb ?? 0 },
      };
    case "group":
      return {
        ...base,
        type: "group",
        style: { width: n.config.width ?? 300, height: n.config.height ?? 180 },
        data: { label: n.name },
      };
  }
}

/** Parents must precede children for React Flow to resolve `parentId`. */
export function toCanvasNodes(docs: NodeDoc[]): CanvasNode[] {
  const groups = docs.filter((d) => d.type === "group");
  const rest = docs.filter((d) => d.type !== "group");
  return [...groups, ...rest].map(toCanvasNode);
}

export function toDeployment(d: DeploymentDoc): Deployment {
  const nameOf = new Map(d.steps.map((s) => [s.nodeId, s.label]));
  const multi = d.steps.filter((s) => s.nodeId).length > 1;
  return {
    id: d._id,
    sha: d.sha,
    message: d.message,
    status: d.status,
    startedAt: d.startedAt,
    finishedAt: d.finishedAt,
    steps: d.steps.map((s) => ({
      nodeId: s.nodeId ?? "",
      label: s.label,
      status: s.status,
      startedAt: s.startedAt,
      finishedAt: s.finishedAt,
    })),
    log: d.log.map((l) => {
      const who = multi && l.nodeId ? `${nameOf.get(l.nodeId) ?? "?"}  ` : "";
      return `${formatClock(l.at - d.startedAt)}  ${who}${l.text}`;
    }),
  };
}
