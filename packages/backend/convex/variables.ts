import { ConvexError, v } from "convex/values";

import type { Doc, Id } from "./_generated/dataModel";
import { type MutationCtx, mutation, query } from "./_generated/server";
import { type Ctx, ownedNode, requireNode, validEnvKey } from "./access";
import { engineOf } from "./nodeHelpers";
import { DEPLOYABLE } from "./status";

/**
 * A value can reference another node's variable, Railway-style: `${{ postgres.DATABASE_URL }}`,
 * or one of its own node's with `${{ POSTGRES_USER }}`. Nodes are addressed by name within the
 * environment (names are unique there); renaming a node rewrites the references to it.
 * References resolve at apply time, so every ship sees current values.
 */
const REF_RE = /\$\{\{\s*(?:([a-z0-9-]{1,40})\.)?([A-Z_][A-Z0-9_]{0,63})\s*\}\}/g;
/** Guards reference chains (a → b → a). Deeper or cyclic references resolve to "". */
const MAX_DEPTH = 5;

export type Part =
  | { text: string }
  | { ref: { node?: string; nodeId?: Id<"nodes">; key: string; missing: boolean } };

export type VariableView = {
  key: string;
  /** As typed; may contain references. */
  value: string;
  /** Every reference expanded: what the container gets. */
  resolved: string;
  /** The row's own flag: mask the raw value. */
  secret: boolean;
  /** A referenced value is secret: mask `resolved`. */
  resolvedSecret: boolean;
  parts: Part[];
};

type Resolved = { value: string; secret: boolean };

export const serviceHost = (id: Id<"nodes">) => `svc-${id}`;

async function ownVariables(ctx: Ctx, nodeId: Id<"nodes">) {
  return await ctx.db
    .query("variables")
    .withIndex("by_node", (q) => q.eq("nodeId", nodeId))
    .collect();
}

async function environmentNodes(ctx: Ctx, environmentId: Id<"environments">) {
  return await ctx.db
    .query("nodes")
    .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
    .collect();
}

/**
 * What a runtime node offers to references without storing it: a ready-made connection URL,
 * then its overlay HOST and PORT. These are not in the node's own env.
 */
function provided(node: Doc<"nodes">, own: Doc<"variables">[]): Map<string, Resolved> {
  const out = new Map<string, Resolved>();
  if (!node.desired) return out;
  const host = serviceHost(node._id);
  const port = node.desired.port;
  const get = (k: string, fallback: string) => own.find((r) => r.key === k)?.value ?? fallback;
  switch (node.type) {
    case "database":
      switch (engineOf(node.desired.image)) {
        case "mysql": {
          const user = get("MYSQL_USER", "app");
          const pass = get("MYSQL_PASSWORD", "");
          const db = get("MYSQL_DATABASE", "app");
          const value = `mysql://${user}:${pass}@${host}:${port ?? 3306}/${db}`;
          out.set("DATABASE_URL", { value, secret: true });
          break;
        }
        case "mongo": {
          const user = get("MONGO_INITDB_ROOT_USERNAME", "app");
          const pass = get("MONGO_INITDB_ROOT_PASSWORD", "");
          const value = `mongodb://${user}:${pass}@${host}:${port ?? 27017}`;
          out.set("DATABASE_URL", { value, secret: true });
          break;
        }
        default: {
          const user = get("POSTGRES_USER", "app");
          const pass = get("POSTGRES_PASSWORD", "");
          const db = get("POSTGRES_DB", "app");
          const value = `postgres://${user}:${pass}@${host}:${port ?? 5432}/${db}`;
          out.set("DATABASE_URL", { value, secret: true });
        }
      }
      break;
    case "cache":
      out.set("REDIS_URL", { value: `redis://${host}:${port ?? 6379}`, secret: false });
      break;
    case "service":
      if (port) out.set("URL", { value: `http://${host}:${port}`, secret: false });
      break;
  }
  out.set("HOST", { value: host, secret: false });
  if (port) out.set("PORT", { value: String(port), secret: false });
  return out;
}

/** Expands references within one environment. Each node's variables load at most once. */
async function resolver(ctx: Ctx, environmentId: Id<"environments">) {
  const byName = new Map((await environmentNodes(ctx, environmentId)).map((n) => [n.name, n]));
  const cache = new Map<Id<"nodes">, Promise<Doc<"variables">[]>>();
  const own = (id: Id<"nodes">) => {
    let rows = cache.get(id);
    if (!rows) cache.set(id, (rows = ownVariables(ctx, id)));
    return rows;
  };

  /** `key` on `node`: its own variable (expanded) wins over a provided one. */
  async function lookup(node: Doc<"nodes">, key: string, depth: number): Promise<Resolved | null> {
    const rows = await own(node._id);
    const row = rows.find((r) => r.key === key);
    if (!row) return provided(node, rows).get(key) ?? null;
    const inner = await expand(node, row.value, depth + 1);
    return { value: inner.resolved, secret: row.secret || inner.secret };
  }

  async function expand(node: Doc<"nodes">, value: string, depth = 0) {
    const parts: Part[] = [];
    let resolved = "";
    let secret = false;
    let last = 0;
    for (const m of value.matchAll(REF_RE)) {
      if (m.index > last) parts.push({ text: value.slice(last, m.index) });
      resolved += value.slice(last, m.index);
      last = m.index + m[0].length;
      const [, name, key = ""] = m;
      const target = name === undefined ? node : byName.get(name);
      const hit = target && depth < MAX_DEPTH ? await lookup(target, key, depth) : null;
      parts.push({ ref: { node: name, nodeId: target?._id, key, missing: !hit } });
      if (hit) {
        resolved += hit.value;
        secret ||= hit.secret;
      }
    }
    if (last < value.length) parts.push({ text: value.slice(last) });
    resolved += value.slice(last);
    return { resolved, secret, parts };
  }

  return { own, expand };
}

