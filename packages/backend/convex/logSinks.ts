import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import {
  action,
  type ActionCtx,
  internalMutation,
  internalQuery,
  mutation,
  query,
} from "./_generated/server";
import { ownedEnvironment, ownedNode, ownedProject } from "./access";
import {
  axiomCanQuery,
  axiomExchange,
  axiomAuthorizeUrl,
  axiomRegisterClient,
  axiomOrgs,
  axiomProvision,
  axiomVerify,
  DATASET_RE,
  DOMAINS,
  type AxiomOrg,
} from "./logProviders/axiom";
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
      org: sink.org ?? null,
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

/** For logs.recent: the environment's sink and the ids of every node that runs on Swarm. */
export const forEnvironment = internalQuery({
  args: { environmentId: v.id("environments") },
  handler: async (ctx, { environmentId }) => {
    const scope = await ownedEnvironment(ctx, environmentId);
    if (!scope) return null;
    const row = await ctx.db
      .query("logSinks")
      .withIndex("by_project", (q) => q.eq("projectId", scope.project._id))
      .unique();
    const nodes = await ctx.db
      .query("nodes")
      .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
      .collect();
    const serviceIds = nodes
      .filter((n) => n.type !== "volume" && n.type !== "group")
      .map((n) => n._id as string);
    return { sink: row?.sink ?? null, serviceIds };
  },
});

export const owns = internalQuery({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => (await ownedProject(ctx, projectId)) !== null,
});

