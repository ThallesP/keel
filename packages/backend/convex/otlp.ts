import { v } from "convex/values";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { type ActionCtx, httpAction, internalMutation, internalQuery } from "./_generated/server";
import type { Ctx } from "./access";
import { type AxiomConfig, base64url, baseUrl } from "./logProviders/axiom";
import { sinkOf } from "./logSinks";

// The OTLP relay (docs/logs.md "Traces"). Services with tracing on and `keel run` export spans
// over OTLP/HTTP to `<site>/otlp/v1/traces` with their environment's ingest key; the relay
// forwards the bytes unchanged to the traces dataset of the environment's organization's sink.
// Apps never hold the sink's token, and a new sink takes effect without a redeploy: the route is
// looked up per request. Traces only: Keel sets OTEL_LOGS_EXPORTER and OTEL_METRICS_EXPORTER to
// `none` (logs come from stdout through the worker). The planned worker receiver can take over
// the in-cluster path later by changing the injected endpoint; apps do not change.

export const KEY_PREFIX = "keel_otlp_";
const MAX_BODY = 4 * 1024 * 1024;
const TYPES = new Set(["application/x-protobuf", "application/json"]);

/** The environment's ingest key, or null before anything asked for one. */
export async function keyFor(ctx: Ctx, environmentId: Id<"environments">) {
  const row = await ctx.db
    .query("otlpKeys")
    .withIndex("by_environment", (q) => q.eq("environmentId", environmentId))
    .first();
  return row?.key ?? null;
}

export const keyOf = internalQuery({
  args: { environmentId: v.id("environments") },
  handler: (ctx, { environmentId }) => keyFor(ctx, environmentId),
});

/** Stores `key` unless the environment got one meanwhile; returns the one that stands. */
export const saveKey = internalMutation({
  args: { environmentId: v.id("environments"), key: v.string() },
  handler: async (ctx, { environmentId, key }) => {
    const existing = await keyFor(ctx, environmentId);
    if (existing) return existing;
    await ctx.db.insert("otlpKeys", { environmentId, key });
    return key;
  },
});

/**
 * The environment's ingest key, made on first use. Random bytes come from an action, like the
 * PKCE verifier of Sign in with Axiom.
 */
export async function ensureKey(ctx: ActionCtx, environmentId: Id<"environments">) {
  const key: string | null = await ctx.runQuery(internal.otlp.keyOf, { environmentId });
  if (key) return key;
  return (await ctx.runMutation(internal.otlp.saveKey, {
    environmentId,
    key: KEY_PREFIX + base64url(crypto.getRandomValues(new Uint8Array(24))),
  })) as string;
}

/**
 * Where spans sent with `key` go: the traces dataset of the sink of the key's organization, or
 * `sink: null` when it has none (no sink, or one from before traces). Null for an unknown key.
 */
export const route = internalQuery({
  args: { key: v.string() },
  handler: async (ctx, { key }): Promise<{ sink: AxiomConfig | null } | null> => {
    const row = await ctx.db
      .query("otlpKeys")
      .withIndex("by_key", (q) => q.eq("key", key))
      .unique();
    if (!row) return null;
    const environment = await ctx.db.get(row.environmentId);
    const project = environment && (await ctx.db.get(environment.projectId));
    if (!project?.organizationId) return null;
    const sink = (await sinkOf(ctx, project.organizationId))?.sink;
    if (sink?.kind !== "axiom" || !sink.traces) return { sink: null };
    return { sink: { domain: sink.domain, dataset: sink.traces, token: sink.token } };
  },
});

/** An empty ExportTraceServiceResponse in the request's encoding: zero protobuf bytes, or `{}`. */
const accepted = (type: string) =>
  new Response(type === "application/json" ? "{}" : null, {
    status: 200,
    headers: { "content-type": type },
  });

/**
 * POST /otlp/v1/traces. Statuses follow OTLP/HTTP: exporters retry 429, 502, 503 and 504 and
 * drop on anything else. With no traces dataset to send to, spans are accepted and dropped, so
 * an exporter does not log a failure every few seconds; `keel traces` says why nothing shows.
 */
export const traces = httpAction(async (ctx, req) => {
  const header = req.headers.get("authorization") ?? "";
  const key = header.startsWith("Bearer ") ? header.slice("Bearer ".length).trim() : "";
  const target = key.startsWith(KEY_PREFIX)
    ? await ctx.runQuery(internal.otlp.route, { key })
    : null;
  if (!target) return new Response("unauthorized", { status: 401 });

  const type = (req.headers.get("content-type") ?? "").split(";")[0]!.trim().toLowerCase();
  if (!TYPES.has(type)) {
    return new Response("OTLP over HTTP: application/x-protobuf or application/json", {
      status: 415,
    });
  }
  if (Number(req.headers.get("content-length") ?? 0) > MAX_BODY) {
    return new Response("too large", { status: 413 });
  }
  const body = await req.arrayBuffer();
  if (body.byteLength > MAX_BODY) return new Response("too large", { status: 413 });
  if (!target.sink) return accepted(type);

  const headers: Record<string, string> = {
    authorization: `Bearer ${target.sink.token}`,
    "x-axiom-dataset": target.sink.dataset,
    "content-type": type,
  };
  const encoding = req.headers.get("content-encoding");
  if (encoding) headers["content-encoding"] = encoding;
  let res: Response;
  try {
    res = await fetch(`${baseUrl(target.sink.domain)}/v1/traces`, {
      method: "POST",
      headers,
      body,
    });
  } catch (err) {
    console.warn(`otlp: Axiom unreachable: ${err instanceof Error ? err.message : String(err)}`);
    return new Response("sink unreachable", { status: 503 });
  }
  if (res.ok) {
    return new Response(await res.arrayBuffer(), {
      status: 200,
      headers: { "content-type": res.headers.get("content-type") ?? type },
    });
  }
  const detail = (await res.text().catch(() => "")).replace(/\s+/g, " ").trim().slice(0, 200);
  console.warn(`otlp: Axiom ${res.status}${detail ? `: ${detail}` : ""}`);
  // Retryable as OTLP defines it, else 503 for any other server error. A 4xx is the payload or
  // the sink's token: retrying would not help, so the exporter drops the batch.
  if ([429, 502, 503, 504].includes(res.status)) {
    return new Response(detail, { status: res.status });
  }
  return new Response(detail || "rejected", { status: res.status >= 500 ? 503 : 400 });
});
