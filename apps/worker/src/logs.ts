import { type Container, followLogs, listSwarmContainers } from "./docker";
import { FrameParser, LineSplitter, sinceAfter } from "./frames";
import { errorText, log, sleep } from "./log";
import { buildSink, type LogEvent, type Sink, type SinkConfig, sinkKey } from "./sinks";
import { getState, touch } from "./state";

// Streams every `svc-*` container's stdout/stderr on this node to the sink of the project it
// belongs to. `docker logs --follow` per container: Docker has no per-node "all containers"
// stream, and a logging driver would put the sink in every service spec and restart every task
// whenever it changes. One follower per container, started on `container start` events and on
// every config poll, stopped when the container is removed or the project loses its sink.
//
// Delivery: a line's resume point (`logsSince`) is saved only once the sink accepted the batch
// it was in, so a restart re-reads anything not delivered yet from Docker's own log file instead
// of skipping it. A sink that stays down backs up its queue; at MAX_QUEUE the followers feeding
// it stop reading until it drains, and Docker's file is the buffer. Nothing is dropped on this
// side; only a sink that rejects a batch as malformed (4xx) discards it.

const FLUSH_MS = 1000;
const FLUSH_LINES = 500;
const RETRY_MS = 5000; // between attempts at a batch the sink could not take
const MAX_QUEUE = 20_000; // per sink; beyond this its followers wait

type Route = { sink: Sink; serviceIds: Set<string> };
type Entry = { event: LogEvent; container: string; since?: string };
type Queue = {
  sink: Sink;
  entries: Entry[];
  draining: Promise<void> | null;
  /** Followers waiting for the queue to have room again. */
  room: (() => void)[];
};

/** projectId → route. Rebuilt on every config poll; sinks are reused when their key matches. */
let routes = new Map<string, Route>();
let sinkByService = new Map<string, Sink>();
/** serviceId → Docker `since` of the moment its project's sink was connected (config `since`). */
let sinceByService = new Map<string, string>();
/**
 * Per container, the line last read in this process. A tail that is re-opened (the socket idles
 * out, Docker hiccups) resumes here: what was read is queued or delivered already, and reading
 * it again would send it twice. Across process restarts the persisted, delivery-confirmed
 * `logsSince` is the resume point instead.
 */
const readSince = new Map<string, string>();

/** Epoch milliseconds → Docker `since` ("seconds.nanoseconds"). */
const dockerSince = (ms: number) =>
  `${Math.floor(ms / 1000)}.${String(Math.min(Math.round((ms % 1000) * 1_000_000), 999_999_999)).padStart(9, "0")}`;

/** Set by the main loop: fetch the config now rather than at the next poll. */
let refreshConfig: () => void = () => {};
let lastRefresh = 0;
export function onConfigRefresh(fn: () => void) {
  refreshConfig = fn;
}

const followers = new Map<string, AbortController>();
/** Containers read to EOF after they exited; nothing left to fetch until they are removed. */
const finished = new Set<string>();

/** One queue per sink, drained in order so a slow sink never fans out into parallel retries. */
const queues = new Map<string, Queue>();
let flushTimer: ReturnType<typeof setTimeout> | null = null;
let retryTimer: ReturnType<typeof setTimeout> | null = null;

function queueFor(sink: Sink) {
  let q = queues.get(sink.key);
  if (!q) queues.set(sink.key, (q = { sink, entries: [], draining: null, room: [] }));
  return q;
}

/** Resolves when the queue has room, or at once when the follower is aborted meanwhile. */
function waitForRoom(q: Queue, signal: AbortSignal) {
  if (q.entries.length < MAX_QUEUE || signal.aborted) return Promise.resolve();
  return new Promise<void>((resolve) => {
    q.room.push(resolve);
    signal.addEventListener("abort", () => resolve(), { once: true });
  });
}

function enqueue(q: Queue, entry: Entry) {
  q.entries.push(entry);
  if (q.entries.length >= FLUSH_LINES) flush();
  else flushTimer ??= setTimeout(flush, FLUSH_MS);
}

function flush() {
  if (flushTimer) clearTimeout(flushTimer);
  flushTimer = null;
  for (const q of queues.values()) {
    if (q.entries.length === 0 || q.draining) continue;
    q.draining = drain(q).finally(() => {
      q.draining = null;
    });
  }
}

/** The sink has these lines: per container, where a restart resumes from. Later entries win. */
function checkpoint(batch: Entry[]) {
  const state = getState();
  let changed = false;
  for (const e of batch) {
    if (!e.since) continue;
    state.logsSince[e.container] = e.since;
    changed = true;
  }
  if (changed) touch();
}

/**
 * Sends the queue in order, FLUSH_LINES per request. A batch the sink could not take goes back
 * to the front and is retried after RETRY_MS; its resume points stay where they were.
 */
