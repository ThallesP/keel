import { events as dockerEvents } from "./docker";
import { log, sleep } from "./log";
import { getState, touch } from "./state";
import { postEvents } from "./controlPlane";

// Forwards `docker events` to the control plane, which turns each one into a targeted Swarm
// observation (convex/events.ts). Only what the control plane can act on is sent: container
// and service events for our `svc-*` services, and node events. exec_* and health_status
// chatter (postgres health checks fire every few seconds) never leaves the node.
//
// Each accepted batch after a (re)connect carries `X-Keel-Resync: 1` so the control plane
// sweeps once: Docker replays only a small buffer, so a gap may have been missed. The stream
// resumes with `since` so nothing in the buffer is skipped either.

type DockerEvent = {
  Type: string;
  Action: string;
  Actor?: { Attributes?: Record<string, string> };
  timeNano?: number;
};

export type OnContainerEvent = (
  action: string,
  containerId: string,
  attrs: Record<string, string>,
) => void;

function relevant(e: DockerEvent) {
  if (e.Action.startsWith("exec_") || e.Action.startsWith("health_status")) return false;
  const attrs = e.Actor?.Attributes ?? {};
  if (e.Type === "container")
    return attrs["com.docker.swarm.service.name"]?.startsWith("svc-") ?? false;
  if (e.Type === "service") return attrs.name?.startsWith("svc-") ?? false;
  return e.Type === "node";
}

/** Runs forever. `onContainer` sees every svc-* container event, used to (un)tail logs. */
export async function forwardEvents(onContainer: OnContainerEvent) {
  const state = getState();
  for (;;) {
    let resync = true;
    try {
      const res = await dockerEvents(state.eventsSince, ["container", "service", "node"]);
      log(
        "events",
        `streaming docker events${state.eventsSince ? ` since ${state.eventsSince}` : ""}`,
      );
      // (Re)connect: ask for a full sweep before streaming anything.
      if (await postEvents("[]", true)) resync = false;
      const reader = res.body!.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let nl: number;
        while ((nl = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, nl);
          buf = buf.slice(nl + 1);
          if (!line.trim()) continue;
          let e: DockerEvent & { id?: string };
          try {
            e = JSON.parse(line);
          } catch {
            continue;
          }
          if (e.Type === "container" && e.id) {
            onContainer(e.Action, e.id, e.Actor?.Attributes ?? {});
          }
          if (relevant(e)) {
            resync = !(await postEvents(line, resync));
          }
          if (typeof e.timeNano === "number") {
            // --since is inclusive: resume one nanosecond after this event.
            const ns = BigInt(e.timeNano) + 1n;
            state.eventsSince = `${ns / 1_000_000_000n}.${String(ns % 1_000_000_000n).padStart(9, "0")}`;
            touch();
          }
        }
      }
      log("events", "docker events stream ended, reconnecting in 2s");
    } catch (err) {
      log("events", `stream error: ${String(err)}, reconnecting in 2s`);
    }
    await sleep(2000);
  }
}
