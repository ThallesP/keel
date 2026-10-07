import { internal } from "./_generated/api";
import { internalMutation } from "./_generated/server";
import { defaultDomain, movedDefaultDomain, publicIp } from "./endpoints";
import { engineOf, randomSecret } from "./nodeHelpers";
import type { Endpoint } from "./schema";
import { markReferrersDirty } from "./variables";

/**
 * Run after every deploy (deploy/functions-entrypoint.sh, so on every install and upgrade).
 * Idempotent: each step looks for work and does nothing when there is none.
 *
 * - Quick Tunnel → keel-proxy (2026-10-06): a service exposed through a Cloudflare Quick Tunnel
 *   becomes an https endpoint on its default domain; the `cloudflared` services are removed.
 * - Redis password (2026-10-07): a Redis without REDIS_PASSWORD gets one and is marked dirty with
 *   everything that references it, so the next Ship restarts it with `--requirepass` and hands
 *   its consumers the new REDIS_URL together.
 * - Public IP changed (install.sh detects it on every run): default sslip.io domains name the IP,
 *   so the old one points nowhere. They move to the current IP, keeping their name.
 * - Every run then syncs keel-proxy, so a fresh or recreated proxy serves what Convex holds.
 */
export const run = internalMutation({
  args: {},
  handler: async (ctx) => {
    const ip = publicIp();
    const at = Date.now();
    let converted = 0;
    for (const node of await ctx.db.query("nodes").collect()) {
      if (node.public === undefined && node.ingress === undefined) continue;
      const port = node.desired?.port;
      const http: Endpoint | undefined =
        node.public && node.type === "service" && port && ip && !node.endpoints?.length
          ? {
              protocol: "http",
              port,
              domain: defaultDomain(node, ip),
              status: { state: "starting", at },
            }
          : undefined;
      if (http) converted++;
      await ctx.db.patch(node._id, {
        public: undefined,
        ingress: undefined,
        ...(http && { endpoints: [http] }),
      });
    }
    let redisPasswords = 0;
    for (const node of await ctx.db.query("nodes").collect()) {
      if (node.type !== "cache" || engineOf(node.desired?.image) !== "redis") continue;
      const rows = await ctx.db
        .query("variables")
        .withIndex("by_node", (q) => q.eq("nodeId", node._id))
        .collect();
      if (rows.some((r) => r.key === "REDIS_PASSWORD")) continue;
      await ctx.db.insert("variables", {
        nodeId: node._id,
        key: "REDIS_PASSWORD",
        value: randomSecret(),
        secret: true,
      });
      await ctx.db.patch(node._id, { dirty: true });
      await markReferrersDirty(ctx, node);
      redisPasswords++;
    }
    let domainsMoved = 0;
    if (ip) {
      for (const node of await ctx.db.query("nodes").collect()) {
        if (!node.endpoints?.length) continue;
        let moved = false;
        const endpoints = node.endpoints.map((e) => {
          const domain = e.protocol === "http" && movedDefaultDomain(node, e.domain!, ip);
          if (!domain) return e;
          moved = true;
          domainsMoved++;
          return { ...e, domain, status: { state: "starting" as const, at } };
        });
        if (moved) await ctx.db.patch(node._id, { endpoints });
      }
    }
    await ctx.scheduler.runAfter(0, internal.swarm.removeLegacyTunnels, {});
    await ctx.scheduler.runAfter(0, internal.proxy.sync, {});
    return { quickTunnelsConverted: converted, redisPasswords, domainsMoved };
  },
});
