import { errorText, log, sleep } from "./log";

// The control plane is Convex's HTTP router (packages/backend/convex/http.ts). Outbound only,
// one bearer token for every route, retried with capped backoff on 5xx / network errors.

const URL = (process.env.KEEL_URL ?? "").replace(/\/+$/, "");
if (!URL) throw new Error("KEEL_URL is required (Convex site URL, e.g. https://x.convex.site)");

async function readToken() {
  if (process.env.KEEL_WORKER_TOKEN) return process.env.KEEL_WORKER_TOKEN;
  const secret = Bun.file("/run/secrets/keel_worker_token");
  if (await secret.exists()) return (await secret.text()).trim();
  throw new Error("no KEEL_WORKER_TOKEN and no /run/secrets/keel_worker_token");
}

const TOKEN = await readToken();

/** Sinks the worker must feed, each with the service ids it covers. Mirrors worker.config. */
export type WorkerConfig = {
  sinks: {
    projectId: string;
    serviceIds: string[];
    sink: { kind: "axiom"; domain: string; dataset: string; token: string };
    /** When the project connected the sink, epoch ms: where a container's first tail starts. */
    since?: number;
  }[];
};

export async function fetchConfig(): Promise<WorkerConfig> {
  const res = await fetch(`${URL}/worker/config`, {
    headers: { authorization: `Bearer ${TOKEN}` },
    signal: AbortSignal.timeout(10_000),
  });
  if (!res.ok) throw new Error(`config ${res.status}`);
  return res.json() as Promise<WorkerConfig>;
}

/**
 * POST a batch of Docker events. Retries forever on 5xx / network failure (a line is never
 * dropped silently); a 4xx will not succeed on retry so it is logged and skipped. Returns
 * whether the batch was accepted, so the caller can ask for a resync after a failure.
 */
export async function postEvents(body: string, resync: boolean): Promise<boolean> {
  for (let n = 0; ; n = Math.min(n + 1, 6)) {
    try {
      const res = await fetch(`${URL}/worker/events`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${TOKEN}`,
          "content-type": "application/json",
          "x-keel-resync": resync ? "1" : "0",
        },
        body,
        signal: AbortSignal.timeout(10_000),
      });
      if (res.ok) return true;
      if (res.status >= 400 && res.status < 500) {
        log("events", `rejected ${res.status}, skipping`, { body: body.slice(0, 120) });
        return false;
      }
      log("events", `post failed ${res.status}, retry in ${n * 5 + 5}s`);
    } catch (err) {
      log("events", `post failed (${errorText(err)}), retry in ${n * 5 + 5}s`);
    }
    resync = true;
    await sleep(n * 5000 + 5000);
  }
}
