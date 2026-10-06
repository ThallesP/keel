import type { Node } from "@xyflow/react";

/** Mirrors `NodeStatus` in `packages/backend/convex/status.ts`. `done`: one-shot image exited 0. */
export type NodeStatus =
  | "healthy"
  | "done"
  | "deploying"
  | "stopping"
  | "error"
  | "stopped"
  | "pending";

/** Live runtime fields shared by everything Swarm runs (service, database, cache). */
export type RuntimeData = {
  name: string;
  status: NodeStatus;
  image?: string;
  port?: number;
  /** desired replicas */
  replicas: number;
  /** observed running tasks */
  running: number;
  /** Present while status === "deploying". */
  deploy?: { step: string; startedAt: number };
  /** Present while status === "error", e.g. "task: non-zero exit (1)". */
  error?: string;
  /** Present while status === "stopping" (when Stop was clicked) or "stopped" (when it shipped). */
  stoppedAt?: number;
  /** Present while status === "done": when the last task exited 0. */
  finishedAt?: number;
  /** Reachable from the internet through keel-proxy (at least one endpoint). */
  public: boolean;
  endpoints: Endpoint[];
};

/** Mirrors `endpointView` in `packages/backend/convex/endpoints.ts`. */
export type Endpoint = {
  protocol: "http" | "tcp" | "udp";
  /** Container port the proxy dials. */
  port: number;
  /** http only. */
  domain?: string;
  /** tcp / udp only: the port on the control plane. */
  publicPort?: number;
  /** `https://<domain>` or `<public IP>:<publicPort>`. */
  address: string;
  /** http `starting`: loaded, waiting for its certificate. */
  state: "starting" | "live" | "failed";
  error?: string;
};

export type ServiceData = RuntimeData & {
  /** The first https endpoint, shown as the card's subtitle. */
  http?: Endpoint;
};

export type DatabaseData = RuntimeData & {
  /** "Postgres 16" */
  engine: string;
};

export type CacheData = RuntimeData & {
  /** "Redis 7" */
  engine: string;
};

export type VolumeData = {
  name: string;
  status: NodeStatus;
  sizeGb: number;
};

export type GroupData = {
  label: string;
};

export type ServiceNode = Node<ServiceData, "service">;
export type DatabaseNode = Node<DatabaseData, "database">;
export type CacheNode = Node<CacheData, "cache">;
export type VolumeNode = Node<VolumeData, "volume">;
export type GroupNode = Node<GroupData, "group">;

export type CanvasNode = ServiceNode | DatabaseNode | CacheNode | VolumeNode | GroupNode;
export type CanvasNodeType = NonNullable<CanvasNode["type"]>;
/** Everything except "group" — the shell-rendered nodes. */
export type InfraNode = Exclude<CanvasNode, GroupNode>;
export type InfraNodeType = NonNullable<InfraNode["type"]>;
/** Nodes Swarm runs: service | database | cache. */
export type RuntimeNode = Exclude<InfraNode, VolumeNode>;

export type PanelTab = "deployments" | "variables" | "logs" | "settings";

export type DeployStepStatus = "pending" | "running" | "done" | "failed";

export type DeployStep = {
  /** Empty string for the final "health checks" step. */
  nodeId: string;
  /** Row label, e.g. the node name */
  label: string;
  status: DeployStepStatus;
  startedAt?: number;
  finishedAt?: number;
};

export type Deployment = {
  id: string;
  sha?: string;
  message: string;
  status: "running" | "success" | "failed";
  startedAt: number;
  finishedAt?: number;
  steps: DeployStep[];
  /** Log lines, oldest first, already stamped `mm:ss  node  text`. */
  log: string[];
};
