import { httpRouter } from "convex/server";

import { internal } from "./_generated/api";
import { httpAction } from "./_generated/server";
import { authComponent, createAuth } from "./auth";
import { traces } from "./otlp";

const http = httpRouter();

authComponent.registerRoutes(http, createAuth, { cors: true });

// Routes for the per-node worker (apps/worker, one container per Swarm node). All bearer-
// protected with KEEL_WORKER_TOKEN (constant-time compare). See docs/workers.md.
//   POST /worker/events  Docker events: one object, an array, or NDJSON. Parsed here, mapped to
//                        nodes and turned into observes by events.ingest.
//   GET  /worker/config  Log sinks + the service ids each covers (worker.config).

const MAX_BODY = 256 * 1024;

function timingSafeEqual(a: string, b: string) {
  const x = new TextEncoder().encode(a);
  const y = new TextEncoder().encode(b);
  let diff = x.length ^ y.length;
  for (let i = 0; i < Math.max(x.length, y.length); i++) diff |= (x[i] ?? 0) ^ (y[i] ?? 0);
  return diff === 0;
}

function authorized(req: Request) {
  const expected = process.env.KEEL_WORKER_TOKEN;
  const header = req.headers.get("authorization") ?? "";
  if (!expected || !header.startsWith("Bearer ")) return false;
  return timingSafeEqual(header.slice("Bearer ".length).trim(), expected);
}

function parseEvents(text: string): Record<string, unknown>[] {
  const parsed: unknown = text.trim().startsWith("[")
    ? JSON.parse(text)
    : text
        .split("\n")
        .filter((l) => l.trim().length > 0)
        .map((l) => JSON.parse(l));
  const list = Array.isArray(parsed) ? parsed : [parsed];
  for (const e of list) {
    if (typeof e !== "object" || e === null || !("Type" in e) || !("Action" in e)) {
      throw new SyntaxError("not a Docker event");
    }
  }
  return list as Record<string, unknown>[];
}

const str = (x: unknown) => (typeof x === "string" ? x : undefined);

/** Keep only what events.ingest needs; Docker attributes are free-form and not stored. */
function trim(e: Record<string, unknown>) {
  const actor = e.Actor as { Attributes?: Record<string, unknown> } | undefined;
  const attrs = actor?.Attributes ?? {};
  return {
    type: String(e.Type),
    action: String(e.Action),
    name: str(attrs.name),
    serviceName: str(attrs["com.docker.swarm.service.name"]),
    time: typeof e.time === "number" ? e.time : undefined,
  };
}

http.route({
  path: "/worker/events",
  method: "POST",
  handler: httpAction(async (ctx, req) => {
    if (!authorized(req)) return new Response("unauthorized", { status: 401 });
    const text = await req.text();
    if (text.length > MAX_BODY) return new Response("too large", { status: 413 });
    let events: ReturnType<typeof trim>[];
    try {
      events = parseEvents(text).map(trim);
    } catch {
      return new Response("bad json", { status: 400 });
    }
    // `X-Keel-Resync: 1` is sent by the worker on its first batch after a (re)start.
    const resync = req.headers.get("x-keel-resync") === "1";
    await ctx.runMutation(internal.events.ingest, { events, resync });
    return new Response("ok", { status: 200 });
  }),
});

http.route({
  path: "/worker/config",
  method: "GET",
  handler: httpAction(async (ctx, req) => {
    if (!authorized(req)) return new Response("unauthorized", { status: 401 });
    const config = await ctx.runQuery(internal.worker.config, {});
    return new Response(JSON.stringify(config), {
      status: 200,
      headers: { "content-type": "application/json", "cache-control": "no-store" },
    });
  }),
});

// OTLP/HTTP spans from services with tracing on and from `keel run`, bearer = the environment's
// ingest key (otlp.ts). Relayed unchanged to the organization's traces dataset.
http.route({ path: "/otlp/v1/traces", method: "POST", handler: traces });

export default http;
