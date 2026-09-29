import { type Container, followLogs, listSwarmContainers } from "./docker";
import { FrameParser, LineSplitter, sinceAfter } from "./frames";
import { errorText, log, sleep } from "./log";
import { buildSink, type LogEvent, type Sink, type SinkConfig, sinkKey } from "./sinks";
import { getState, touch } from "./state";

// Streams every `svc-*` container's stdout/stderr on this node to the sink of the project it
// belongs to. `docker logs --follow` per container (Docker has no per-node "all containers"
// stream; the only native fan-out is a logging driver, and changing that would touch every
// service spec and lose `docker service logs`). One follower per container, started on
// `container start` events and on every config poll, stopped when the container dies or
// the project loses its sink.

const FLUSH_MS = 1000;
const FLUSH_LINES = 500;
const MAX_QUEUE = 20_000; // per sink; beyond this the oldest lines are dropped

type Route = { sink: Sink; serviceIds: Set<string> };

/** projectId → route. Rebuilt on every config poll; sinks are reused when their key matches. */
let routes = new Map<string, Route>();
let sinkByService = new Map<string, Sink>();

const followers = new Map<string, AbortController>();

// One pending list per sink, drained in order so a slow sink never fans out into parallel retries.
const pending = new Map<string, { sink: Sink; events: LogEvent[] }>();
let flushTimer: ReturnType<typeof setTimeout> | null = null;
let draining = Promise.resolve();

function enqueue(sink: Sink, e: LogEvent) {
  let q = pending.get(sink.key);
  if (!q) pending.set(sink.key, (q = { sink, events: [] }));
  q.events.push(e);
  if (q.events.length > MAX_QUEUE) q.events.splice(0, q.events.length - MAX_QUEUE);
  if (q.events.length >= FLUSH_LINES) void flush();
  else flushTimer ??= setTimeout(() => void flush(), FLUSH_MS);
}

function flush() {
  if (flushTimer) clearTimeout(flushTimer);
  flushTimer = null;
  const batches: { sink: Sink; events: LogEvent[] }[] = [];
  for (const q of pending.values()) {
    if (q.events.length === 0) continue;
    batches.push({ sink: q.sink, events: q.events });
    q.events = [];
  }
  draining = draining.then(async () => {
    for (const b of batches) await b.sink.send(b.events);
  });
  return draining;
}

let nodeId = "";
export function setNodeId(id: string) {
  nodeId = id;
}

const SERVICE_LABEL = "com.docker.swarm.service.name";

function serviceIdOf(labels: Record<string, string>) {
  const name = labels[SERVICE_LABEL] ?? "";
  return name.startsWith("svc-") ? name.slice(4) : null;
}

async function follow(c: Container, signal: AbortSignal) {
  const labels = c.Labels;
  const serviceId = serviceIdOf(labels)!;
  const service = labels[SERVICE_LABEL]!;
  const task = labels["com.docker.swarm.task.id"] ?? "";
  const replica = Number(labels["com.docker.swarm.task.name"]?.split(".")[1] ?? 0) || 0;
  const container = c.Id.slice(0, 12);
  const state = getState();
  while (!signal.aborted) {
    const sink = sinkByService.get(serviceId);
    if (!sink) return; // project lost its sink between the check and here
    try {
      const res = await followLogs(c.Id, state.logsSince[c.Id]);
      log("logs", `following ${service} (${container})`, { since: state.logsSince[c.Id] ?? "now" });
      const reader = res.body!.getReader();
      const frames = new FrameParser();
      const lines = new LineSplitter();
      signal.addEventListener("abort", () => void reader.cancel().catch(() => {}), { once: true });
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        for (const frame of frames.push(value)) {
          for (const line of lines.push(frame)) {
            const current = sinkByService.get(serviceId);
            if (!current) return;
            enqueue(current, {
              _time: line.time || new Date().toISOString(),
              message: line.text,
              stream: line.stream,
              service_id: serviceId,
              service,
              task,
              replica,
              node: nodeId,
              container,
            });
            if (line.time) {
              const since = sinceAfter(line.time);
              if (since) {
                state.logsSince[c.Id] = since;
                touch();
              }
            }
          }
        }
      }
      // EOF: the container stopped. The `die` event removes the follower; nothing to redo.
      return;
    } catch (err) {
      if (signal.aborted) return;
      log("logs", `follow ${container} failed (${errorText(err)}), retry in 3s`);
      await sleep(3000);
    }
  }
}

function start(c: Container) {
  if (followers.has(c.Id)) return;
  const serviceId = serviceIdOf(c.Labels);
  if (!serviceId || !sinkByService.has(serviceId)) return;
  const ctl = new AbortController();
  followers.set(c.Id, ctl);
  void follow(c, ctl.signal).finally(() => {
    if (followers.get(c.Id) === ctl) followers.delete(c.Id);
  });
}

export function stop(containerId: string) {
  followers.get(containerId)?.abort();
  followers.delete(containerId);
  const state = getState();
  if (containerId in state.logsSince) {
    delete state.logsSince[containerId];
    touch();
  }
}

/** Re-scan running containers: start followers that are missing, stop ones no longer routed. */
export async function reconcileFollowers() {
  const containers = await listSwarmContainers();
  const live = new Set(containers.map((c) => c.Id));
  for (const c of containers) start(c);
  for (const [id] of followers) {
    if (!live.has(id)) stop(id);
  }
  // A follower whose project dropped its sink returns on its own at the next line; abort now
  // so an idle container does not keep a socket open.
  for (const c of containers) {
    const serviceId = serviceIdOf(c.Labels);
    if (serviceId && !sinkByService.has(serviceId)) stop(c.Id);
  }
  // Forget resume points for containers that are gone.
  const state = getState();
  for (const id of Object.keys(state.logsSince)) if (!live.has(id)) delete state.logsSince[id];
}

/** Called on every config poll. Reuses sinks whose config did not change. */
export function applyConfig(
  sinks: { projectId: string; serviceIds: string[]; sink: SinkConfig }[],
) {
  const next = new Map<string, Route>();
  const nextByService = new Map<string, Sink>();
  const existing = new Map([...routes.values()].map((r) => [r.sink.key, r.sink]));
  for (const s of sinks) {
    const key = sinkKey(s.sink);
    const sink = existing.get(key) ?? buildSink(s.sink);
    next.set(s.projectId, { sink, serviceIds: new Set(s.serviceIds) });
    for (const id of s.serviceIds) nextByService.set(id, sink);
  }
  const changed =
    next.size !== routes.size ||
    [...next].some(([p, r]) => routes.get(p)?.sink.key !== r.sink.key) ||
    nextByService.size !== sinkByService.size ||
    [...nextByService].some(([id, s]) => sinkByService.get(id)?.key !== s.key);
  routes = next;
  sinkByService = nextByService;
  if (changed) log("logs", `config applied`, { sinks: next.size, services: nextByService.size });
  return changed;
}

/** Hook for the event forwarder: new svc-* container → follow it; dead one → stop. */
export function onContainerEvent(action: string, id: string, attrs: Record<string, string>) {
  if (action === "start") {
    // Labels on the event are the same as the container's; enough to route without an inspect.
    start({ Id: id, Names: [], Labels: attrs, State: "running" });
  } else if (action === "die" || action === "destroy") {
    stop(id);
  }
}

export const flushLogs = flush;