export const projectSlug = internalQuery({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => (await ownedProject(ctx, projectId))?.slug ?? null,
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

// ── Sign in with Axiom ──────────────────────────────────────────────────────────────────────
//
// beginAxiomSignIn makes the PKCE verifier + state here (the browser may be on plain http, where
// WebCrypto is unavailable, and the verifier never needs to leave the server) and returns the
// authorize URL. Axiom redirects to /axiom/callback, which hands `state` + `code` to
// signInAxiom: exchange for a personal token, list orgs, and with a single org provision right
// away. With several, the token waits in `axiomPending` until the Logs page calls chooseAxiomOrg.
// Provisioning (logProviders/axiom.ts axiomProvision) creates `keel-<project slug>` and a token
// scoped to ingest + query on it; the personal token is never stored in logSinks. Both pending
// tables are per project, single use, and expire after 10 minutes.

const PENDING_MS = 10 * 60_000;

export const beginAxiomSignIn = action({
  args: { projectId: v.id("projects"), redirectUri: v.string() },
  handler: async (ctx, { projectId, redirectUri }): Promise<{ url: string }> => {
    let uri: URL;
    try {
      uri = new URL(redirectUri);
    } catch {
      throw new ConvexError("Bad redirect URI");
    }
    if (!/^https?:$/.test(uri.protocol) || uri.pathname !== "/axiom/callback") {
      throw new ConvexError("Bad redirect URI");
    }
    let clientId = await ctx.runQuery(internal.logSinks.clientFor, { redirectUri });
    if (!clientId) {
      try {
        clientId = await axiomRegisterClient(redirectUri);
      } catch (err) {
        throw new ConvexError(err instanceof Error ? err.message : String(err));
      }
      await ctx.runMutation(internal.logSinks.saveClient, { redirectUri, clientId });
    }
    const { state, verifier, url } = await axiomAuthorizeUrl(clientId, redirectUri);
    await ctx.runMutation(internal.logSinks.startSignIn, {
      projectId,
      clientId,
      state,
      verifier,
      redirectUri,
    });
    return { url };
  },
});

export const clientFor = internalQuery({
  args: { redirectUri: v.string() },
  handler: async (ctx, { redirectUri }) =>
    (
      await ctx.db
        .query("axiomClients")
        .withIndex("by_redirect", (q) => q.eq("redirectUri", redirectUri))
        .first()
    )?.clientId ?? null,
});

export const saveClient = internalMutation({
  args: { redirectUri: v.string(), clientId: v.string() },
  handler: async (ctx, args) => {
    const known = await ctx.db
      .query("axiomClients")
      .withIndex("by_redirect", (q) => q.eq("redirectUri", args.redirectUri))
      .first();
    if (!known) await ctx.db.insert("axiomClients", args);
  },
});

export const startSignIn = internalMutation({
  args: {
    projectId: v.id("projects"),
    clientId: v.string(),
    state: v.string(),
    verifier: v.string(),
    redirectUri: v.string(),
  },
  handler: async (ctx, args) => {
    if (!(await ownedProject(ctx, args.projectId))) throw new ConvexError("Project not found");
    const old = await ctx.db
      .query("axiomSignIns")
      .withIndex("by_project", (q) => q.eq("projectId", args.projectId))
      .unique();
    if (old) await ctx.db.delete(old._id);
    const id = await ctx.db.insert("axiomSignIns", args);
    await ctx.scheduler.runAfter(PENDING_MS, internal.logSinks.dropSignIn, { id });
  },
});

/** The started sign-in for `state`, deleted on read. Null if unknown, used, or someone else's. */
export const takeSignIn = internalMutation({
  args: { state: v.string() },
  handler: async (ctx, { state }) => {
    const row = await ctx.db
      .query("axiomSignIns")
      .withIndex("by_state", (q) => q.eq("state", state))
      .unique();
    if (!row || !(await ownedProject(ctx, row.projectId))) return null;
    await ctx.db.delete(row._id);
    return {
      projectId: row.projectId,
      clientId: row.clientId,
      verifier: row.verifier,
      redirectUri: row.redirectUri,
    };
  },
});

export const dropSignIn = internalMutation({
  args: { id: v.id("axiomSignIns") },
  handler: async (ctx, { id }) => {
    if (await ctx.db.get(id)) await ctx.db.delete(id);
  },
});

/** Orgs to choose from while a sign-in with several orgs is pending, else null. Names only. */
export const pendingOrgs = query({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => {
    if (!(await ownedProject(ctx, projectId))) return null;
    const row = await ctx.db
      .query("axiomPending")
      .withIndex("by_project", (q) => q.eq("projectId", projectId))
      .unique();
    return row ? row.orgs.map(({ id, name }) => ({ id, name })) : null;
  },
});

export const stashPending = internalMutation({
  args: {
    projectId: v.id("projects"),
    token: v.string(),
    orgs: v.array(v.object({ id: v.string(), name: v.string(), domain: v.string() })),
  },
  handler: async (ctx, args) => {
    if (!(await ownedProject(ctx, args.projectId))) throw new ConvexError("Project not found");
    const old = await ctx.db
      .query("axiomPending")
      .withIndex("by_project", (q) => q.eq("projectId", args.projectId))
      .unique();
    if (old) await ctx.db.delete(old._id);
    const id = await ctx.db.insert("axiomPending", args);
    await ctx.scheduler.runAfter(PENDING_MS, internal.logSinks.dropPending, { id });
  },
});

/** Hands the pending org pick to the caller and deletes it: one pick per sign-in. */
export const takePending = internalMutation({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => {
    if (!(await ownedProject(ctx, projectId))) return null;
    const row = await ctx.db
      .query("axiomPending")
      .withIndex("by_project", (q) => q.eq("projectId", projectId))
      .unique();
    if (!row) return null;
    await ctx.db.delete(row._id);
    return { token: row.token, orgs: row.orgs };
  },
});

export const dropPending = internalMutation({
  args: { id: v.id("axiomPending") },
  handler: async (ctx, { id }) => {
    if (await ctx.db.get(id)) await ctx.db.delete(id);
  },
});

async function provision(ctx: ActionCtx, projectId: Id<"projects">, token: string, org: AxiomOrg) {
  const slug = await ctx.runQuery(internal.logSinks.projectSlug, { projectId });
  if (!slug) throw new ConvexError("Project not found");
  const name = `keel-${slug}`;
  try {
    const cfg = await axiomProvision(token, org, name, name);
    await axiomCanQuery(cfg);
    await ctx.runMutation(internal.logSinks.save, {
      projectId,
      sink: { kind: "axiom", ...cfg, org: org.name },
    });
    return { dataset: cfg.dataset, org: org.name };
  } catch (err) {
    if (err instanceof ConvexError) throw err;
    throw new ConvexError(err instanceof Error ? err.message : String(err));
  }
}

/** The /axiom/callback step. `{ choose: true }` when the user has to pick an org first. */
export const signInAxiom = action({
  args: { state: v.string(), code: v.string() },
  handler: async (
    ctx,
    { state, code },
  ): Promise<{ choose: true } | { choose: false; dataset: string; org: string }> => {
    const started = await ctx.runMutation(internal.logSinks.takeSignIn, { state });
    if (!started) throw new ConvexError("Axiom sign-in expired, try again");
    const { projectId, clientId, verifier, redirectUri } = started;
    let token: string;
    let orgs: AxiomOrg[];
    try {
      token = await axiomExchange(clientId, code, verifier, redirectUri);
      orgs = await axiomOrgs(token);
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
    const [only] = orgs;
    if (!only) throw new ConvexError("This Axiom account has no organization");
    if (orgs.length === 1)
      return { choose: false, ...(await provision(ctx, projectId, token, only)) };
    await ctx.runMutation(internal.logSinks.stashPending, { projectId, token, orgs });
    return { choose: true };
  },
});

export const chooseAxiomOrg = action({
  args: { projectId: v.id("projects"), orgId: v.string() },
  handler: async (ctx, { projectId, orgId }): Promise<{ dataset: string; org: string }> => {
    const pending = await ctx.runMutation(internal.logSinks.takePending, { projectId });
    if (!pending) throw new ConvexError("Sign-in expired, sign in with Axiom again");
    const org = pending.orgs.find((o) => o.id === orgId);
    if (!org) throw new ConvexError("Organization not found");
    return provision(ctx, projectId, pending.token, org);
  },
});

/** Drops a pending org pick (the user backed out). */
export const cancelAxiomSignIn = mutation({
  args: { projectId: v.id("projects") },
  handler: async (ctx, { projectId }) => {
    if (!(await ownedProject(ctx, projectId))) return;
    const row = await ctx.db
      .query("axiomPending")
      .withIndex("by_project", (q) => q.eq("projectId", projectId))
      .unique();
    if (row) await ctx.db.delete(row._id);
  },
});