/** Swarm `Env` for a node: its own variables with every reference expanded. */
export async function computeEnv(ctx: Ctx, node: Doc<"nodes">): Promise<string[]> {
  const { own, expand } = await resolver(ctx, node.environmentId);
  const env: string[] = [];
  for (const row of await own(node._id)) {
    env.push(`${row.key}=${(await expand(node, row.value)).resolved}`);
  }
  return env;
}

/** Variable rows anywhere in the environment whose value references `node` by name. */
async function referencing(ctx: Ctx, node: Doc<"nodes">) {
  const rows: Doc<"variables">[] = [];
  for (const n of await environmentNodes(ctx, node.environmentId)) {
    for (const row of await ownVariables(ctx, n._id)) {
      for (const m of row.value.matchAll(REF_RE)) {
        if (m[1] !== node.name) continue;
        rows.push(row);
        break;
      }
    }
  }
  return rows;
}

/** Something `node` provides changed (vars, port, the node itself): its referrers need a ship. */
export async function markReferrersDirty(ctx: MutationCtx, node: Doc<"nodes">) {
  const ids = new Set((await referencing(ctx, node)).map((r) => r.nodeId));
  ids.delete(node._id);
  for (const id of ids) await ctx.db.patch(id, { dirty: true });
}

/** Rewrites every `${{ node.KEY }}` pointing at `node`; `to` returns the new name and key. */
async function rewriteReferences(
  ctx: MutationCtx,
  node: Doc<"nodes">,
  to: (key: string) => { name: string; key: string },
) {
  for (const row of await referencing(ctx, node)) {
    const value = row.value.replace(REF_RE, (whole, name: string | undefined, key: string) => {
      if (name !== node.name) return whole;
      const next = to(key);
      return `\${{ ${next.name}.${next.key} }}`;
    });
    await ctx.db.patch(row._id, { value });
  }
}

/** Keeps references pointing at the node after a rename. Resolved values do not change. */
export async function renameReferences(ctx: MutationCtx, node: Doc<"nodes">, name: string) {
  await rewriteReferences(ctx, node, (key) => ({ name, key }));
}

export const list = query({
  args: { nodeId: v.id("nodes") },
  handler: async (ctx, { nodeId }): Promise<VariableView[]> => {
    const scope = await ownedNode(ctx, nodeId);
    if (!scope) return [];
    const { own, expand } = await resolver(ctx, scope.node.environmentId);
    const views: VariableView[] = [];
    for (const row of await own(nodeId)) {
      const { resolved, secret, parts } = await expand(scope.node, row.value);
      views.push({
        key: row.key,
        value: row.value,
        resolved,
        secret: row.secret,
        resolvedSecret: secret,
        parts,
      });
    }
    return views;
  },
});

/** What the Reference picker offers: every other runtime node and the keys it can be asked for. */
export const referenceable = query({
  args: { nodeId: v.id("nodes") },
  handler: async (ctx, { nodeId }) => {
    const scope = await ownedNode(ctx, nodeId);
    if (!scope) return [];
    const sources = [];
    for (const n of await environmentNodes(ctx, scope.node.environmentId)) {
      if (n._id === nodeId || n.type === "group" || !DEPLOYABLE.has(n.type)) continue;
      const own = await ownVariables(ctx, n._id);
      const keys = [...provided(n, own)]
        .filter(([key]) => !own.some((r) => r.key === key))
        .map(([key, { secret }]) => ({ key, secret, provided: true }));
      for (const row of own) keys.push({ key: row.key, secret: row.secret, provided: false });
      sources.push({ nodeId: n._id, name: n.name, type: n.type, image: n.desired?.image, keys });
    }
    return sources;
  },
});

/** Upsert by key. `previousKey` renames that row instead; references to it follow the rename. */
export const set = mutation({
  args: {
    nodeId: v.id("nodes"),
    key: v.string(),
    value: v.string(),
    secret: v.boolean(),
    previousKey: v.optional(v.string()),
  },
  handler: async (ctx, { nodeId, key, value, secret, previousKey = key }) => {
    const { node } = await requireNode(ctx, nodeId);
    validEnvKey(key);
    if (value.length > 4096) throw new ConvexError("Value too long");
    const rows = await ownVariables(ctx, nodeId);
    if (previousKey !== key && rows.some((r) => r.key === key)) {
      throw new ConvexError(`${key} already exists`);
    }
    const existing = rows.find((r) => r.key === previousKey);
    if (existing) await ctx.db.patch(existing._id, { key, value, secret });
    else await ctx.db.insert("variables", { nodeId, key, value, secret });
    if (existing && previousKey !== key) {
      await rewriteReferences(ctx, node, (k) => ({
        name: node.name,
        key: k === previousKey ? key : k,
      }));
    }
    await ctx.db.patch(nodeId, { dirty: true });
    await markReferrersDirty(ctx, node);
  },
});

export const remove = mutation({
  args: { nodeId: v.id("nodes"), key: v.string() },
  handler: async (ctx, { nodeId, key }) => {
    const { node } = await requireNode(ctx, nodeId);
    const existing = (await ownVariables(ctx, nodeId)).find((r) => r.key === key);
    if (!existing) return;
    await ctx.db.delete(existing._id);
    await ctx.db.patch(nodeId, { dirty: true });
    await markReferrersDirty(ctx, node);
  },
});
