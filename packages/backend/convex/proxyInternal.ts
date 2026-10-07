import { v } from "convex/values";

import { internal } from "./_generated/api";
import { internalMutation, internalQuery } from "./_generated/server";
import { certHint, endpointKey } from "./endpoints";
import { type EndpointStatus, endpointStatus } from "./schema";

// Internal, used by proxy.ts (the sync action) and http.ts (certificate reports).

/** Everything one proxy.sync needs: every endpoint of every node, plus where reports go. */
export const syncInput = internalQuery({
  args: {},
  handler: async (ctx) => {
    // Full scan: exposed nodes are few, and the proxy config is one document for the install.
    const nodes = await ctx.db.query("nodes").collect();
    const routes = nodes.flatMap((n) =>
      (n.endpoints ?? []).map(({ protocol, port, domain, publicPort }) => ({
        nodeId: n._id,
        protocol,
        port,
        domain,
        publicPort,
      })),
    );
    const site =
      process.env.KEEL_PROXY_REPORT_URL || `${process.env.CONVEX_SITE_URL ?? ""}/proxy/events`;
    return {
      routes,
      reporter: { url: site, token: process.env.KEEL_WORKER_TOKEN ?? "" },
      // Development and CI point ACME at a staging CA. KEEL_ACME_EMAIL (install.sh) adds ZeroSSL
      // after Let's Encrypt; with neither set, Caddy uses Let's Encrypt alone (proxy.ts issuers).
      acme: {
        ca: process.env.KEEL_ACME_CA || undefined,
        email: process.env.KEEL_ACME_EMAIL || undefined,
      },
    };
  },
});

/**
 * A sync's outcome. Endpoints changed or removed since the sync read them are left alone, and so
 * is a status that did not change (the cron re-syncs every few minutes).
 */
export const setStatuses = internalMutation({
  args: {
    statuses: v.array(v.object({ nodeId: v.id("nodes"), key: v.string(), status: endpointStatus })),
  },
  handler: async (ctx, { statuses }) => {
    for (const nodeId of new Set(statuses.map((s) => s.nodeId))) {
      const node = await ctx.db.get(nodeId);
      if (!node?.endpoints) continue;
      const next = new Map(
        statuses.filter((s) => s.nodeId === nodeId).map((s) => [s.key, s.status]),
      );
      const same = (a: EndpointStatus, b: EndpointStatus) =>
        a.state === b.state && a.error === b.error;
      const changed = node.endpoints.some((e) => {
        const s = next.get(endpointKey(e));
        return s && !same(s, e.status);
      });
      if (!changed) continue;
      await ctx.db.patch(nodeId, {
        endpoints: node.endpoints.map((e) => {
          const s = next.get(endpointKey(e));
          return { ...e, status: s && !same(s, e.status) ? s : e.status };
        }),
      });
    }
  },
});

/**
 * Cron (crons.ts): sync again while anything is exposed. Retries what failed for a reason that
 * goes away on its own (the proxy restarting, a port freed) and picks up a changed host address.
 * An unchanged config is a no-op in Caddy, so a live endpoint's certificate work is not disturbed.
 */
export const resync = internalMutation({
  args: {},
  handler: async (ctx) => {
    const nodes = await ctx.db.query("nodes").collect();
    if (nodes.some((n) => n.endpoints?.length)) {
      await ctx.scheduler.runAfter(0, internal.proxy.sync, {});
    }
  },
});

/**
 * keel-proxy's event handler reporting a certificate (POST /proxy/events). A failure is final
 * only for this attempt: Caddy keeps retrying, and the next `cert_obtained` flips it live.
 */
export const certReport = internalMutation({
  args: {
    event: v.union(v.literal("cert_obtained"), v.literal("cert_failed")),
    name: v.string(),
    error: v.optional(v.string()),
  },
  handler: async (ctx, { event, name, error }) => {
    const nodes = await ctx.db.query("nodes").collect();
    const at = Date.now();
    for (const node of nodes) {
      if (!node.endpoints?.some((e) => e.protocol === "http" && e.domain === name)) continue;
      await ctx.db.patch(node._id, {
        endpoints: node.endpoints.map((e) =>
          e.protocol === "http" && e.domain === name
            ? {
                ...e,
                status:
                  event === "cert_obtained"
                    ? { state: "live" as const, at }
                    : { state: "failed" as const, error: certHint(error), at },
              }
            : e,
        ),
      });
    }
  },
});
