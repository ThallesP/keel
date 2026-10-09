import type { Deployment as ApiDeployment, NodeView } from "@/api/gen";

import { formatClock } from "./format";
import type { CanvasNode, Deployment, Endpoint, RuntimeData } from "./types";

// API answers → the UI's own types (types.ts stays the source of truth for components).

/**
 * @deprecated Ids are plain strings now; pass them as they are. Kept (an identity) only so files
 * still migrating keep compiling; delete once nothing imports it.
 */
export const asNodeId = (id: string): string => id;

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

function runtime(n: NodeView): RuntimeData {
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
    public: n.public,
    endpoints: n.endpoints,
  };
}

const RANK = { live: 0, starting: 1, failed: 2 } as const;

/** The https endpoint the card names: one that serves beats one on its way beats a failed one. */
function bestHttp(endpoints: Endpoint[]) {
  return endpoints
    .filter((e) => e.protocol === "http")
    .sort((a, b) => RANK[a.state] - RANK[b.state])[0];
}

export function toCanvasNode(n: NodeView): CanvasNode {
  const base = {
    id: n.id,
    position: n.position,
    ...(n.parentId ? { parentId: n.parentId, extent: "parent" as const } : {}),
  };
  switch (n.type) {
    case "service":
      return {
        ...base,
        type: "service",
        data: { ...runtime(n), http: bestHttp(n.endpoints) },
      };
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
export function toCanvasNodes(nodes: NodeView[]): CanvasNode[] {
  const groups = nodes.filter((n) => n.type === "group");
  const rest = nodes.filter((n) => n.type !== "group");
  return [...groups, ...rest].map(toCanvasNode);
}

export function toDeployment(d: ApiDeployment): Deployment {
  const { steps } = d;
  const nameOf = new Map(steps.map((s) => [s.nodeId, s.label]));
  const multi = steps.filter((s) => s.nodeId).length > 1;
  return {
    id: d.id,
    sha: d.sha,
    message: d.message,
    status: d.status,
    startedAt: d.startedAt,
    finishedAt: d.finishedAt,
    steps: steps.map((s) => ({
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
