import { internal } from "./_generated/api";
import { internalMutation } from "./_generated/server";
import { defaultDomain, publicIp } from "./endpoints";
import type { Endpoint } from "./schema";

/**
 * Run after every deploy (deploy/functions-entrypoint.sh, so on every install and upgrade).
 * Idempotent: each step looks for work and does nothing when there is none.
 *
 * - Quick Tunnel → keel-proxy (2026-10-06): a service exposed through a Cloudflare Quick Tunnel
 *   becomes an https endpoint on its default domain; the `cloudflared` services are removed.
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
    await ctx.scheduler.runAfter(0, internal.swarm.removeLegacyTunnels, {});
    await ctx.scheduler.runAfter(0, internal.proxy.sync, {});
    return { quickTunnelsConverted: converted };
  },
});
