import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Doc, Id } from "./_generated/dataModel";
import { mutation, query, type QueryCtx } from "./_generated/server";
import {
  ownedEnvironment,
  ownedNode,
  requireEnvironment,
  requireUser,
  requireNode,
  validImage,
  validName,
  validPort,
  validReplicas,
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
import {
  allocatePublicPort,
  defaultDomain,
  endpointKey,
  endpointView,
  HTTP_PORTS,
  MAX_ENDPOINTS,
  publicIp,
  requirePublicIp,
  validDomain,
} from "./endpoints";
import { type Endpoint, endpointProtocol, nodeType, position } from "./schema";
import { beginDeployment } from "./deployments";
import { DEPLOYABLE } from "./status";
import { markReferrersDirty, renameReferences } from "./variables";

async function nodesOf(ctx: QueryCtx, environmentId: Id<"environments">) {
  return await ctx.db
    .query("nodes")
    .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
    .collect();
}

/** Names are unique per environment: `${{ name.KEY }}` references resolve by name. */
async function takenNames(ctx: QueryCtx, environmentId: Id<"environments">) {
  return new Set((await nodesOf(ctx, environmentId)).map((n) => n.name));
}

/** Width of a canvas node (node-shell.tsx); groups carry their own in `config.width`. */
const NODE_WIDTH = 220;

/**
 * Where a node created without a position lands (the CLI has no canvas to drop it on): right of
 * the rightmost top-level node, level with it, so it neither stacks at 0,0 nor overlaps.
 */
function nextPosition(nodes: Doc<"nodes">[]) {
  let at: { x: number; y: number } | undefined;
  for (const n of nodes) {
    if (n.parentId) continue; // relative to its group
    const x = n.position.x + (n.config.width ?? NODE_WIDTH) + 60;
    if (!at || x > at.x) at = { x, y: n.position.y };
  }
  return at ?? { x: 0, y: 0 };
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
    // Omitted by the CLI: see nextPosition.
    position: v.optional(position),
    // service only: image to run.
    image: v.optional(v.string()),
    // database | cache only: picks image + port from ENGINES.
    engine: v.optional(engine),
    // service | database | cache: instead of the type's port and 1 replica. Same rules as setDesired.
    port: v.optional(v.number()),
    replicas: v.optional(v.number()),
    // Ship right away. Skipped silently when a deployment is already running (node stays dirty).
    deploy: v.optional(v.boolean()),
  },
  handler: async (
    ctx,
    { environmentId, type, name, position, image, engine: engineKey, port, replicas, deploy },
  ) => {
    await requireEnvironment(ctx, environmentId);
    const siblings = await nodesOf(ctx, environmentId);
    const taken = new Set(siblings.map((n) => n.name));
    const defaults = DEFAULTS[type];
    const deployable = DEPLOYABLE.has(type);
    if (image !== undefined && type !== "service") {
      throw new ConvexError("Only services take a custom image");
    }
    if ((port !== undefined || replicas !== undefined) && !("image" in defaults)) {
      throw new ConvexError("This node type has no runtime settings");
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
            replicas: validReplicas(replicas) ?? 1,
            port: validPort(port) ?? picked?.port ?? defaults.port,
          }
        : undefined;
    const id = await ctx.db.insert("nodes", {
      environmentId,
      type,
      name: finalName,
      position: position ?? nextPosition(siblings),
      config:
        type === "volume" ? { sizeGb: 10 } : type === "group" ? { width: 300, height: 180 } : {},
      desired,
      dirty: deployable,
    });
    if (type === "database" || type === "cache") {
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
    await ctx.db.patch(id, {
      desired: {
        ...node.desired,
        image: image === undefined ? node.desired.image : validImage(image),
        port: port === undefined ? node.desired.port : validPort(port),
        replicas: validReplicas(replicas) ?? node.desired.replicas,
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
 * Open a way in from the internet through keel-proxy on the control plane (docs/networking.md).
 * Immediate, not Ship-gated: proxy.sync loads it right away. With no options: https on an
 * sslip.io domain for a service, tcp on its own port for a database or cache. Exposing what is
 * already exposed returns the existing endpoint; an http endpoint with the same domain, or a
 * tcp/udp one with the same public port, gets the new container port.
 */
export const expose = mutation({
  args: {
    id: v.id("nodes"),
    protocol: v.optional(endpointProtocol),
    // Container port the proxy dials. Default: the node's port.
    port: v.optional(v.number()),
    // http: your own hostname, pointed at the control plane's public IP with an A record.
    domain: v.optional(v.string()),
    // tcp / udp: port on the control plane. Default: the container port when it is free.
    publicPort: v.optional(v.number()),
  },
  handler: async (ctx, { id, ...args }) => {
    const { node } = await requireNode(ctx, id);
    if (
      !node.desired ||
      (node.type !== "service" && node.type !== "database" && node.type !== "cache")
    ) {
      throw new ConvexError("Only services, databases and caches can be exposed");
    }
    const protocol = args.protocol ?? (node.type === "service" ? "http" : "tcp");
    const port = validPort(args.port ?? node.desired.port);
    if (!port) throw new ConvexError("Set the service's port first");
    const ip = requirePublicIp();
    const own = node.endpoints ?? [];
    const others = (await ctx.db.query("nodes").collect())
      .filter((n) => n._id !== id)
      .flatMap((n) => (n.endpoints ?? []).map((e) => ({ ...e, owner: n.name })));

    let wanted: Omit<Endpoint, "status">;
    if (protocol === "http") {
      if (args.publicPort !== undefined)
        throw new ConvexError("HTTP is always served on 80 and 443");
      const domain = args.domain === undefined ? defaultDomain(node, ip) : validDomain(args.domain);
      const holder = others.find((e) => e.protocol === "http" && e.domain === domain);
      if (holder) throw new ConvexError(`${domain} is already used by ${holder.owner}`);
      wanted = { protocol, port, domain };
    } else {
      if (args.domain !== undefined) throw new ConvexError("Only HTTP endpoints have a domain");
      const existing = own.find(
        (e) => e.protocol === protocol && e.port === port && args.publicPort === undefined,
      );
      if (existing) return endpointView(existing);
      const taken = new Set(
        [...others, ...own].filter((e) => e.protocol === protocol).map((e) => e.publicPort!),
      );
      if (protocol === "tcp") for (const p of HTTP_PORTS) taken.add(p);
      const publicPort =
        args.publicPort === undefined
          ? allocatePublicPort(port, taken)
          : validPort(args.publicPort)!;
      if (protocol === "tcp" && HTTP_PORTS.has(publicPort)) {
        throw new ConvexError("80 and 443 serve HTTP; pick another public port");
      }
      const holder = others.find((e) => e.protocol === protocol && e.publicPort === publicPort);
      if (holder)
        throw new ConvexError(`Port ${publicPort}/${protocol} is already used by ${holder.owner}`);
      wanted = { protocol, port, publicPort };
    }

    const key = endpointKey(wanted);
    const same = own.find((e) => endpointKey(e) === key && e.port === wanted.port);
    if (same) return endpointView(same);
    const rest = own.filter((e) => endpointKey(e) !== key);
    if (rest.length >= MAX_ENDPOINTS)
      throw new ConvexError(`At most ${MAX_ENDPOINTS} endpoints per node`);
    const endpoint: Endpoint = {
      ...wanted,
      ...(port !== node.desired.port && { pinnedPort: true }),
      status: { state: "starting", at: Date.now() },
    };
    await ctx.db.patch(id, { endpoints: [...rest, endpoint] });
    await ctx.scheduler.runAfter(0, internal.proxy.sync, {});
    return endpointView(endpoint);
  },
});

/** The control plane's public IP, for "point your domain here" and "open this port" hints. */
export const publicAddress = query({
  args: {},
  handler: async (ctx) => {
    await requireUser(ctx);
    return publicIp() ?? null;
  },
});

/**
 * Close one endpoint (its domain, or protocol + public port), or every one ("Make private").
 * Like expose, immediate.
 */
export const unexpose = mutation({
  args: {
    id: v.id("nodes"),
    protocol: v.optional(endpointProtocol),
    domain: v.optional(v.string()),
    publicPort: v.optional(v.number()),
  },
  handler: async (ctx, { id, protocol, domain, publicPort }) => {
    const { node } = await requireNode(ctx, id);
    // No selector closes every endpoint; a partial one must not fall through to that.
    const all = protocol === undefined && domain === undefined && publicPort === undefined;
    const named = protocol === "http" ? domain !== undefined : publicPort !== undefined;
    if (!all && !(protocol && named)) {
      throw new ConvexError("Name the endpoint: protocol and domain (http) or public port");
    }
    if (!node.endpoints?.length) return;
    const key =
      protocol && endpointKey({ protocol, domain: domain && validDomain(domain), publicPort });
    const endpoints = key ? node.endpoints.filter((e) => endpointKey(e) !== key) : [];
    if (endpoints.length === node.endpoints.length) return;
    await ctx.db.patch(id, { endpoints: endpoints.length > 0 ? endpoints : undefined });
    await ctx.scheduler.runAfter(0, internal.proxy.sync, {});
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
      endpoints: _x,
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
    if (node.endpoints?.length) await ctx.scheduler.runAfter(0, internal.proxy.sync, {});
    if (node.desired) {
      await ctx.scheduler.runAfter(0, internal.swarm.remove, { id });
      // A running deployment with a step for this node would otherwise wait for an observe
      // that never comes (events for a deleted node map to nothing) until the timeout.
      await ctx.scheduler.runAfter(0, internal.reconcile.run, {});
    }
  },
});
