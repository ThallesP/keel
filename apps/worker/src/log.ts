/** One-line structured stderr logging; Swarm keeps the worker's own output in `docker service logs`. */
export function log(scope: string, msg: string, extra?: Record<string, unknown>) {
  const tail = extra ? " " + JSON.stringify(extra) : "";
  console.error(`${new Date().toISOString()} [${scope}] ${msg}${tail}`);
}

export const errorText = (err: unknown) =>
  (err instanceof Error ? err.message : String(err)).replace(/\s+/g, " ").trim().slice(0, 300);

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
