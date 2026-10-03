import type { LogLine, ProjectLine, ProjectTail, Replica, Tail } from "./types";

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
  await axiomCanQuery(cfg);
}

/** The token can query the dataset. */
export async function axiomCanQuery(cfg: AxiomConfig) {
  await query(cfg, `${ds(cfg)} | limit 1`, Date.now() - 60_000);
}

/**
 * A tail query over the last 30 days. A dataset nothing was shipped to yet has no fields, and APL
 * rejects `where service_id …` with 400 "invalid field": that is "no lines yet", not an error.
 */
async function tailQuery(cfg: AxiomConfig, apl: string) {
  try {
    return await query(cfg, apl, Date.now() - QUERY_WINDOW_MS);
  } catch (err) {
    if (err instanceof Error && /Axiom 400.*invalid field/.test(err.message)) return [];
    throw err;
  }
}

const str = (x: unknown) => (typeof x === "string" ? x : x == null ? "" : String(x));
const QUERY_WINDOW_MS = 30 * 24 * 60 * 60_000;

/** Last `n` lines of one service from the dataset, oldest first, plus the replicas seen. */
export async function axiomTail(cfg: AxiomConfig, serviceId: string, n: number): Promise<Tail> {
  // serviceId is a Convex id (alphanumeric) so it is safe to inline.
  const apl = `${ds(cfg)} | where service_id == "${serviceId}" | sort by _time desc | limit ${n} | project _time, message, stream, task, replica`;
  const rows = await tailQuery(cfg, apl);
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

/** Last `n` lines across the given services (newest window), oldest first. For the Logs page. */
export async function axiomRecent(
  cfg: AxiomConfig,
  serviceIds: string[],
  n: number,
  search: string,
): Promise<ProjectTail> {
  if (serviceIds.length === 0) return { source: "axiom", lines: [] };
  // Convex ids are alphanumeric, safe to inline. The search term is an APL string literal.
  const ids = serviceIds.map((id) => `"${id}"`).join(", ");
  const term = search.trim();
  const where = term ? ` | where message contains "${term.replace(/[\\"]/g, "\\$&")}"` : "";
  const apl = `${ds(cfg)} | where service_id in (${ids})${where} | sort by _time desc | limit ${n} | project _time, message, stream, task, service_id`;
  const rows = await tailQuery(cfg, apl);
  const lines: ProjectLine[] = rows
    .map((r) => ({
      time: Date.parse(str(r._time)) || 0,
      text: str(r.message),
      stream: r.stream === "stderr" ? ("stderr" as const) : ("stdout" as const),
      task: str(r.task),
      serviceId: str(r.service_id),
    }))
    .reverse();
  return { source: "axiom", lines };
}

// ── Sign in with Axiom ──────────────────────────────────────────────────────────────────────
//
// Axiom's OAuth server for third parties is the one behind its MCP server
// (mcp.axiom.co/.well-known/oauth-authorization-server → authorization.axiom.co). It has no
// app console but supports Dynamic Client Registration (RFC 7591), so every Keel install
// registers its own public client for its own `<origin>/axiom/callback` (https, or
// http://localhost; plain-http IPs are refused) and runs authorization code + PKCE with it.
// The control plane exchanges the code, uses that user token once to create the dataset and
// mint an API token scoped to ingest + query on it, and drops it. Only the scoped token is
// stored. (The older login.axiom.co server only knows the Axiom CLI's client, which is
// limited to loopback redirects.)

/**
 * Where the flow talks to. The overrides exist for a local mock and only apply with
 * KEEL_ALLOW_LOCAL_SINKS=1, like the full-origin sink domain in logSinks.connectAxiom.
 */
function axiomOAuth() {
  const local = process.env.KEEL_ALLOW_LOCAL_SINKS === "1";
  return {
    auth: ((local && process.env.KEEL_AXIOM_AUTH_URL) || "https://authorization.axiom.co").replace(
      /\/+$/,
      "",
    ),
    api: (local && process.env.KEEL_AXIOM_API_URL) || null,
  };
}

const base64url = (bytes: Uint8Array) =>
  btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");

type OAuthError = { error?: string; error_description?: string };
const oauthMessage = (data: OAuthError, status: number) =>
  data.error_description || data.error || `HTTP ${status}`;

/** Registers a public client for `redirectUri` (DCR). Returns its client id. */
export async function axiomRegisterClient(redirectUri: string) {
  const res = await fetch(`${axiomOAuth().auth}/oauth2/register`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      client_name: "Keel",
      redirect_uris: [redirectUri],
      grant_types: ["authorization_code"],
      response_types: ["code"],
      token_endpoint_auth_method: "none",
    }),
  });
  const data = (await res.json().catch(() => ({}))) as OAuthError & { client_id?: string };
  if (!res.ok || !data.client_id) {
    throw new Error(`Axiom refused to register Keel: ${oauthMessage(data, res.status)}`);
  }
  return data.client_id;
}

