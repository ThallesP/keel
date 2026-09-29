"use node";

import { v } from "convex/values";
import Docker from "dockerode";

import { internal } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import { type ActionCtx, internalAction } from "./_generated/server";
import type { Desired, Observed } from "./schema";

// Root-equivalent access to the host. Everything in this file stays internal.
const docker = new Docker({ socketPath: "/var/run/docker.sock" });

const NETWORK = "keel";
const SERVICE_LABEL = "keel.service";
const REVISION_LABEL = "keel.revision";
// Swarm reports a task as `starting` for ~100ms after the daemon emits `container start`.
// When a scan lands in that window we look once or twice more, then stop; the next
// Docker event (or the per-deployment timeout) takes it from there. This is not polling:
// it only runs while Swarm itself says a task is mid-transition.
const SETTLE_MS = 2_000;
const SETTLE_MAX = 2;

const serviceName = (id: string) => `svc-${id}`;

// No EndpointSpec: nothing is published on the host (networking.md, zero-inbound-port).
// Service-to-service traffic uses the overlay DNS name `svc-<id>`.
// `oneShot` (learned by observe): the image exits 0. Swarm counts exit 0 inside the 5s Monitor
// window as a failed update, so `rollback` would undo every Redeploy of it; `continue` lets the
// new revision run once. Mode stays Replicated: on-failure never restarts an exit-0 task, and a
// spec update (the revision label) is what runs it again.
function toSpec(id: string, d: Desired, env: string[], oneShot: boolean): Docker.ServiceSpec {
  const Labels = { [SERVICE_LABEL]: id, [REVISION_LABEL]: String(d.revision) };
  return {
    Name: serviceName(id),
    Labels,
    TaskTemplate: {
      ContainerSpec: { Image: d.image, Env: env, Labels },
      // Delay is nanoseconds. After MaxAttempts Swarm gives up and observe reports crashloop.
      RestartPolicy: { Condition: "on-failure", Delay: 5_000_000_000, MaxAttempts: 5 },
      Networks: [{ Target: NETWORK }],
    },
    Mode: { Replicated: { Replicas: d.replicas } },
    // start-first is only safe while services are stateless. See volumes.md before adding mounts.
    UpdateConfig: {
      Parallelism: 1,
      Order: "start-first",
      FailureAction: oneShot ? "continue" : "rollback",
    },
  };
}

function notFoundAsNull(err: { statusCode?: number }) {
  if (err.statusCode === 404) return null;
  throw err;
}

const errorText = (err: unknown) =>
  (err instanceof Error ? err.message : String(err)).replace(/\s+/g, " ").trim().slice(0, 300);

function pull(image: string) {
  return new Promise<void>((resolve, reject) => {
    docker.pull(image, (err: Error | null, stream: NodeJS.ReadableStream) => {
      if (err) return reject(err);
      docker.modem.followProgress(stream, (e: Error | null) => (e ? reject(e) : resolve()));
    });
  });
}

