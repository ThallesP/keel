import type { LogLine, Replica, Tail } from "./types";

// Axiom provider. Ingest is done by the per-node worker (apps/worker/src/sinks/axiom.ts); this
// file is the read side plus the connect-time check. Plain fetch, no Node dependency, so it can
// run in the default Convex runtime. Event shape both sides agree on is in docs/logs.md.

export type AxiomConfig = { domain: string; dataset: string; token: string };

/** Axiom dataset names: letters, digits, `-` `_` `.`; must not start with a dot or dash. */
export const DATASET_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;
export const DOMAINS = ["api.axiom.co", "api.eu.axiom.co"] as const;

/** `api.axiom.co` → `https://api.axiom.co`; a full origin (local mock) passes through. */
export function baseUrl(domain: string) {
  return (domain.includes("://") ? domain : `https://${domain}`).replace(/\/+$/, "");
}

async function call(cfg: AxiomConfig, path: string, init: RequestInit) {
  const res = await fetch(`${baseUrl(cfg.domain)}${path}`, {
    ...init,
    headers: {
      authorization: `Bearer ${cfg.token}`,
      "content-type": "application/json",
      ...init.headers,
    },
  });
  if (!res.ok) {
    const body = (await res.text().catch(() => "")).replace(/\s+/g, " ").trim().slice(0, 200);
    throw new Error(`Axiom ${res.status}${body ? `: ${body}` : ""}`);
  }
  return res;
}

type Tabular = {
  tables?: { fields: { name: string }[]; columns: unknown[][] }[];
};

/** POST /v1/datasets/_apl?format=tabular → rows as objects keyed by field name. */
async function query(cfg: AxiomConfig, apl: string, sinceMs: number) {
  const res = await call(cfg, "/v1/datasets/_apl?format=tabular", {
    method: "POST",
    body: JSON.stringify({
      apl,
      startTime: new Date(sinceMs).toISOString(),
      endTime: new Date(Date.now() + 60_000).toISOString(),
    }),
  });
  const data = (await res.json()) as Tabular;
  const table = data.tables?.[0];
  if (!table) return [];
  const rows: Record<string, unknown>[] = [];
  const count = table.columns[0]?.length ?? 0;
  for (let i = 0; i < count; i++) {
    const row: Record<string, unknown> = {};
    table.fields.forEach((f, c) => (row[f.name] = table.columns[c]?.[i]));
    rows.push(row);
  }
  return rows;
}

const ds = (cfg: AxiomConfig) => `['${cfg.dataset}']`;

/**
 * Connect-time check: the token can create/see the dataset and query it. Creating an existing
 * dataset is a 4xx we ignore; the query afterwards is what proves access.
 */
export async function axiomVerify(cfg: AxiomConfig) {
  if (!DATASET_RE.test(cfg.dataset)) throw new Error("Dataset name: letters, digits, - _ . only");
  await call(cfg, "/v2/datasets", {
    method: "POST",
    body: JSON.stringify({ name: cfg.dataset, description: "Keel container logs" }),
  }).catch((err: Error) => {
    // 409 / "already exists" is the normal case on reconnect. Anything else (401, 403) is
    // re-raised by the query below with a clearer scope.
    if (!/exists|409/i.test(err.message) && /40[13]/.test(err.message)) throw err;
  });
  await query(cfg, `${ds(cfg)} | limit 1`, Date.now() - 60_000);
}

const str = (x: unknown) => (typeof x === "string" ? x : x == null ? "" : String(x));
const QUERY_WINDOW_MS = 30 * 24 * 60 * 60_000;

/** Last `n` lines of one service from the dataset, oldest first, plus the replicas seen. */
export async function axiomTail(cfg: AxiomConfig, serviceId: string, n: number): Promise<Tail> {
  // serviceId is a Convex id (alphanumeric) so it is safe to inline.
  const apl = `${ds(cfg)} | where service_id == "${serviceId}" | sort by _time desc | limit ${n} | project _time, message, stream, task, replica`;
  const rows = await query(cfg, apl, Date.now() - QUERY_WINDOW_MS);
  const lines: LogLine[] = rows
    .map((r) => ({
      time: Date.parse(str(r._time)) || 0,
      text: str(r.message),
      stream: r.stream === "stderr" ? ("stderr" as const) : ("stdout" as const),
      task: str(r.task),
    }))
    .reverse();
  const slots = new Map<string, number>();
  for (const r of rows) {
    const task = str(r.task);
    if (task && !slots.has(task)) slots.set(task, Number(r.replica) || 0);
  }
  const replicas: Replica[] = [...slots]
    .map(([task, slot]) => ({ task, slot, state: "" }))
    .sort((a, b) => a.slot - b.slot || a.task.localeCompare(b.task));
  return { source: "axiom", lines, replicas };
}
