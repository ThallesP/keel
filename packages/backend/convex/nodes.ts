import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { mutation, query, type QueryCtx } from "./_generated/server";
import {
  ownedEnvironment,
  ownedNode,
  requireEnvironment,
  requireNode,
  validImage,
  validName,
  validPort,
} from "./access";
import {
  DEFAULTS,
  ENGINES,
  engine,
  engineOf,
  seedVariables,
  uniqueName,
  view,
} from "./nodeHelpers";
import { nodeType, position } from "./schema";
import { beginDeployment } from "./deployments";
import { DEPLOYABLE } from "./status";
import { markReferrersDirty, renameReferences } from "./variables";

/** Names are unique per environment: `${{ name.KEY }}` references resolve by name. */
async function takenNames(ctx: QueryCtx, environmentId: Id<"environments">) {
  const siblings = await ctx.db
    .query("nodes")
    .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
    .collect();
  return new Set(siblings.map((n) => n.name));
}

export const list = query({
  args: { environmentId: v.id("environments") },
  handler: async (ctx, { environmentId }) => {
    if (!(await ownedEnvironment(ctx, environmentId))) return [];
    const nodes = await ctx.db
      .query("nodes")
      .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
      .collect();
    return nodes.map(view);
  },
});

/** `ghcr.io/acme/api-server:1.2` → `api-server`; falls back to the type default. */
function nameFromImage(image: string, fallback: string) {
  const repo = image.split("@")[0]!.split("/").pop()!.split(":")[0]!;
  const slug = repo
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
  return slug || fallback;
}

export const create = mutation({
  args: {
    environmentId: v.id("environments"),
    type: nodeType,
    name: v.optional(v.string()),
    position,
    // service only: image to run. Port comes from the type default; edit later via setDesired.
    image: v.optional(v.string()),
    // database | cache only: picks image + port from ENGINES.
    engine: v.optional(engine),
    // Ship right away. Skipped silently when a deployment is already running (node stays dirty).
    deploy: v.optional(v.boolean()),
  },
  handler: async (
    ctx,
    { environmentId, type, name, position, image, engine: engineKey, deploy },
  ) => {
    await requireEnvironment(ctx, environmentId);
    const taken = await takenNames(ctx, environmentId);
    const defaults = DEFAULTS[type];
    const deployable = DEPLOYABLE.has(type);
    if (image !== undefined && type !== "service") {
      throw new ConvexError("Only services take a custom image");
    }
    if (engineKey !== undefined && ENGINES[engineKey].type !== type) {
      throw new ConvexError(`${engineKey} is not a ${type}`);
    }
    const picked = engineKey ? ENGINES[engineKey] : undefined;
    const finalImage = picked?.image ?? (image === undefined ? undefined : validImage(image));
    // Name after what runs (`nginx`, `api-server`), not the node kind (`service`).
    const runsImage = finalImage ?? ("image" in defaults ? defaults.image : undefined);
    const baseName =
      engineKey ?? (runsImage ? nameFromImage(runsImage, defaults.name) : defaults.name);
    if (name && taken.has(name)) throw new ConvexError(`"${name}" is already taken`);
    const finalName = name ? validName(name) : uniqueName(baseName, taken);
    const desired =
      "image" in defaults
        ? {
            image: finalImage ?? defaults.image,
            revision: 0,
            replicas: 1,
            port: picked?.port ?? defaults.port,
          }
        : undefined;
    const id = await ctx.db.insert("nodes", {
      environmentId,
      type,
      name: finalName,
      position,
      config:
        type === "volume" ? { sizeGb: 10 } : type === "group" ? { width: 300, height: 180 } : {},
      desired,
      dirty: deployable,
    });
    if (type === "database") {
      const rows = seedVariables(engineOf(desired?.image));
      for (const row of rows) await ctx.db.insert("variables", { nodeId: id, ...row });
    }
    let deploymentId: Id<"deployments"> | undefined;
    if (deploy && deployable) {
      try {
        deploymentId = await beginDeployment(ctx, environmentId, { only: [id] });
      } catch (err) {
        if (!(err instanceof ConvexError)) throw err;
      }
    }
    return { id, deploymentId };
  },
});

export const move = mutation({
  args: { id: v.id("nodes"), position },
  handler: async (ctx, { id, position }) => {
    await requireNode(ctx, id);
    await ctx.db.patch(id, { position });
  },
});

export const rename = mutation({
  args: { id: v.id("nodes"), name: v.string() },
  handler: async (ctx, { id, name }) => {
    const { node } = await requireNode(ctx, id);
    if (name === node.name) return;
    validName(name);
    if ((await takenNames(ctx, node.environmentId)).has(name)) {
      throw new ConvexError(`"${name}" is already taken`);
    }
    await renameReferences(ctx, node, name);
    await ctx.db.patch(id, { name });
  },
});

