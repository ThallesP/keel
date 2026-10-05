import { ConvexError, v } from "convex/values";

import { internal } from "./_generated/api";
import type { Doc } from "./_generated/dataModel";
import { action, internalMutation, internalQuery, query, type QueryCtx } from "./_generated/server";
import { type Ctx, ownedEnvironment, ownedNode, requireNode } from "./access";
import { sinkOf } from "./logSinks";
import { ensureKey, KEY_PREFIX, keyFor } from "./otlp";
import { agentPrompt } from "./tracingPrompt";

// Tracing for a service (docs/logs.md "Traces"). A per-service switch, `desired.tracing`, staged
// like a variable: on the next ship, apply gives the container the OTEL_* variables below, which
// point any standard OpenTelemetry SDK at the OTLP relay (otlp.ts) with the environment's ingest
// key. `keel run` gets the same set for a local run. The app's own variables win over these.
// Getting the app instrumented is the agent prompt's job (tracingPrompt.ts).

export const NO_SINK = "Connect Axiom to see traces";
export const NO_TRACES = "Sign in with Axiom again to turn on traces";

const HEADERS = "OTEL_EXPORTER_OTLP_HEADERS";

/** Where deployed services send spans: the relay on this control plane. */
function endpoint() {
  const site = process.env.KEEL_OTLP_URL || `${process.env.CONVEX_SITE_URL ?? ""}/otlp`;
  return site.replace(/\/+$/, "");
}

/**
 * The OTEL_* a traced process gets, in order. `endpoint` null: `keel run`, which adds the address
 * its machine reaches the control plane at. A local run is tagged `deployment.environment.name=
 * local` and is otherwise the deployed service (same service id, same environment).
 */
export function tracingEnv(
  node: Doc<"nodes">,
  environment: Doc<"environments">,
  key: string,
  { local }: { local: boolean },
): [string, string][] {
  const resource = [
    ["keel.service_id", node._id],
    ["keel.environment_id", environment._id],
    ["deployment.environment.name", local ? "local" : environment.name],
  ]
    .map(([k, value]) => `${k}=${encodeURIComponent(value!)}`)
    .join(",");
  return [
    ...(local ? [] : [["OTEL_EXPORTER_OTLP_ENDPOINT", endpoint()] as [string, string]]),
    ["OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"],
    [HEADERS, `Authorization=Bearer%20${key}`],
    ["OTEL_SERVICE_NAME", node.name],
    ["OTEL_RESOURCE_ATTRIBUTES", resource],
    ["OTEL_TRACES_EXPORTER", "otlp"],
    ["OTEL_METRICS_EXPORTER", "none"],
    ["OTEL_LOGS_EXPORTER", "none"],
  ];
}

const keyOfEnv = (env: string) => env.slice(0, env.indexOf("="));

const ENDPOINTS = ["OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"];

/**
 * Whether the service's own variables replace Keel's `k`: key by key, except that the ingest key
 * only goes to Keel's endpoint, so a service that names its own endpoint gets no Keel headers.
 * `keel run` applies the same rule (`runEnv` in apps/cli).
 */
function overridden(own: Set<string>, k: string) {
  return own.has(k) || (k === HEADERS && ENDPOINTS.some((e) => own.has(e)));
}

/** A node's Swarm env (`KEY=value`) plus its tracing variables when it has tracing on. */
export async function withTracing(ctx: QueryCtx, node: Doc<"nodes">, env: string[]) {
  if (!node.desired?.tracing) return env;
  const [environment, key] = await Promise.all([
    ctx.db.get(node.environmentId),
    keyFor(ctx, node.environmentId),
  ]);
  // tracing.enable makes the key before it sets the switch; without one there is nothing to send.
  if (!environment || !key) return env;
  const own = new Set(env.map(keyOfEnv));
  const added = tracingEnv(node, environment, key, { local: false })
    .filter(([k]) => !overridden(own, k))
    .map(([k, value]) => `${k}=${value}`);
  return [...env, ...added];
}

type TracesState = "off" | "old" | "on";

/** Whether the organization can store traces: no sink, a sink from before traces, or yes. */
async function tracesState(ctx: Ctx, project: Doc<"projects">): Promise<TracesState> {
  const sink = project.organizationId ? (await sinkOf(ctx, project.organizationId))?.sink : null;
  if (sink?.kind !== "axiom") return "off";
  return sink.traces ? "on" : "old";
}

