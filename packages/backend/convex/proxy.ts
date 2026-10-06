"use node";

import http from "node:http";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { internalAction } from "./_generated/server";
import { certHint, endpointKey } from "./endpoints";
import type { EndpointStatus } from "./schema";

// keel-proxy (apps/proxy): Caddy + caddy-l4 on the control plane, the one way in from the
// internet (docs/networking.md). Convex owns its whole configuration and pushes it through the
// admin API on a unix socket that only this container shares (deploy/compose.yml); the overlay,
// where user containers live, cannot reach it.
const SOCKET = process.env.KEEL_PROXY_SOCKET || "/run/keel-proxy/admin.sock";

type Route = {
  nodeId: Id<"nodes">;
  protocol: "http" | "tcp" | "udp";
  port: number;
  domain?: string;
  publicPort?: number;
};
type Reporter = { url: string; token: string };
type Acme = { ca?: string; email?: string };
type Cert = { state: "ok" | "failed" | "pending"; error?: string };

function admin(method: string, path: string, body?: unknown) {
  return new Promise<{ status: number; text: string }>((resolve, reject) => {
    const req = http.request(
      {
        socketPath: SOCKET,
        method,
        path,
        headers: body === undefined ? {} : { "content-type": "application/json" },
      },
      (res) => {
        let text = "";
        res.setEncoding("utf8");
        res.on("data", (chunk) => (text += chunk));
        res.on("end", () => resolve({ status: res.statusCode ?? 0, text }));
      },
    );
    req.setTimeout(30_000, () => req.destroy(new Error("keel-proxy did not answer within 30s")));
    req.on("error", (err: NodeJS.ErrnoException) =>
      reject(
        err.code === "ENOENT" || err.code === "ECONNREFUSED"
          ? new Error(`keel-proxy is not running (no admin socket at ${SOCKET})`)
          : err,
      ),
    );
    if (body !== undefined) req.write(JSON.stringify(body));
    req.end();
  });
}

async function adminJSON<T>(path: string): Promise<T> {
  const res = await admin("GET", path);
  if (res.status >= 300) throw new Error(caddyError(res.text));
  return JSON.parse(res.text) as T;
}

/** Caddy answers errors as `{"error": "loading config: loading new config: …"}`. */
function caddyError(text: string) {
  let message = text;
  try {
    message = (JSON.parse(text) as { error?: string }).error ?? text;
  } catch {
    // not JSON; use as is
  }
  return message.replace(/^(loading (new )?config: )+/, "").trim();
}

const hostPort = (addr: string, port: number) =>
  addr.includes(":") ? `[${addr}]:${port}` : `${addr}:${port}`;
const upstream = (r: Route) => `svc-${r.nodeId}:${r.port}`;
const internalName = (d: string) => d === "localhost" || d.endsWith(".localhost");

/**
 * The `apps` half of Caddy's config for every endpoint. Listeners use the plugin's `host-tcp` /
 * `host-udp` networks (sockets in the host namespace) on each address from /keel/host-addrs:
 * never a wildcard, `tailscale serve` holds 443 on the tailnet address.
 */
function caddyApps(routes: Route[], addrs: string[], reporter: Reporter, acme: Acme) {
  const apps: Record<string, unknown> = {};
  const web = routes.filter((r) => r.protocol === "http");
  const raw = routes.filter((r) => r.protocol !== "http");
  if (web.length > 0) {
    apps.http = {
      servers: {
        // Automatic HTTPS manages a certificate per host matcher and adds the :80 server (same
        // network and addresses) that redirects to HTTPS and answers ACME HTTP-01 challenges.
        public: {
          listen: addrs.map((a) => `host-tcp/${hostPort(a, 443)}`),
          // HTTP/3 would need a UDP 443 listener in the host namespace too; not yet.
          protocols: ["h1", "h2"],
          routes: web.map((r) => ({
            match: [{ host: [r.domain] }],
            handle: [{ handler: "reverse_proxy", upstreams: [{ dial: upstream(r) }] }],
            terminal: true,
          })),
        },
      },
    };
    const managed = web.map((r) => r.domain!).filter((d) => !internalName(d));
    if ((acme.ca || acme.email) && managed.length > 0) {
      apps.tls = {
        automation: {
          policies: [
            {
              subjects: managed,
              issuers: [
                {
                  module: "acme",
                  ...(acme.ca && { ca: acme.ca }),
                  ...(acme.email && { email: acme.email }),
                },
              ],
            },
          ],
        },
      };
    }
    apps.events = {
      subscriptions: [
        {
          events: ["cert_obtained", "cert_failed"],
          handlers: [{ handler: "keel", url: reporter.url, token: reporter.token }],
        },
      ],
    };
  }
  if (raw.length > 0) {
    apps.layer4 = {
      servers: Object.fromEntries(
        raw.map((r) => [
          `${r.protocol}-${r.publicPort}`,
          {
            listen: addrs.map((a) => `host-${r.protocol}/${hostPort(a, r.publicPort!)}`),
            routes: [
              {
                handle: [
                  {
                    handler: "proxy",
                    upstreams: [{ dial: [`${r.protocol === "udp" ? "udp/" : ""}${upstream(r)}`] }],
                  },
                ],
              },
            ],
          },
        ]),
      ),
    };
  }
  return apps;
}