/** Image / port / replicas. Image is user input by design; it is only ever an image ref. */
export const setDesired = mutation({
  args: {
    id: v.id("nodes"),
    image: v.optional(v.string()),
    port: v.optional(v.number()),
    replicas: v.optional(v.number()),
  },
  handler: async (ctx, { id, image, port, replicas }) => {
    const { node } = await requireNode(ctx, id);
    if (!node.desired) throw new ConvexError("This node type has no runtime settings");
    if (replicas !== undefined && (!Number.isInteger(replicas) || replicas < 0 || replicas > 20)) {
      throw new ConvexError("Replicas must be 0–20");
    }
    await ctx.db.patch(id, {
      desired: {
        ...node.desired,
        image: image === undefined ? node.desired.image : validImage(image),
        port: port === undefined ? node.desired.port : validPort(port),
        replicas: replicas ?? node.desired.replicas,
      },
      dirty: true,
    });
    await markReferrersDirty(ctx, node);
  },
});

/** Scale to 0 and ship. The service stays defined; `start` brings it back. */
export const stop = mutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const { node } = await requireNode(ctx, id);
    if (!node.desired) throw new ConvexError("This node type cannot be stopped");
    if (node.desired.replicas === 0) return;
    await ctx.db.patch(id, { desired: { ...node.desired, replicas: 0 }, dirty: true });
    return await beginDeployment(ctx, node.environmentId, { only: [id], verb: "stop" });
  },
});

/** Scale back to 1 replica and ship. Also the "Deploy" for never-shipped nodes. */
export const start = mutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const { node } = await requireNode(ctx, id);
    if (!node.desired) throw new ConvexError("This node type cannot be started");
    const replicas = node.desired.replicas === 0 ? 1 : node.desired.replicas;
    await ctx.db.patch(id, { desired: { ...node.desired, replicas }, dirty: true });
    const verb = node.desired.revision === 0 ? "deploy" : "start";
    return await beginDeployment(ctx, node.environmentId, { only: [id], verb });
  },
});

/**
 * Expose to the internet through a Cloudflare Quick Tunnel: one `cloudflared` Swarm service
 * dialling `svc-<id>:<port>` over the overlay. Immediate, not Ship-gated. The URL is temporary
 * (changes when that tunnel restarts); a named tunnel with a stable hostname is the next provider.
 */
export const expose = mutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const { node } = await requireNode(ctx, id);
    if (node.type !== "service" || !node.desired?.port) {
      throw new ConvexError("Only services with a port can be exposed");
    }
    if (node.public) return;
    const at = Date.now();
    await ctx.db.patch(id, {
      public: { provider: "quick-tunnel", at },
      ingress: { state: "starting", at },
    });
    await ctx.scheduler.runAfter(0, internal.swarm.applyIngress, { id });
  },
});

export const unexpose = mutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const { node } = await requireNode(ctx, id);
    if (!node.public) return;
    await ctx.db.patch(id, { public: undefined, ingress: undefined });
    await ctx.scheduler.runAfter(0, internal.swarm.removeIngress, { id });
  },
});

export const duplicate = mutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const { node } = await requireNode(ctx, id);
    if (node.type === "group") throw new ConvexError("Groups cannot be duplicated");
    const {
      _id,
      _creationTime,
      observed: _o,
      deployedRevision: _d,
      applyError: _e,
      observeScheduled: _s,
      public: _p,
      ingress: _i,
      ...rest
    } = node;
    const taken = await takenNames(ctx, node.environmentId);
    const copyId = await ctx.db.insert("nodes", {
      ...rest,
      name: uniqueName(`${node.name.slice(0, 32)}-copy`, taken),
      position: { x: node.position.x + 40, y: node.position.y + 40 },
      desired: node.desired ? { ...node.desired, revision: 0 } : undefined,
      dirty: DEPLOYABLE.has(node.type),
      shippedAt: undefined,
    });
    const vars = await ctx.db
      .query("variables")
      .withIndex("by_node", (q) => q.eq("nodeId", id))
      .collect();
    for (const { key, value, secret } of vars) {
      await ctx.db.insert("variables", { nodeId: copyId, key, value, secret });
    }
    return copyId;
  },
});

export const remove = mutation({
  args: { id: v.id("nodes") },
  handler: async (ctx, { id }) => {
    const scope = await ownedNode(ctx, id);
    if (!scope) return; // already gone (a multi-select delete can race itself)
    const { node } = scope;
    // References to it now resolve to "" and show as missing; their nodes need a ship.
    await markReferrersDirty(ctx, node);
    const vars = await ctx.db
      .query("variables")
      .withIndex("by_node", (q) => q.eq("nodeId", id))
      .collect();
    for (const row of vars) await ctx.db.delete(row._id);
    const children = await ctx.db
      .query("nodes")
      .withIndex("by_environment", (q) => q.eq("environmentId", node.environmentId))
      .filter((q) => q.eq(q.field("parentId"), id))
      .collect();
    for (const child of children) {
      await ctx.db.patch(child._id, {
        parentId: undefined,
        position: { x: node.position.x + child.position.x, y: node.position.y + child.position.y },
      });
    }
    if (node.observeScheduled) await ctx.scheduler.cancel(node.observeScheduled);
    // Row goes first, in the same transaction as the schedule: an in-flight swarm.apply that
    // re-reads applyInput after this commit sees null and backs out of creating the service.
    await ctx.db.delete(id);
    if (node.desired) {
      await ctx.scheduler.runAfter(0, internal.swarm.remove, { id });
      // A running deployment with a step for this node would otherwise wait for an observe
      // that never comes (events for a deleted node map to nothing) until the timeout.
      await ctx.scheduler.runAfter(0, internal.reconcile.run, {});
    }
  },
});
