"use node";

import Docker from "dockerode";

import type { LogLine, Replica, Tail } from "./types";

// Default provider: `docker service logs` read from the manager socket. Works with no setup and
// no shipping, but only holds what the node's json-file driver kept, and every read is a
// round-trip to every node that runs a task.

const docker = new Docker({ socketPath: "/var/run/docker.sock" });

const TASK_KEY = "com.docker.swarm.task.id=";

/**
 * `2026-09-14T04:05:06.123456789Z com.docker.swarm.node.id=…,com.docker.swarm.task.id=… text`
 * → { time, task, text }. The details block only exists with `details: true`; the stamp only
 * with `timestamps: true`. Lines without a stamp keep time 0.
 */
function parseLine(raw: string, stream: LogLine["stream"]): LogLine {
  let rest = raw;
  let time = 0;
  const space = rest.indexOf(" ");
  const stamp = space > 0 ? rest.slice(0, space) : "";
  if (stamp.endsWith("Z")) {
    const parsed = Date.parse(stamp.replace(/(\.\d{3})\d+Z$/, "$1Z"));
    if (!Number.isNaN(parsed)) {
      time = parsed;
      rest = rest.slice(space + 1);
    }
  }
  let task = "";
  const detailsEnd = rest.indexOf(" ");
  const details = detailsEnd > 0 ? rest.slice(0, detailsEnd) : rest;
  if (details.startsWith("com.docker.swarm.")) {
    const kv = details.split(",").find((d) => d.startsWith(TASK_KEY));
    if (kv) task = kv.slice(TASK_KEY.length);
    rest = detailsEnd > 0 ? rest.slice(detailsEnd + 1) : "";
  }
  return { time, text: rest, stream, task };
}

function splitLines(text: string, stream: LogLine["stream"]) {
  return text
    .split("\n")
    .filter((l) => l.length > 0)
    .map((l) => parseLine(l.replace(/\r$/, ""), stream));
}

/** Non-TTY Docker logs are 8-byte frame headers [type,0,0,0,len(4)] followed by payload. */
export function demux(buf: Buffer): LogLine[] {
  const out: Record<LogLine["stream"], string[]> = { stdout: [], stderr: [] };
  let off = 0;
  while (off + 8 <= buf.length) {
    const type = buf.readUInt8(off);
    const len = buf.readUInt32BE(off + 4);
    if (type > 2 || off + 8 + len > buf.length) break; // not multiplexed (TTY) or truncated
    out[type === 2 ? "stderr" : "stdout"].push(
      buf.subarray(off + 8, off + 8 + len).toString("utf8"),
    );
    off += 8 + len;
  }
  if (off === 0 && buf.length > 0) return splitLines(buf.toString("utf8"), "stdout");
  return [
    ...splitLines(out.stdout.join(""), "stdout"),
    ...splitLines(out.stderr.join(""), "stderr"),
  ].sort((a, b) => a.time - b.time);
}

type TaskLike = { ID: string; Slot?: number; Status?: { State?: string } };

/** Last `n` lines of the node's Swarm service, every replica merged, each line tagged by task. */
export async function dockerTail(nodeId: string, n: number): Promise<Tail> {
  const name = `svc-${nodeId}`;
  const [raw, tasks] = await Promise.all([
    docker
      .getService(name)
      .logs({ stdout: true, stderr: true, tail: n, timestamps: true, details: true })
      .catch((err: { statusCode?: number }) => {
        if (err.statusCode === 404) return null;
        throw err;
      }),
    docker.listTasks({ filters: { service: [name] } }).catch(() => [] as TaskLike[]) as Promise<
      TaskLike[]
    >,
  ]);
  if (!raw) return { source: "docker", lines: [], replicas: [] };
  const replicas: Replica[] = tasks
    .map((t) => ({ task: t.ID, slot: t.Slot ?? 0, state: t.Status?.State ?? "unknown" }))
    .sort((a, b) => a.slot - b.slot || a.task.localeCompare(b.task));
  // dockerode resolves the non-follow body as a Buffer despite its stream typing.
  const lines = demux(Buffer.isBuffer(raw) ? raw : Buffer.from(String(raw)));
  return { source: "docker", lines, replicas };
}