/** Fresh PKCE verifier + state, and the authorize URL carrying the S256 challenge. */
export async function axiomAuthorizeUrl(clientId: string, redirectUri: string) {
  const verifier = base64url(crypto.getRandomValues(new Uint8Array(32)));
  const state = base64url(crypto.getRandomValues(new Uint8Array(16)));
  const challenge = base64url(
    new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier))),
  );
  const url = new URL(`${axiomOAuth().auth}/oauth2/authorize`);
  url.search = new URLSearchParams({
    client_id: clientId,
    response_type: "code",
    redirect_uri: redirectUri,
    scope: "openid profile email",
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  }).toString();
  return { state, verifier, url: url.toString() };
}

async function personal(
  domain: string,
  token: string,
  orgId: string | null,
  path: string,
  init: RequestInit = {},
) {
  const res = await fetch(`${baseUrl(domain)}${path}`, {
    ...init,
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
      ...(orgId ? { "x-axiom-org-id": orgId } : {}),
      ...init.headers,
    },
  });
  if (!res.ok) {
    const body = (await res.text().catch(() => "")).replace(/\s+/g, " ").trim().slice(0, 200);
    throw new Error(`Axiom ${res.status}${body ? `: ${body}` : ""}`);
  }
  return res;
}

/** Authorization code → user token. */
export async function axiomExchange(
  clientId: string,
  code: string,
  verifier: string,
  redirectUri: string,
) {
  const res = await fetch(`${axiomOAuth().auth}/oauth2/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      code_verifier: verifier,
      redirect_uri: redirectUri,
      client_id: clientId,
    }),
  });
  const data = (await res.json().catch(() => ({}))) as OAuthError & { access_token?: string };
  if (!res.ok || !data.access_token) {
    throw new Error(`Axiom sign-in failed: ${oauthMessage(data, res.status)}`);
  }
  return data.access_token;
}

/** `aud` of a JWT, for error messages. Never the token. */
function audience(token: string) {
  try {
    const claims = JSON.parse(atob(token.split(".")[1]!.replace(/-/g, "+").replace(/_/g, "/")));
    return JSON.stringify(claims.aud ?? null);
  } catch {
    return "(not a JWT)";
  }
}

export type AxiomOrg = { id: string; name: string; domain: string };

/** Orgs the personal token can see, each with the API host its data lives on. */
export async function axiomOrgs(token: string): Promise<AxiomOrg[]> {
  const api = axiomOAuth().api;
  const res = await personal(api ?? DOMAINS[0], token, null, "/v2/orgs").catch((err: Error) => {
    // The sign-in token is issued for Axiom's MCP server; if its API ever stops taking it,
    // say so plainly instead of a bare 401.
    throw new Error(
      `${err.message} (Axiom API rejected the sign-in token, aud ${audience(token)})`,
    );
  });
  const orgs = (await res.json()) as {
    id: string;
    name: string;
    defaultEdgeDeployment?: string;
    region?: string;
  }[];
  return orgs.map((o) => ({
    id: o.id,
    name: o.name,
    // `cloud.eu-central-1.aws` (or the deprecated `region`) → the EU host.
    domain:
      api ?? (/eu-/.test(o.defaultEdgeDeployment ?? o.region ?? "") ? DOMAINS[1] : DOMAINS[0]),
  }));
}

/**
 * With the personal token: create the dataset (if missing) and mint an API token that can only
 * ingest into and query it. Returns the sink config to store.
 */
export async function axiomProvision(
  token: string,
  org: AxiomOrg,
  dataset: string,
  label: string,
): Promise<AxiomConfig> {
  if (!DATASET_RE.test(dataset)) throw new Error("Dataset name: letters, digits, - _ . only");
  await personal(org.domain, token, org.id, "/v2/datasets", {
    method: "POST",
    body: JSON.stringify({ name: dataset, description: "Keel container logs" }),
  }).catch((err: Error) => {
    if (!/exists|409/i.test(err.message)) throw err;
  });
  const res = await personal(org.domain, token, org.id, "/v2/tokens", {
    method: "POST",
    body: JSON.stringify({
      name: label,
      description: "Keel: workers ingest container logs, the control plane reads them back",
      datasetCapabilities: { [dataset]: { ingest: ["create"], query: ["read"] } },
      orgCapabilities: {},
    }),
  });
  const minted = (await res.json()) as { token?: string };
  if (!minted.token) throw new Error("Axiom did not return a token");
  return { domain: org.domain, dataset, token: minted.token };
}