async function drain(q: Queue) {
  while (q.entries.length > 0 && queues.get(q.sink.key) === q) {
    const batch = q.entries.splice(0, FLUSH_LINES);
    if (!(await q.sink.send(batch.map((e) => e.event)))) {
      q.entries.unshift(...batch);
      retryTimer ??= setTimeout(() => {
        retryTimer = null;
        flush();
      }, RETRY_MS);
      return;
    }
    checkpoint(batch);
    if (q.entries.length < MAX_QUEUE / 2) for (const resume of q.room.splice(0)) resume();
  }
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
      // In this process → delivered before a restart → the sink's connect time → new lines only.
      const since = readSince.get(c.Id) ?? state.logsSince[c.Id] ?? sinceByService.get(serviceId);
      const res = await followLogs(c.Id, since);
      log("logs", `following ${service} (${container})`, { since: since ?? "now" });
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
            const q = queueFor(current);
            await waitForRoom(q, signal);
            if (signal.aborted) return;
            const after = line.time ? sinceAfter(line.time) : undefined;
            if (after) readSince.set(c.Id, after);
            enqueue(q, {
              event: {
                _time: line.time || new Date().toISOString(),
                message: line.text,
                stream: line.stream,
                service_id: serviceId,
                service,
                task,
                replica,
                node: nodeId,
                container,
              },
              container: c.Id,
              since: after,
            });
          }
        }
      }
      // EOF: the container exited. Its lines are queued; the resume point catches up as they
      // are delivered and goes when the container is removed.
      finished.add(c.Id);
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

/**
 * Stops following. The resume point stays (lines may still be queued, and an exited container
 * can be read again) unless `forget`: the container is gone and so is its log.
 */
export function stop(containerId: string, forget = false) {
  followers.get(containerId)?.abort();
  followers.delete(containerId);
  if (!forget) return;
  finished.delete(containerId);
  readSince.delete(containerId);
  const state = getState();
  if (containerId in state.logsSince) {
    delete state.logsSince[containerId];
    touch();
  }
}

/**
 * Re-scan containers: follow running ones that are routed to a sink, finish reading exited ones
 * a restart left undelivered (they have a resume point and were not read to EOF yet), stop the
 * rest, and forget resume points of containers Docker no longer has.
 */
export async function reconcileFollowers() {
  const containers = await listSwarmContainers();
  const known = new Set(containers.map((c) => c.Id));
  const state = getState();
  for (const c of containers) {
    const serviceId = serviceIdOf(c.Labels);
    const routed = serviceId !== null && sinkByService.has(serviceId);
    const pending = c.Id in state.logsSince && !finished.has(c.Id);
    if (routed && (c.State === "running" || pending)) start(c);
    // Unrouted (project dropped its sink): a follower returns on its own at the next line;
    // abort now so an idle container does not keep a socket open.
    else stop(c.Id);
  }
  for (const [id] of followers) if (!known.has(id)) stop(id, true);
  for (const id of Object.keys(state.logsSince)) if (!known.has(id)) stop(id, true);
  for (const id of finished) if (!known.has(id)) finished.delete(id);
}

/** Called on every config poll. Reuses sinks whose config did not change. */
export function applyConfig(
  sinks: { projectId: string; serviceIds: string[]; sink: SinkConfig; since?: number }[],
) {
  const next = new Map<string, Route>();
  const nextByService = new Map<string, Sink>();
  const nextSince = new Map<string, string>();
  const existing = new Map([...routes.values()].map((r) => [r.sink.key, r.sink]));
  for (const s of sinks) {
    const key = sinkKey(s.sink);
    const sink = existing.get(key) ?? buildSink(s.sink);
    next.set(s.projectId, { sink, serviceIds: new Set(s.serviceIds) });
    for (const id of s.serviceIds) {
      nextByService.set(id, sink);
      if (typeof s.since === "number") nextSince.set(id, dockerSince(s.since));
    }
  }
  const changed =
    next.size !== routes.size ||
    [...next].some(([p, r]) => routes.get(p)?.sink.key !== r.sink.key) ||
    nextByService.size !== sinkByService.size ||
    [...nextByService].some(([id, s]) => sinkByService.get(id)?.key !== s.key);
  routes = next;
  sinkByService = nextByService;
  sinceByService = nextSince;
  // A sink nobody routes to any more (disconnected, or its token changed) cannot take what is
  // still queued for it; the lines go with it, and followers waiting on it move on.
  const used = new Set([...next.values()].map((r) => r.sink.key));
  for (const [key, q] of queues) {
    if (used.has(key)) continue;
    queues.delete(key);
    if (q.entries.length > 0)
      log("logs", `dropping ${q.entries.length} queued lines for a removed sink`);
    for (const resume of q.room.splice(0)) resume();
  }
  if (changed) log("logs", `config applied`, { sinks: next.size, services: nextByService.size });
  return changed;
}

/**
 * Hook for the event forwarder: new svc-* container → follow it; removed one → forget it. A
 * container that dies ends its own stream (EOF), and what it wrote stays queued until delivered.
 */
export function onContainerEvent(action: string, id: string, attrs: Record<string, string>) {
  if (action === "start") {
    const serviceId = serviceIdOf(attrs);
    if (!serviceId) return;
    // Labels on the event are the same as the container's; enough to route without an inspect.
    if (sinkByService.has(serviceId)) start({ Id: id, Names: [], Labels: attrs, State: "running" });
    // Unrouted: its project may have connected a sink since the last poll. Ask for the config
    // now so the container's first lines are not up to a poll interval behind; the tail then
    // starts from the connect time, before the container did, so nothing is missed meanwhile.
    else if (Date.now() - lastRefresh > 5000) {
      lastRefresh = Date.now();
      refreshConfig();
    }
  } else if (action === "destroy") {
    stop(id, true);
  }
}

/** Sends whatever is queued and waits for the sinks to answer (shutdown). */
export async function flushLogs() {
  flush();
  await Promise.all([...queues.values()].map((q) => q.draining));
}
