import { v } from "convex/values";

import type { Doc } from "./_generated/dataModel";
import { endpointAddress, endpointView } from "./endpoints";
import { deriveStatus } from "./status";

export const DEFAULTS = {
  service: { name: "service", image: "nginx:alpine", port: 80 },
  database: { name: "postgres", image: "postgres:16", port: 5432 },
  cache: { name: "redis", image: "redis:7", port: 6379 },
  volume: { name: "data" },
  group: { name: "group" },
} as const;

/**
 * Engines the Add modal offers under "Database". Redis is a `cache` node; the rest are
 * `database`. Keys match the image repo so `engineOf(image)` can recover the engine later.
 */
export const ENGINES = {
  postgres: { type: "database", image: "postgres:16", port: 5432 },
  mysql: { type: "database", image: "mysql:8", port: 3306 },
  mongo: { type: "database", image: "mongo:7", port: 27017 },
  redis: { type: "cache", image: "redis:7", port: 6379 },
} as const;

export type Engine = keyof typeof ENGINES;

/** Validator for `nodes.create({ engine })`. */
export const engine = v.union(
  v.literal("postgres"),
  v.literal("mysql"),
  v.literal("mongo"),
  v.literal("redis"),
);

/** `postgres:16` → "postgres", `docker.io/library/mysql:8.4` → "mysql", `nginx` → undefined */
export function engineOf(image: string | undefined): Engine | undefined {
  if (!image) return undefined;
  const repo = image.split("@")[0]!.split("/").pop()!.split(":")[0]!;
  return repo in ENGINES ? (repo as Engine) : undefined;
}

/**
 * Credentials the engine's official image reads on first boot. Every engine gets a password, even
 * one that is never exposed. Redis reads none from its env: swarm.ts passes REDIS_PASSWORD to
 * `redis-server --requirepass`.
 */
export function seedVariables(engine: Engine | undefined) {
  const rows = (pairs: [string, string, boolean][]) =>
    pairs.map(([key, value, secret]) => ({ key, value, secret }));
  switch (engine) {
    case "postgres":
      return rows([
        ["POSTGRES_USER", "app", false],
        ["POSTGRES_PASSWORD", randomSecret(), true],
        ["POSTGRES_DB", "app", false],
      ]);
    case "mysql":
      return rows([
        ["MYSQL_ROOT_PASSWORD", randomSecret(), true],
        ["MYSQL_USER", "app", false],
        ["MYSQL_PASSWORD", randomSecret(), true],
        ["MYSQL_DATABASE", "app", false],
      ]);
    case "mongo":
      return rows([
        ["MONGO_INITDB_ROOT_USERNAME", "app", false],
        ["MONGO_INITDB_ROOT_PASSWORD", randomSecret(), true],
      ]);
    case "redis":
      return rows([["REDIS_PASSWORD", randomSecret(), true]]);
    default:
      return [];
  }
}

export function randomSecret(length = 20) {
  const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  let out = "";
  for (let i = 0; i < length; i++) out += alphabet[Math.floor(Math.random() * alphabet.length)];
  return out;
}

/** `postgres`, then `postgres-2`, `postgres-3`… within one environment. */
export function uniqueName(base: string, taken: Set<string>) {
  if (!taken.has(base)) return base;
  for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`;
}

/** What the canvas renders. Status is derived here, never stored. */
export function view(n: Doc<"nodes">) {
  const status = deriveStatus(n);
  const step =
    status !== "deploying"
      ? undefined
      : !n.observed || n.observed.revision < (n.desired?.revision ?? 0)
        ? "pulling image"
        : n.observed.state === "updating"
          ? "rolling out"
          : "starting";
  const http = n.endpoints?.find((e) => e.protocol === "http");
  return {
    id: n._id,
    type: n.type,
    name: n.name,
    parentId: n.parentId,
    position: n.position,
    config: n.config,
    dirty: n.dirty ?? false,
    status,
    image: n.desired?.image,
    port: n.desired?.port,
    replicas: n.desired?.replicas ?? 0,
    running: n.observed?.running ?? 0,
    revision: n.desired?.revision ?? 0,
    deployedRevision: n.deployedRevision,
    public: (n.endpoints?.length ?? 0) > 0,
    // The first https endpoint; the CLI prints it.
    publicUrl: http ? endpointAddress(http) : undefined,
    endpoints: n.endpoints?.map(endpointView) ?? [],
    error: n.applyError ?? (status === "error" ? n.observed?.error : undefined),
    deploy: step && n.shippedAt ? { step, startedAt: n.shippedAt } : undefined,
    // `stopping`: when Stop was clicked. `stopped`: when the stop shipped.
    stoppedAt: status === "stopped" || status === "stopping" ? n.shippedAt : undefined,
    finishedAt: status === "done" ? n.observed?.finishedAt : undefined,
  };
}