function requireTraces(state: TracesState) {
  if (state === "off") throw new ConvexError(NO_SINK);
  if (state === "old") throw new ConvexError(NO_TRACES);
}

/** For the actions below: the caller's service, its environment, and the traces state. */
export const scope = internalQuery({
  args: { nodeId: v.id("nodes") },
  handler: async (ctx, { nodeId }) => {
    const { node, environment, project } = await requireNode(ctx, nodeId);
    if (node.type !== "service" || !node.desired) {
      throw new ConvexError("Only services can be traced");
    }
    return { node, environment, traces: await tracesState(ctx, project) };
  },
});

export const setEnabled = internalMutation({
  args: { nodeId: v.id("nodes"), on: v.boolean() },
  handler: async (ctx, { nodeId, on }) => {
    const { node } = await requireNode(ctx, nodeId);
    if (!node.desired || (node.desired.tracing ?? false) === on) return;
    const { tracing: _, ...desired } = node.desired;
    await ctx.db.patch(nodeId, {
      desired: on ? { ...desired, tracing: true } : desired,
      dirty: true,
    });
  },
});

/**
 * Turns tracing on or off for a service. Staged: the container gets (or loses) the variables on
 * the next ship. Turning it on needs somewhere to send spans, and makes the ingest key.
 */
export const enable = action({
  args: { nodeId: v.id("nodes"), on: v.boolean() },
  handler: async (ctx, { nodeId, on }) => {
    const { environment, traces } = await ctx.runQuery(internal.tracing.scope, { nodeId });
    if (on) {
      requireTraces(traces);
      await ensureKey(ctx, environment._id);
    }
    await ctx.runMutation(internal.tracing.setEnabled, { nodeId, on });
  },
});

/** The service's tracing as its Settings tab shows it. The ingest key is masked. */
export const forNode = query({
  args: { nodeId: v.id("nodes") },
  handler: async (ctx, { nodeId }) => {
    const scope = await ownedNode(ctx, nodeId);
    const desired = scope?.node.desired;
    if (!scope || scope.node.type !== "service" || !desired) return null;
    const { node, environment, project } = scope;
    const key = await keyFor(ctx, environment._id);
    const own = new Set(
      (
        await ctx.db
          .query("variables")
          .withIndex("by_node", (q) => q.eq("nodeId", nodeId))
          .collect()
      ).map((row) => row.key),
    );
    const masked = key ? `${KEY_PREFIX}…${key.slice(-4)}` : `${KEY_PREFIX}…`;
    return {
      enabled: desired.tracing ?? false,
      traces: await tracesState(ctx, project),
      env: tracingEnv(node, environment, masked, { local: false }).map(([k, value]) => ({
        key: k,
        value,
        secret: k === HEADERS,
        // The service sets this one itself (or its own endpoint), and its value wins.
        overridden: overridden(own, k),
      })),
    };
  },
});

/**
 * For `keel run`: the tracing variables of a local run of the service, without the endpoint (the
 * CLI adds its own). `env: null` with the reason when the organization has nowhere to send them,
 * so the run still goes ahead with the service's variables.
 */
export const localEnv = action({
  args: { nodeId: v.id("nodes") },
  handler: async (
    ctx,
    { nodeId },
  ): Promise<{ env: Record<string, string> | null; reason: string | null }> => {
    const { node, environment, traces } = await ctx.runQuery(internal.tracing.scope, { nodeId });
    if (traces !== "on") return { env: null, reason: traces === "off" ? NO_SINK : NO_TRACES };
    const key = await ensureKey(ctx, environment._id);
    return {
      env: Object.fromEntries(tracingEnv(node, environment, key, { local: true })),
      reason: null,
    };
  },
});

/**
 * The agent prompt, naming the service (`nodeId`) or the project (`environmentId`) when given
 * one the caller can see. Same text for the dashboard and `keel tracing prompt`.
 */
export const prompt = query({
  args: {
    nodeId: v.optional(v.id("nodes")),
    environmentId: v.optional(v.id("environments")),
  },
  handler: async (ctx, { nodeId, environmentId }) => {
    if (nodeId) {
      const scope = await ownedNode(ctx, nodeId);
      if (scope?.node.type === "service") {
        return agentPrompt({ service: scope.node.name, project: scope.project.slug });
      }
    }
    const scope = environmentId ? await ownedEnvironment(ctx, environmentId) : null;
    return agentPrompt({ project: scope?.project.slug });
  },
});