// Idempotent: create if missing, update if present. Reads `desired` + env at run time so
// two quick user actions converge on the latest one regardless of scheduling order.
// `pull: true` (explicit Redeploy) refreshes the image from the registry; otherwise a locally
// cached image is used as-is, so a ship never waits on Docker Hub for an image we already have.
export const apply = internalAction({
  args: {
    id: v.id("nodes"),
    deploymentId: v.optional(v.id("deployments")),
    pull: v.optional(v.boolean()),
  },
  handler: async (ctx, { id, deploymentId, pull: refresh = false }) => {
    const input = await ctx.runQuery(internal.nodesInternal.applyInput, { id });
    if (!input) return; // deleted before we ran; `remove` handles the Swarm side
    const { desired, env, oneShot } = input;
    const step = (fn: "stepRunning" | "stepLog" | "stepApplied" | "stepFailed", text: string) =>
      deploymentId
        ? ctx.runMutation(internal.deployments[fn], { deploymentId, nodeId: id, text })
        : Promise.resolve();
    const stillWanted = async () =>
      (await ctx.runQuery(internal.nodesInternal.applyInput, { id })) !== null;

    try {
      const cached = await docker.getImage(desired.image).inspect().catch(notFoundAsNull);
      if (cached && !refresh) {
        await step("stepRunning", `using cached ${desired.image}`);
      } else {
        await step("stepRunning", `pulling ${desired.image}`);
        const t0 = Date.now();
        try {
          await pull(desired.image);
          await step(
            "stepLog",
            `pulled ${desired.image} in ${((Date.now() - t0) / 1000).toFixed(1)}s`,
          );
        } catch (err) {
          // Registry unreachable but image cached locally is fine; a missing image is not.
          if (!cached) throw err;
          await step("stepLog", `pull failed (${errorText(err)}), using cached image`);
        }
      }

      // A pull can take minutes and the user may have deleted the node meanwhile. nodes.remove
      // deletes the row before scheduling swarm.remove, so this read is authoritative.
      if (!(await stillWanted())) return;

      const spec = toSpec(id, desired, env, oneShot);
      const service = docker.getService(spec.Name!);
      // Two user actions in quick succession schedule two applies. The loser gets
      // "update out of sequence" from Swarm; re-read the version and try again.
      for (let attempt = 0; ; attempt++) {
        const existing = await service.inspect().catch(notFoundAsNull);
        try {
          if (!existing) {
            await docker.createService(spec);
            // Deleted between the check above and the create: swarm.remove already ran against
            // nothing, so the service we just made would be an orphan. Take it back.
            if (!(await stillWanted())) {
              await service.remove().catch(notFoundAsNull);
              return;
            }
            await step("stepApplied", `service created · ${desired.replicas} replica(s)`);
          } else {
            await service.update({ _query: { version: existing.Version.Index }, _body: spec });
            await step("stepApplied", `service updated · revision ${desired.revision}`);
          }
          break;
        } catch (err) {
          if (attempt >= 2) throw err;
        }
      }
      await ctx.runMutation(internal.nodesInternal.setApplyError, { id, error: undefined });
    } catch (err) {
      const text = errorText(err);
      await ctx.runMutation(internal.nodesInternal.setApplyError, { id, error: text });
      await step("stepFailed", `error: ${text}`);
      return;
    }
    // Docker events drive observation from here. This one guarantees a scan lands after
    // stepApplied even if the event burst for the create already went by; it coalesces
    // with any scan those events scheduled.
    await ctx.runMutation(internal.nodesInternal.scheduleObserve, { id });
  },
});

export const remove = internalAction({
  args: { id: v.id("nodes") },
  handler: async (_ctx, { id }) => {
    await docker.getService(serviceName(id)).remove().catch(notFoundAsNull);
  },
});

type Task = {
  NodeID?: string;
  DesiredState: string;
  Status: { State: string; Err?: string; Timestamp?: string };
  Spec: { ContainerSpec?: { Labels?: Record<string, string> } };
};

type Service = {
  Spec?: { Name?: string; Labels?: Record<string, string> };
  // Swarm's verdict on the last `service update`. Absent on a fresh create. It watches the new
  // task for UpdateConfig.Monitor (5s) and pauses or rolls back if the task dies in that window.
  UpdateStatus?: { State?: string; Message?: string };
};

const label = (t: Task, key: string) => t.Spec.ContainerSpec?.Labels?.[key];
const isFailed = (t: Task) => t.Status.State === "failed" || t.Status.State === "rejected";
// Between "scheduled" and "running"; a container event follows within seconds. `pending`
// (nothing can schedule it) is deliberately not here: that is the deployment timeout's job.
const TRANSIENT = new Set([
  "new",
  "allocated",
  "assigned",
  "accepted",
  "preparing",
  "ready",
  "starting",
]);

function summarize(tasks: Task[], service: Service | null): Observed {
  const update = service?.UpdateStatus?.State;
  const rolledBack = update === "paused" || (update?.startsWith("rollback") ?? false);
  // Old revisions linger in task history; only the newest one says anything about health.
  // After a rollback the spec reverts, but the failed revision's tasks are still the newest,
  // so `revision` names what failed and `state: failed` says the service is not running it.
  const revision = Math.max(
    Number(service?.Spec?.Labels?.[REVISION_LABEL] ?? 0),
    ...tasks.map((t) => Number(label(t, REVISION_LABEL) ?? 0)),
  );
  const current = tasks.filter((t) => Number(label(t, REVISION_LABEL)) === revision);
  const live = current.filter((t) => t.DesiredState === "running");
  const running = live.filter((t) => t.Status.State === "running");
  const failed = current.filter(isFailed);
  // Exit 0 under RestartPolicy on-failure: Swarm marks the task `complete`, desired `shutdown`,
  // and never touches it again. One-shot images (migrations, hello-world) end every run here.
  const completed = current.filter((t) => t.Status.State === "complete");
  const oneShot = completed.length > 0 && live.length === 0 && failed.length === 0;
  const finishedAt = Math.max(...completed.map((t) => Date.parse(t.Status.Timestamp ?? "") || 0));
  return {
    revision,
    running: running.length,
    ...(oneShot && { completed: completed.length, finishedAt }),
    nodeIds: [...new Set(running.flatMap((t) => (t.NodeID ? [t.NodeID] : [])))],
    state: rolledBack
      ? "failed"
      : failed.length >= 5
        ? "crashloop"
        : live.some((t) => t.Status.State === "pending")
          ? "pending"
          : // `updating` until Swarm reports `completed`: a task that dies inside the Monitor window
            // must not count as converged, Swarm is about to roll it back.
            update === "updating" || running.length < live.length
            ? "updating"
            : oneShot
              ? "completed"
              : "ok",
    error:
      failed[failed.length - 1]?.Status.Err ??
      (rolledBack ? service?.UpdateStatus?.Message : undefined),
    at: Date.now(),
  };
}

