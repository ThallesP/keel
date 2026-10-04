import { ConvexError, v } from "convex/values";

import { api, internal } from "./_generated/api";
import type { Doc } from "./_generated/dataModel";
import {
  action,
  type ActionCtx,
  internalMutation,
  internalQuery,
  mutation,
  type MutationCtx,
  query,
} from "./_generated/server";
import {
  type Ctx,
  currentMembership,
  NO_ORGANIZATION,
  ownedEnvironment,
  ownedNode,
} from "./access";
import {
  axiomCanQuery,
  axiomChosenOrg,
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
import { axiomOrg, logSink } from "./schema";

// One log sink per organization, shared by every project in it: set up once, and a new project
// ships there from its first deploy. Absent = Docker default (read from the manager, ship
// nothing). The worker on every node polls /worker/config for the sink and the services of
// every project, and streams container lines there; logs.tail reads them back. Adding a
// provider: extend `logSink` in schema.ts, add logProviders/<kind>.ts (read side),
// apps/worker/src/sinks/<kind>.ts (write side), and a branch in logs.tail and the worker's sink
// factory. Nothing else knows the kind.

/** The org's rows from before sinks were per organization (one per project), newest first. */
async function legacyRows(ctx: Ctx, organizationId: string) {
  const rows = await ctx.db
    .query("logSinks")
    .withIndex("by_organization", (q) => q.eq("organizationId", undefined))
    .order("desc")
    .collect();
  const ours: Doc<"logSinks">[] = [];
  for (const row of rows) {
    const project = row.projectId && (await ctx.db.get(row.projectId));
    if (project && project.organizationId === organizationId) ours.push(row);
  }
  return ours;
}

/**
 * The organization's sink row, or null. Until a sign-in or Disconnect replaces them, rows from
 * before sinks were per organization stand in: the newest of its projects' becomes the org's.
 */
export async function sinkOf(ctx: Ctx, organizationId: string) {
  const row = await ctx.db
    .query("logSinks")
    .withIndex("by_organization", (q) => q.eq("organizationId", organizationId))
    .unique();
  return row ?? (await legacyRows(ctx, organizationId))[0] ?? null;
}

/** The sink of a project the caller owns (ownedProject only hands out the caller's org's). */
const projectSink = (ctx: Ctx, project: Doc<"projects">) =>
  project.organizationId ? sinkOf(ctx, project.organizationId) : null;

/** Deletes the organization's sink and its rows from before sinks were per organization. */
async function clear(ctx: MutationCtx, organizationId: string) {
  const row = await ctx.db
    .query("logSinks")
    .withIndex("by_organization", (q) => q.eq("organizationId", organizationId))
    .unique();
  if (row) await ctx.db.delete(row._id);
  for (const legacy of await legacyRows(ctx, organizationId)) await ctx.db.delete(legacy._id);
}

async function requireOrganization(ctx: Ctx) {
  const membership = await currentMembership(ctx);
  if (!membership) throw new ConvexError(NO_ORGANIZATION);
  return membership.organizationId;
}

/** The caller's organization's sink as the UI may see it: kind + where, never the token. */
export const get = query({
  args: {},
  handler: async (ctx) => {
    const membership = await currentMembership(ctx);
    if (!membership) return null;
    const row = await sinkOf(ctx, membership.organizationId);
    if (!row) return null;
    const { sink } = row;
    return {
      kind: sink.kind,
      domain: sink.domain,
      dataset: sink.dataset,
      traces: sink.traces ?? null,
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
    const row = await projectSink(ctx, scope.project);
    return { sink: row?.sink ?? null };
  },
});

/** For logs.recent: the environment's sink and the ids of every node that runs on Swarm. */
export const forEnvironment = internalQuery({
  args: { environmentId: v.id("environments") },
  handler: async (ctx, { environmentId }) => {
    const scope = await ownedEnvironment(ctx, environmentId);
    if (!scope) return null;
    const row = await projectSink(ctx, scope.project);
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

/** The caller's organization's sink becomes `sink`, replacing whatever it had. */
export const save = internalMutation({
  args: { sink: logSink },
  handler: async (ctx, { sink }) => {
    const organizationId = await requireOrganization(ctx);
    // A fresh row rather than a patch: its _creationTime is the connect time the worker starts
    // reading containers from (worker.config `since`).
    await clear(ctx, organizationId);
    await ctx.db.insert("logSinks", { organizationId, sink });
  },
});

/**
 * Verifies the token against Axiom (creates the datasets if needed), then makes it the
 * organization's sink. `traces` is the traces dataset; the token needs ingest + query on it too.
 */
export const connectAxiom = action({
  args: {
    domain: v.string(),
    dataset: v.string(),
    traces: v.optional(v.string()),
    token: v.string(),
  },
  handler: async (ctx, { domain, dataset, traces, token }) => {
    if (!(await ctx.runQuery(api.organizations.current, {}))) {
      throw new ConvexError(NO_ORGANIZATION);
    }
    const allowLocal = process.env.KEEL_ALLOW_LOCAL_SINKS === "1";
    if (
      !(DOMAINS as readonly string[]).includes(domain) &&
      !(allowLocal && domain.includes("://"))
    ) {
      throw new ConvexError("Region must be US or EU");
    }
    for (const name of traces === undefined ? [dataset] : [dataset, traces]) {
      if (!DATASET_RE.test(name)) throw new ConvexError("Dataset: letters, digits, - _ . only");
    }
    const trimmed = token.trim();
    if (trimmed.length < 8) throw new ConvexError("That does not look like an Axiom API token");
    const sink = { kind: "axiom" as const, domain, dataset, traces, token: trimmed };
    try {
      await axiomVerify(sink);
      if (traces) await axiomVerify({ ...sink, dataset: traces });
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
    await ctx.runMutation(internal.logSinks.save, { sink });
    return { dataset, traces: traces ?? null };
  },
});

/** Back to the Docker default, for every project. The worker stops shipping on its next poll. */
export const disconnect = mutation({
  args: {},
  handler: async (ctx) => {
    await clear(ctx, await requireOrganization(ctx));
  },
});

// ── Sign in with Axiom ──────────────────────────────────────────────────────────────────────
//
// beginAxiomSignIn makes the PKCE verifier + state here (the browser may be on plain http, where
// WebCrypto is unavailable, and the verifier never needs to leave the server) and returns the
// authorize URL. Axiom redirects to /axiom/callback, which hands `state` + `code` to
// signInAxiom: exchange for a personal token, list orgs, and provision right away in the org
// picked on Axiom's consent page (named by the token) or the only one. Failing both, the token
// waits in `axiomPending` until the Observability page calls chooseAxiomOrg.
// Provisioning (logProviders/axiom.ts axiomProvision) creates `keel-logs` and `keel-traces` if
// missing and a token scoped to ingest + query on those two, named after the Keel organization;
// the personal token is never stored in logSinks. Signing in again replaces the sink (that is how
// sinks from before traces get their traces dataset). Both pending tables are per organization,
// single use, and expire after 10 minutes.

const PENDING_MS = 10 * 60_000;

export const beginAxiomSignIn = action({
  args: { redirectUri: v.string() },
  handler: async (ctx, { redirectUri }): Promise<{ url: string }> => {
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
    clientId: v.string(),
    state: v.string(),
    verifier: v.string(),
    redirectUri: v.string(),
  },
  handler: async (ctx, args) => {
    const organizationId = await requireOrganization(ctx);
    const old = await ctx.db
      .query("axiomSignIns")
      .withIndex("by_organization", (q) => q.eq("organizationId", organizationId))
      .unique();
    if (old) await ctx.db.delete(old._id);
    const id = await ctx.db.insert("axiomSignIns", { organizationId, ...args });
    await ctx.scheduler.runAfter(PENDING_MS, internal.logSinks.dropSignIn, { id });
  },
});

/** The started sign-in for `state`, deleted on read. Null if unknown, used, or another org's. */
export const takeSignIn = internalMutation({
  args: { state: v.string() },
  handler: async (ctx, { state }) => {
    const row = await ctx.db
      .query("axiomSignIns")
      .withIndex("by_state", (q) => q.eq("state", state))
      .unique();
    const membership = await currentMembership(ctx);
    if (!row || row.organizationId !== membership?.organizationId) return null;
    await ctx.db.delete(row._id);
    return { clientId: row.clientId, verifier: row.verifier, redirectUri: row.redirectUri };
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
  args: {},
  handler: async (ctx) => {
    const membership = await currentMembership(ctx);
    if (!membership) return null;
    const row = await ctx.db
      .query("axiomPending")
      .withIndex("by_organization", (q) => q.eq("organizationId", membership.organizationId))
      .unique();
    return row ? row.orgs.map(({ id, name }) => ({ id, name })) : null;
  },
});

export const stashPending = internalMutation({
  args: { token: v.string(), orgs: v.array(axiomOrg) },
  handler: async (ctx, args) => {
    const organizationId = await requireOrganization(ctx);
    const old = await ctx.db
      .query("axiomPending")
      .withIndex("by_organization", (q) => q.eq("organizationId", organizationId))
      .unique();
    if (old) await ctx.db.delete(old._id);
    const id = await ctx.db.insert("axiomPending", { organizationId, ...args });
    await ctx.scheduler.runAfter(PENDING_MS, internal.logSinks.dropPending, { id });
  },
});

/** Hands the pending org pick to the caller and deletes it: one pick per sign-in. */
export const takePending = internalMutation({
  args: {},
  handler: async (ctx) => {
    const membership = await currentMembership(ctx);
    if (!membership) return null;
    const row = await ctx.db
      .query("axiomPending")
      .withIndex("by_organization", (q) => q.eq("organizationId", membership.organizationId))
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

async function provision(ctx: ActionCtx, token: string, org: AxiomOrg) {
  const keel = await ctx.runQuery(api.organizations.current, {});
  if (!keel) throw new ConvexError(NO_ORGANIZATION);
  try {
    const cfg = await axiomProvision(token, org, `keel-${keel.slug}`);
    for (const dataset of [cfg.dataset, cfg.traces]) {
      await axiomCanQuery({ ...cfg, dataset }).catch((err: Error) => {
        throw new Error(`Querying ${dataset}: ${err.message}`);
      });
    }
    await ctx.runMutation(internal.logSinks.save, {
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
    const { clientId, verifier, redirectUri } = started;
    let token: string;
    let orgs: AxiomOrg[];
    try {
      token = await axiomExchange(clientId, code, verifier, redirectUri);
      orgs = await axiomOrgs(token);
    } catch (err) {
      throw new ConvexError(err instanceof Error ? err.message : String(err));
    }
    if (orgs.length === 0) throw new ConvexError("This Axiom account has no organization");
    const chosen = axiomChosenOrg(token);
    const org = orgs.length === 1 ? orgs[0] : orgs.find((o) => o.id === chosen);
    if (org) return { choose: false, ...(await provision(ctx, token, org)) };
    await ctx.runMutation(internal.logSinks.stashPending, { token, orgs });
    return { choose: true };
  },
});

export const chooseAxiomOrg = action({
  args: { orgId: v.string() },
  handler: async (ctx, { orgId }): Promise<{ dataset: string; org: string }> => {
    const pending = await ctx.runMutation(internal.logSinks.takePending, {});
    if (!pending) throw new ConvexError("Sign-in expired, sign in with Axiom again");
    const org = pending.orgs.find((o) => o.id === orgId);
    if (!org) throw new ConvexError("Organization not found");
    return provision(ctx, pending.token, org);
  },
});

/** Drops a pending org pick (the user backed out). */
export const cancelAxiomSignIn = mutation({
  args: {},
  handler: async (ctx) => {
    const membership = await currentMembership(ctx);
    if (!membership) return;
    const row = await ctx.db
      .query("axiomPending")
      .withIndex("by_organization", (q) => q.eq("organizationId", membership.organizationId))
      .unique();
    if (row) await ctx.db.delete(row._id);
  },
});
