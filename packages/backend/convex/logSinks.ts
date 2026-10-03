import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import { action, internalMutation, internalQuery, mutation, query } from "./_generated/server";
import { ownedNode, ownedProject } from "./access";
import { axiomVerify, DATASET_RE, DOMAINS } from "./logProviders/axiom";
import { logSink } from "./schema";

// One log sink per project. Absent = Docker default (read from the manager, ship nothing).
// The worker on every node polls /worker/config for the full sink list and streams container
// lines there; logs.tail reads them back. Adding a provider: extend `logSink` in schema.ts,
// add logProviders/<kind>.ts (read side), apps/worker/src/sinks/<kind>.ts (write side), and a
// branch in logs.tail and the worker's sink factory. Nothing else knows the kind.

/** What the UI may see: kind + where, never the token. */
export const get = query({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => {
    if (!(await ownedProject(ctx, projectId))) return null;
    const row = await ctx.db
      .query("logSinks")
      .withIndex("by_project", (q) => q.eq("projectId", projectId))
      .unique();
    if (!row) return null;
    const { sink } = row;
    return {
      kind: sink.kind,
      domain: sink.domain,
      dataset: sink.dataset,
      tokenHint: `…${sink.token.slice(-4)}`,
    };
  },
});

/** For logs.tail: the node's sink (full config) if the caller owns the node, else null. */
export const forNode = internalQuery({
  args: { nodeId: v.id("nodes") },
  handler: async (ctx, { nodeId }) => {
    const scope = await ownedNode(ctx, nodeId);
    if (!scope) return null;
    const row = await ctx.db
      .query("logSinks")
      .withIndex("by_project", (q) => q.eq("projectId", scope.project._id))
      .unique();
    return { sink: row?.sink ?? null };
  },
});

export const owns = internalQuery({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => (await ownedProject(ctx, projectId)) !== null,
});

export const save = internalMutation({
  args: { projectId: v.id("projects"), sink: logSink },
  handler: async (ctx, { projectId, sink }) => {
    if (!(await ownedProject(ctx, projectId))) throw new ConvexError("Project not found");
    const row = await ctx.db
      .query("logSinks")
      .withIndex("by_project", (q) => q.eq("projectId", projectId))
      .unique();
    // A fresh row rather than a patch: its _creationTime is the connect time the worker starts
    // reading containers from (worker.config `since`).
    if (row) await ctx.db.delete(row._id);
    await ctx.db.insert("logSinks", { projectId, sink });
  },
});

/** Verifies the token against Axiom (creates the dataset if needed), then stores the sink. */
export const connectAxiom = action({
  args: {
    projectId: v.id("projects"),
    domain: v.string(),
    dataset: v.string(),
    token: v.string(),
  },
  handler: async (ctx, { projectId, domain, dataset, token }) => {
    if (!(await ctx.runQuery(internal.logSinks.owns, { projectId }))) {
      throw new ConvexError("Project not found");
    }
    const allowLocal = process.env.KEEL_ALLOW_LOCAL_SINKS === "1";
    if (
      !(DOMAINS as readonly string[]).includes(domain) &&
      !(allowLocal && domain.includes("://"))
    ) {
      throw new ConvexError("Region must be US or EU");
    }
    if (!DATASET_RE.test(dataset)) throw new ConvexError("Dataset: letters, digits, - _ . only");
    const trimmed = token.trim();
    if (trimmed.length < 8) throw new ConvexError("That does not look like an Axiom API token");
    const sink = { kind: "axiom" as const, domain, dataset, token: trimmed };
    try {
      await axiomVerify(sink);
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
    await ctx.runMutation(internal.logSinks.save, { projectId, sink });
    return { dataset };
  },
});

/** Back to the Docker default. The worker stops shipping on its next config poll. */
export const disconnect = mutation({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => {
    if (!(await ownedProject(ctx, projectId))) throw new ConvexError("Project not found");
    const row = await ctx.db
      .query("logSinks")
      .withIndex("by_project", (q) => q.eq("projectId", projectId))
      .unique();
    if (row) await ctx.db.delete(row._id);
  },
});
