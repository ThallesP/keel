// Docker Engine API over the unix socket. Bun's fetch dials sockets natively, so no client
// library. The socket is mounted read-only and every call here is a GET: the worker observes,
// it never mutates the daemon. Mutations stay on the manager (packages/backend/convex/swarm.ts).

const SOCKET = process.env.DOCKER_SOCKET ?? "/var/run/docker.sock";
const BASE = "http://docker";

export type Container = {
  Id: string;
  Names: string[];
  Labels: Record<string, string>;
  State: string;
};

async function get(path: string, params?: Record<string, string>) {
  const qs = params ? "?" + new URLSearchParams(params).toString() : "";
  const res = await fetch(`${BASE}${path}${qs}`, { unix: SOCKET });
  if (!res.ok) throw new Error(`docker ${path}: ${res.status} ${await res.text()}`);
  return res;
}

export async function info(): Promise<{ Swarm?: { NodeID?: string }; Name?: string }> {
  return (await get("/info")).json() as Promise<{ Swarm?: { NodeID?: string }; Name?: string }>;
}

/** Swarm task containers, running or exited (Swarm keeps the last few of each service around). */
export async function listSwarmContainers(): Promise<Container[]> {
  const res = await get("/containers/json", {
    filters: JSON.stringify({
      label: ["com.docker.swarm.service.name"],
      status: ["running", "exited"],
    }),
  });
  return res.json() as Promise<Container[]>;
}

/** Live NDJSON event stream, one object per line. Never returns on its own; the caller reads until EOF. */
export function events(since: string | undefined, types: string[]) {
  const params: Record<string, string> = { filters: JSON.stringify({ type: types }) };
  if (since) params.since = since;
  return get("/events", params);
}

/**
 * Follow a container's stdout+stderr with RFC3339Nano timestamps. Non-TTY containers answer
 * with the multiplexed frame format (see frames.ts). `since` is "seconds.nanoseconds".
 */
export function followLogs(id: string, since: string | undefined) {
  const params: Record<string, string> = {
    follow: "1",
    stdout: "1",
    stderr: "1",
    timestamps: "1",
  };
  if (since) params.since = since;
  else params.tail = "0";
  return get(`/containers/${id}/logs`, params);
}
