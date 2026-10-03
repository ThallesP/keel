// Keel per-node worker. Runs on every Swarm node as the `keel-worker` global service
// (scripts/deploy-worker.sh). Two jobs today, both outbound-only, both read-only on Docker:
//
//   1. events.ts  forwards `docker events` to the control plane so Swarm observation is
//                 event-driven instead of polled (docs/workers.md).
//   2. logs.ts    streams container stdout/stderr to the project's log sink, when one is
//                 configured (docs/logs.md). Config is polled from /worker/config.
//
// Nothing listens. The Docker socket is mounted read-only. Restarts resume from state.json.

import { fetchConfig } from "./controlPlane";
import { info } from "./docker";
import { forwardEvents } from "./events";
import { errorText, log, sleep } from "./log";
import {
  applyConfig,
  flushLogs,
  onConfigRefresh,
  onContainerEvent,
  reconcileFollowers,
  setNodeId,
} from "./logs";
import { loadState, startStateWriter } from "./state";

const CONFIG_POLL_MS = Number(process.env.KEEL_CONFIG_POLL_MS ?? 30_000);

await loadState();
startStateWriter();

const me = await info();
setNodeId(me.Swarm?.NodeID ?? me.Name ?? "");
log("worker", `starting on node ${me.Swarm?.NodeID ?? "?"} (${me.Name ?? "?"})`);

/** Until the next poll, or sooner when logs.ts asks for the config early (onConfigRefresh). */
let wake: (() => void) | null = null;
const untilNextPoll = () =>
  new Promise<void>((resolve) => {
    const done = () => {
      clearTimeout(timer);
      wake = null;
      resolve();
    };
    const timer = setTimeout(done, CONFIG_POLL_MS);
    wake = done;
  });
onConfigRefresh(() => wake?.());

async function pollConfig() {
  for (;;) {
    try {
      const cfg = await fetchConfig();
      applyConfig(cfg.sinks);
      await reconcileFollowers();
    } catch (err) {
      log("config", `poll failed: ${errorText(err)}`);
    }
    await untilNextPoll();
  }
}

const shutdown = async (signal: string) => {
  log("worker", `${signal}, flushing`);
  await Promise.race([flushLogs(), sleep(5000)]);
  process.exit(0);
};
process.on("SIGTERM", () => void shutdown("SIGTERM"));
process.on("SIGINT", () => void shutdown("SIGINT"));

await Promise.all([pollConfig(), forwardEvents(onContainerEvent)]);
