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

/** Reads one of a node's own variables, references expanded, or `fallback` when it has none. */
type Getter = (key: string, fallback: string) => Promise<string>;

/**
 * What a runtime node offers to references without storing it: a ready-made connection URL,
 * then its overlay HOST and PORT. These are not in the node's own env. Credentials come through
 * `get`, so a password that is itself a reference lands in the URL resolved, not as `${{ … }}`.
 * Userinfo and database name are percent-encoded (RFC 3986): libpq, MySQL, Mongo and every
 * URL-parsing driver decode them, and a `@`, `/` or `#` in a password no longer breaks the URL.
 */
async function provided(node: Doc<"nodes">, get: Getter): Promise<Map<string, Resolved>> {
  const out = new Map<string, Resolved>();
  if (!node.desired) return out;
  const host = serviceHost(node._id);
  const port = node.desired.port;
  const enc = encodeURIComponent;
  switch (node.type) {
    case "database":
      switch (engineOf(node.desired.image)) {
        case "mysql": {
          const [user, pass, db] = await Promise.all([
            get("MYSQL_USER", "app"),
            get("MYSQL_PASSWORD", ""),
            get("MYSQL_DATABASE", "app"),
          ]);
          const value = `mysql://${enc(user)}:${enc(pass)}@${host}:${port ?? 3306}/${enc(db)}`;
          out.set("DATABASE_URL", { value, secret: true });
          break;
        }
        case "mongo": {
          const [user, pass] = await Promise.all([
            get("MONGO_INITDB_ROOT_USERNAME", "app"),
            get("MONGO_INITDB_ROOT_PASSWORD", ""),
          ]);
          const value = `mongodb://${enc(user)}:${enc(pass)}@${host}:${port ?? 27017}`;
          out.set("DATABASE_URL", { value, secret: true });
          break;
        }
        default: {
          const [user, pass, db] = await Promise.all([
            get("POSTGRES_USER", "app"),
            get("POSTGRES_PASSWORD", ""),
            get("POSTGRES_DB", "app"),
          ]);
          const value = `postgres://${enc(user)}:${enc(pass)}@${host}:${port ?? 5432}/${enc(db)}`;
          out.set("DATABASE_URL", { value, secret: true });
        }
      }
      break;
    case "cache": {
      const pass = await get("REDIS_PASSWORD", "");
      const auth = pass ? `default:${enc(pass)}@` : "";
      out.set("REDIS_URL", { value: `redis://${auth}${host}:${port ?? 6379}`, secret: !!pass });
      break;
    }
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
    if (row) {
      const inner = await expand(node, row.value, depth + 1);
      return { value: inner.resolved, secret: row.secret || inner.secret };
    }
    // Only own rows expand here (never provided ones again), so this cannot recurse on itself;
    // `expand` stops at MAX_DEPTH for anything the row's value points to.
    const get: Getter = async (k, fallback) => {
      const r = rows.find((x) => x.key === k);
      return r ? (await expand(node, r.value, depth + 1)).resolved : fallback;
    };
    return (await provided(node, get)).get(key) ?? null;
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

/**
 * Does a reference found in a row of `rowNodeId` point at `node`? `${{ other.KEY }}` names the
 * target; an unqualified `${{ KEY }}` means the row's own node.
 */
function pointsAt(name: string | undefined, rowNodeId: Id<"nodes">, node: Doc<"nodes">) {
  return name === undefined ? rowNodeId === node._id : name === node.name;
}

/** Variable rows anywhere in the environment whose value references `node`. */
async function referencing(ctx: Ctx, node: Doc<"nodes">) {
  const rows: Doc<"variables">[] = [];
  for (const n of await environmentNodes(ctx, node.environmentId)) {
    for (const row of await ownVariables(ctx, n._id)) {
      const refs = [...row.value.matchAll(REF_RE)];
      if (refs.some((m) => pointsAt(m[1], row.nodeId, node))) rows.push(row);
    }
  }
  return rows;
}

/**
 * Something `node` provides changed (vars, port, the node itself): every node whose env depends
 * on it needs a ship. Dependence is transitive (`api` → `worker.QUEUE_URL` → `redis.REDIS_URL`
 * changes when redis's port does), so referrers are walked to a fixpoint. The environment's
 * variables load once; `node` itself is the caller's to mark.
 */
export async function markReferrersDirty(ctx: MutationCtx, node: Doc<"nodes">) {
  const nodes = await environmentNodes(ctx, node.environmentId);
  const byName = new Map(nodes.map((n) => [n.name, n._id]));
  // target node → nodes with a variable that references it
  const referrers = new Map<Id<"nodes">, Set<Id<"nodes">>>();
  for (const n of nodes) {
    for (const row of await ownVariables(ctx, n._id)) {
      for (const m of row.value.matchAll(REF_RE)) {
        const target = m[1] === undefined ? n._id : byName.get(m[1]);
        if (!target || target === n._id) continue;
        let set = referrers.get(target);
        if (!set) referrers.set(target, (set = new Set()));
        set.add(n._id);
      }
    }
  }
  const seen = new Set<Id<"nodes">>([node._id]);
  const queue = [node._id];
  for (let at = 0; at < queue.length; at++) {
    for (const id of referrers.get(queue[at]!) ?? []) {
      if (seen.has(id)) continue;
      seen.add(id);
      queue.push(id);
      await ctx.db.patch(id, { dirty: true });
    }
  }
}

/**
 * Rewrites every reference pointing at `node`; `to` returns the new name and key. Qualified
 * references take both; unqualified ones on the node itself keep their form and take the key.
 */
async function rewriteReferences(
  ctx: MutationCtx,
  node: Doc<"nodes">,
  to: (key: string) => { name: string; key: string },
) {
  for (const row of await referencing(ctx, node)) {
    const value = row.value.replace(REF_RE, (whole, name: string | undefined, key: string) => {
      if (!pointsAt(name, row.nodeId, node)) return whole;
      const next = to(key);
      return name === undefined ? `\${{ ${next.key} }}` : `\${{ ${next.name}.${next.key} }}`;
    });
    if (value !== row.value) await ctx.db.patch(row._id, { value });
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
      // Only the key names and secret flags matter here, so credentials stay unread.
      const keys = [...(await provided(n, async (_k, fallback) => fallback))]
        .filter(([key]) => !own.some((r) => r.key === key))
        .map(([key, { secret }]) => ({ key, secret, provided: true }));
      for (const row of own) keys.push({ key: row.key, secret: row.secret, provided: false });
      sources.push({ nodeId: n._id, name: n.name, type: n.type, image: n.desired?.image, keys });
    }
    return sources;
  },
});

/**
 * Upsert by key. `previousKey` renames that row instead; references to it follow the rename,
 * both `${{ node.KEY }}` elsewhere and `${{ KEY }}` on the node itself.
 */
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