/**
 * The endpoints a failed load is about. Caddy loads all or nothing and names the listener that
 * would not bind (`listen tcp 192.0.2.1:5432: bind: address already in use`): that endpoint
 * fails alone and the rest load without it. 80/443 belong to every http endpoint.
 */
function blame(message: string, routes: Route[]) {
  const blamed = new Map<string, string>();
  const m = /listen (tcp|udp) \S*?:(\d+): (.+?)(?:$|\n)/.exec(message);
  if (!m) return blamed;
  const [, protocol, rawPort, reason] = m;
  const port = Number(rawPort);
  const why = reason!.includes("address already in use")
    ? `port ${port}/${protocol} is already in use on the control plane`
    : `cannot listen on ${port}/${protocol}: ${reason}`;
  for (const r of routes) {
    const web = protocol === "tcp" && (port === 80 || port === 443) && r.protocol === "http";
    if (web || (r.protocol === protocol && r.publicPort === port)) {
      blamed.set(endpointKey(r), `${why[0]!.toUpperCase()}${why.slice(1)}`);
    }
  }
  return blamed;
}

const errorText = (err: unknown) =>
  (err instanceof Error ? err.message : String(err)).replace(/\s+/g, " ").trim().slice(0, 300);

/** Load the config for `routes`, then say how each endpoint stands. */
async function apply(routes: Route[], reporter: Reporter, acme: Acme) {
  const at = Date.now();
  const failed = new Map<string, string>();
  const statusOf = (r: Route, status: EndpointStatus) => ({
    nodeId: r.nodeId,
    key: endpointKey(r),
    status,
  });
  try {
    const addrs = routes.length > 0 ? await adminJSON<string[]>("/keel/host-addrs") : [];
    if (routes.length > 0 && addrs.length === 0) {
      throw new Error("keel-proxy found no public network address on the control plane");
    }
    // Each failed pass takes at least one endpoint out, so this ends.
    for (;;) {
      const live = routes.filter((r) => !failed.has(endpointKey(r)));
      const res = await admin("POST", "/config/apps", caddyApps(live, addrs, reporter, acme));
      if (res.status < 300) break;
      const message = caddyError(res.text);
      const blamed = blame(message, live);
      if (blamed.size === 0) throw new Error(message);
      for (const [key, why] of blamed) failed.set(key, why);
    }
  } catch (err) {
    // Proxy unreachable, or an error no endpoint can own: it keeps serving its previous config.
    const error = errorText(err);
    return routes.map((r) => statusOf(r, { state: "failed", error, at }));
  }
  const domains = routes
    .filter((r) => r.protocol === "http" && !failed.has(endpointKey(r)))
    .map((r) => r.domain!);
  const certs =
    domains.length > 0
      ? await adminJSON<Record<string, Cert>>(
          `/keel/certs?${domains.map((d) => `name=${encodeURIComponent(d)}`).join("&")}`,
        ).catch(() => ({}) as Record<string, Cert>)
      : {};
  return routes.map((r) => {
    const error = failed.get(endpointKey(r));
    if (error) return statusOf(r, { state: "failed", error, at });
    if (r.protocol !== "http") return statusOf(r, { state: "live", at });
    const cert = certs[r.domain!];
    // `pending` stays `starting`: the `keel` event handler reports the outcome to /proxy/events.
    if (cert?.state === "ok") return statusOf(r, { state: "live", at });
    if (cert?.state === "failed")
      return statusOf(r, { state: "failed", error: certHint(cert.error), at });
    return statusOf(r, { state: "starting", at });
  });
}

/**
 * Make keel-proxy serve exactly the endpoints in Convex. Idempotent and whole-config: scheduled
 * after every expose, unexpose and node delete, and by migrations.run on every install. Two
 * syncs can overlap; each re-reads after loading and goes again if the endpoints moved, so the
 * last one to finish always loads the latest set.
 */
export const sync = internalAction({
  args: {},
  handler: async (ctx) => {
    for (let pass = 0; pass < 3; pass++) {
      const { routes, reporter, acme } = await ctx.runQuery(internal.proxyInternal.syncInput, {});
      const statuses = await apply(routes, reporter, acme);
      await ctx.runMutation(internal.proxyInternal.setStatuses, { statuses });
      const after = await ctx.runQuery(internal.proxyInternal.syncInput, {});
      if (JSON.stringify(after.routes) === JSON.stringify(routes)) return;
    }
  },
});