const settling = (tasks: Task[], revision: number) =>
  tasks.some(
    (t) =>
      Number(label(t, REVISION_LABEL)) === revision &&
      t.DesiredState !== "shutdown" &&
      TRANSIENT.has(t.Status.State),
  );

async function observeServers(ctx: ActionCtx) {
  const servers: { Status?: { State?: string } }[] = await docker.listNodes();
  const ready = servers.filter((n) => n.Status?.State === "ready").length;
  console.log(`observeServers ready=${ready}/${servers.length}`);
  await ctx.runMutation(internal.environments.setServers, { servers: ready });
}

// One node, one Docker call. Scheduled (debounced) by events.ingest for every Docker event
// that names this node's service, and once by apply after it finishes.
export const observeNode = internalAction({
  args: { id: v.id("nodes"), settle: v.optional(v.number()) },
  handler: async (ctx, { id, settle = 0 }) => {
    // Clear first: an event arriving from here on schedules a fresh scan instead of
    // being coalesced into this one, whose Docker read may predate the event's effect.
    await ctx.runMutation(internal.nodesInternal.clearObserveScheduled, { id });
    const [service, tasks]: [Service | null, Task[]] = await Promise.all([
      docker.getService(serviceName(id)).inspect().catch(notFoundAsNull),
      docker.listTasks({ filters: { label: [`${SERVICE_LABEL}=${id}`] } }),
    ]);
    const observed = summarize(tasks, service);
    console.log(
      `observeNode ${id} tasks=${tasks.length} update=${service?.UpdateStatus?.State ?? "-"} revision=${observed.revision} state=${observed.state} running=${observed.running} settle=${settle}`,
    );
    await ctx.runMutation(internal.nodesInternal.setObserved, { id, observed });
    await ctx.runMutation(internal.reconcile.run, {});
    if (settle < SETTLE_MAX && settling(tasks, observed.revision)) {
      await ctx.runMutation(internal.nodesInternal.scheduleObserve, {
        id,
        delayMs: SETTLE_MS,
        settle: settle + 1,
      });
    }
  },
});

// Ready-server count for the status bar. Scheduled by events.ingest on `node` events.
export const observeSwarmNodes = internalAction({
  args: {},
  handler: (ctx) => observeServers(ctx),
});

// Full sweep from the manager: every task on every node. Nothing runs this on a timer. It is
// scheduled when the event forwarder (re)starts, since events emitted while it was down are
// only replayed from Docker's small in-memory buffer, and it is there for manual use
// (`bunx convex run swarm:observe`).
export const observe = internalAction({
  args: {},
  handler: async (ctx) => {
    const nodes = await ctx.runQuery(internal.nodesInternal.listDeployable, {});
    if (nodes.length > 0) {
      const [services, tasks]: [Service[], Task[]] = await Promise.all([
        docker.listServices({ filters: { label: [SERVICE_LABEL] } }),
        docker.listTasks({ filters: { label: [SERVICE_LABEL] } }),
      ]);
      const byName = new Map(services.map((s) => [s.Spec?.Name, s]));
      for (const node of nodes) {
        const id: Id<"nodes"> = node.id;
        const own = tasks.filter((t) => label(t, SERVICE_LABEL) === id);
        const observed = summarize(own, byName.get(serviceName(id)) ?? null);
        await ctx.runMutation(internal.nodesInternal.setObserved, { id, observed });
      }
      await ctx.runMutation(internal.reconcile.run, {});
    }
    console.log(`observe (full sweep) nodes=${nodes.length}`);
    await observeServers(ctx);
  },
});
