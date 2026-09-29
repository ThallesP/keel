import { v } from "convex/values";

import type { Doc } from "./_generated/dataModel";
import { internalMutation } from "./_generated/server";
import type { DeployStep } from "./schema";
import { converged } from "./status";

const MAX_LOG = 500;

function settle(step: DeployStep, node: Doc<"nodes"> | null, now: number): [DeployStep, string?] {
  const name = step.label;
  if (!node) return [{ ...step, status: "failed", finishedAt: now }, `${name}: node deleted`];
  if (!step.appliedAt || !node.observed || !node.desired) return [step];
  if (node.observed.revision === node.desired.revision) {
    const { state, error } = node.observed;
    if (state === "crashloop" || state === "failed") {
      const why = error ? ` · ${error}` : "";
      const what = state === "failed" ? "rolled back by Swarm" : "crash loop";
      return [{ ...step, status: "failed", finishedAt: now }, `${name}: ${what}${why}`];
    }
    if (converged(node)) {
      const text =
        state === "completed"
          ? `${name}: ran to completion`
          : node.desired.replicas === 0
            ? `${name}: stopped`
            : `${name}: ${node.observed.running}/${node.desired.replicas} replicas running`;
      return [{ ...step, status: "done", finishedAt: now }, text];
    }
  }
  return [step];
}

/** After each observe: settle running steps against observed state, close deployments. */
export const run = internalMutation({
  args: {},
  handler: async (ctx) => {
    const running = await ctx.db
      .query("deployments")
      .withIndex("by_status", (q) => q.eq("status", "running"))
      .collect();
    const now = Date.now();
    for (const d of running) {
      const log = [...d.log];
      const steps: DeployStep[] = [];
      for (const step of d.steps) {
        if (!step.nodeId || step.status !== "running") {
          steps.push(step);
          continue;
        }
        const [next, text] = settle(step, await ctx.db.get(step.nodeId), now);
        steps.push(next);
        if (text) log.push({ at: now, nodeId: step.nodeId, text });
      }
      const nodeSteps = steps.filter((s) => s.nodeId);
      const health = steps.find((s) => !s.nodeId);
      const anyFailed = nodeSteps.some((s) => s.status === "failed");
      const allDone = nodeSteps.every((s) => s.status === "done");
      const allApplied = nodeSteps.every((s) => s.status !== "pending" && s.appliedAt);
      if (health) {
        if (anyFailed) Object.assign(health, { status: "failed", finishedAt: now });
        else if (allDone) {
          Object.assign(health, {
            status: "done",
            finishedAt: now,
            startedAt: health.startedAt ?? now,
          });
          log.push({
            at: now,
            text: d.message.startsWith("stop ") ? "stopped" : "all replicas healthy",
          });
        } else if (allApplied && health.status === "pending") {
          Object.assign(health, { status: "running", startedAt: now });
        }
      }
      const status = anyFailed ? "failed" : allDone ? "success" : "running";
      await ctx.db.patch(d._id, {
        steps,
        log: log.slice(-MAX_LOG),
        status,
        finishedAt: status === "running" ? undefined : now,
      });
    }
  },
});

/**
 * Scheduled once per deployment by deployments.start (DEPLOY_TIMEOUT_MS). If the deployment is
 * still running, every unfinished step fails and its node goes red with the last observed error.
 * Nothing else in the system runs on a timer.
 */
export const timeoutDeployment = internalMutation({
  args: { deploymentId: v.id("deployments") },
  handler: async (ctx, { deploymentId }) => {
    const d = await ctx.db.get(deploymentId);
    if (!d || d.status !== "running") return;
    const now = Date.now();
    const log = [...d.log];
    const steps: DeployStep[] = [];
    for (const step of d.steps) {
      if (step.status === "done" || step.status === "failed") {
        steps.push(step);
        continue;
      }
      steps.push({ ...step, status: "failed", finishedAt: now });
      if (!step.nodeId) continue;
      const node = await ctx.db.get(step.nodeId);
      const why = node?.observed?.error ? ` · ${node.observed.error}` : "";
      const text = `${step.label}: timed out waiting for replicas${why}`;
      log.push({ at: now, nodeId: step.nodeId, text });
      if (node)
        await ctx.db.patch(node._id, { applyError: `timed out waiting for replicas${why}` });
    }
    await ctx.db.patch(deploymentId, {
      steps,
      log: log.slice(-MAX_LOG),
      status: "failed",
      finishedAt: now,
    });
  },
});
