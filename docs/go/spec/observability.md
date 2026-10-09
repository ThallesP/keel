# Porting spec: observability (logs, log sinks, traces, OTLP relay, tracing switch)

Source of truth at the time of writing (worktree `go`, HEAD `36c2ded`):

| Area | TS source |
| --- | --- |
| Sink per organization, Sign in with Axiom | `packages/backend/convex/logSinks.ts`, `logProviders/axiom.ts` (OAuth half) |
| Log read side | `convex/logs.ts`, `logProviders/{axiom,docker,types}.ts`, `timeRange.ts` |
| Trace read side | `convex/traces.ts`, `traceProviders/{axiom,types}.ts` |
| OTLP relay | `convex/otlp.ts`, route in `convex/http.ts` |
| Tracing switch + env injection | `convex/tracing.ts`, called from `nodesInternal.applyInput` |
| Agent prompt | `convex/tracingPrompt.ts` |
| Worker config | `convex/worker.ts`, `GET /worker/config` in `convex/http.ts` |
| Log shipping (per-node agent) | `apps/worker/src/{logs,frames,docker,state,controlPlane,index,events}.ts`, `apps/worker/src/sinks/*` |
| Web consumers | `apps/web/src/components/canvas/observability/**`, `bottom-panel/log-stream.tsx`, `bottom-panel/tabs/{logs,tracing}.tsx`, `copy-prompt.tsx`, `settings.tsx`, `routes/_auth/axiom/callback.tsx`, `lib/axiom-sign-in.ts` |
| CLI consumers | `apps/cli/internal/keel/api.go` (`logs:tail`, `traces:overview`, `tracing:*`), `internal/cli/run.go` |
| Design doc | `docs/logs.md` |

In the Go rewrite all of this lives in one binary: the control plane (tables, API, OTLP relay, Docker log provider on the manager, Axiom read side) and the per-node agent (log shipper). Nothing here is a cron; the only scheduled work is two 10-minute expiries.

Conventions in this document:

- "Function" = a Convex function. Its Go equivalent is one JSON API operation. Names are given as `module.fn` (the web calls `api.module.fn`, the CLI calls `"module:fn"`).
- "Times" are epoch milliseconds as JSON **numbers**, often fractional (sub-ms precision matters for spans). The CLI decodes them as `float64`.
- "Error `X`" = a `ConvexError` whose data is the exact string `X`. The web shows the string verbatim (`errorMessage`: `String(err.data)`); the CLI maps some of them to codes by exact match (§16). A Go port must return these strings byte for byte (note the `…` U+2026 and `’` style quotes where they appear).
- "Uncaught" = a plain `Error` that is not a `ConvexError`: in a Convex production deployment the client only sees a generic `Server Error`. Listed where it happens; a Go port may return the message, but it must not collide with a mapped string.

---

## 1. Data model

### 1.1 `logSinks` (at most one per organization)

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `_id` | id | yes | |
| `_creationTime` | float ms | yes | **The connect time.** The worker starts reading containers with no resume point from here (`worker.config` `since`). Every (re)connect inserts a fresh row so this resets. A Go port needs an explicit `connected_at` column with the same semantics. |
| `organizationId` | string (Better Auth org id) | optional | The owning organization. Unset only on legacy rows (§1.7). |
| `projectId` | id → `projects` | optional | Legacy rows only: the project that connected it before sinks were per organization. |
| `sink` | `LogSink` (§1.6) | yes | Where logs/traces go and the token. |

Index: `by_organization [organizationId]`. Lookups use `.unique()` for an org (so at most one row per org is an invariant; a Go port should add a unique constraint on non-null `organization_id`).

### 1.2 `axiomClients` (DCR client per callback URL)

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `redirectUri` | string | yes | Exact redirect URI registered with Axiom, e.g. `https://keel.example.ts.net/axiom/callback`. |
| `clientId` | string | yes | Public OAuth client id Axiom returned. Not a secret. |

Index: `by_redirect [redirectUri]`. Read with `.first()`, written only if none exists (first writer wins). Never deleted.

### 1.3 `axiomSignIns` (PKCE state between authorize and callback)

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `organizationId` | string | yes | Org that started the sign-in. |
| `clientId` | string | yes | DCR client used. |
| `state` | string | yes | OAuth `state` (base64url of 16 random bytes, 22 chars). |
| `verifier` | string | yes | PKCE verifier (base64url of 32 random bytes, 43 chars). Secret. |
| `redirectUri` | string | yes | Same URI sent to authorize; required again by the token exchange. |

Indexes: `by_state [state]` (`.unique()`), `by_organization [organizationId]` (`.unique()`: at most one in-flight sign-in per org). Lifetime: deleted on use (`takeSignIn`), on a newer sign-in by the same org (`startSignIn`), or by the scheduled `dropSignIn` 10 minutes after insert.

### 1.4 `axiomPending` (personal token waiting for an org pick)

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `organizationId` | string | yes | Keel org. |
| `token` | string | yes | The Axiom **personal** (user) access token. Secret. Expires at Axiom 5 minutes after issue. |
| `orgs` | `AxiomOrg[]` (§1.6) | yes | Orgs the token can see. |

Index: `by_organization [organizationId]` (`.unique()`). Lifetime: deleted by `takePending` (org pick), `cancelAxiomSignIn`, a newer `stashPending`, or the scheduled `dropPending` 10 minutes after insert.

### 1.5 `otlpKeys` (OTLP relay ingest key per environment)

| Field | Type | Req | Meaning |
| --- | --- | --- | --- |
| `environmentId` | id → `environments` | yes | |
| `key` | string | yes | `keel_otlp_` + base64url(24 random bytes) (32 chars) = 42 chars total. Bearer for `POST /otlp/v1/traces`. Secret. |

Indexes: `by_key [key]` (`.unique()`), `by_environment [environmentId]` (`.first()`). At most one per environment (enforced by `saveKey` re-reading inside the mutation; Go: unique constraint on `environment_id`). Made on first use, **never rotated, never deleted** (not even when the environment or project is deleted: the row is orphaned and the relay then answers 401 because the environment lookup fails).

### 1.6 Embedded types

`LogSink` (union with one variant today):

```
{ kind: "axiom",
  domain: string,     // "api.axiom.co" | "api.eu.axiom.co"; a full origin ("http://127.0.0.1:4318") only via KEEL_ALLOW_LOCAL_SINKS=1 (mock)
  dataset: string,    // logs dataset ("keel-logs" when made by Sign in with Axiom)
  traces?: string,    // traces dataset ("keel-traces"); unset on sinks connected before traces existed
  token: string,      // Axiom API token, scoped to ingest:create + query:read on dataset (+ traces). Secret.
  org?: string }      // Axiom org *name*, display only (only set by Sign in with Axiom)
```

`AxiomOrg`: `{ id: string, name: string, domain: string /* API host its data lives on */, maxDatasets?: number /* license.maxDatasets */ }`.

`timeRange`: `"15m" | "1h" | "24h" | "7d"`.

### 1.7 Related fields owned by other areas

| Table.field | Used here for |
| --- | --- |
| `nodes.desired.tracing?: boolean` | The tracing switch (service nodes only). Absent = off. Written only by `tracing.setEnabled`. |
| `nodes.dirty?: boolean` | Set `true` by `tracing.setEnabled` (drives "Ship · N changes"). |
| `nodes.name`, `nodes.type`, `nodes.environmentId`, `nodes.desired` | `OTEL_SERVICE_NAME`; which nodes are services; scoping. |
| `variables (nodeId, key, …)` | The service's own env keys win over the tracing ones (`overridden`). |
| `environments.name`, `environments.projectId` | `deployment.environment.name`; scope. |
| `projects.organizationId`, `projects.slug` | Sink lookup; prompt names the project by slug. |
| Better Auth `member (userId, organizationId, role)`, `organization (slug, name)` | Caller's org; token name `keel-<org slug>`. |

### 1.8 Legacy per-project sink rows

Rows inserted before 2026-10-04 have `organizationId` unset and `projectId` set. `sinkOf(org)`:

1. The row with `organizationId == org` (unique), else
2. among rows with `organizationId` unset, ordered by `_creationTime` **descending**, the first whose `projectId` resolves to a project with `organizationId == org`, else `null`.

`clear(org)` deletes the org row and **every** legacy row of that org's projects. `save` = `clear` + insert. Legacy rows of other orgs are never touched.

Go port suggestion (behaviour-equivalent): a one-time migration that, per org without an org row, re-tags the newest legacy row (`organization_id = org`, keep its `_creationTime` as `connected_at`) and deletes the older ones; delete legacy rows whose project no longer exists. After that `sinkOf` is a single lookup. Also drop in-flight `axiomSignIns`/`axiomPending` rows at migration (they live ≤10 min).

---

## 2. Access rules

| Helper | Behaviour |
| --- | --- |
| `currentMembership(ctx)` | Signed-in user's Better Auth `member` row (first by `userId`) → `{ user, organizationId, role }`, else `null` (signed out, or no org). One org per install. |
| `requireOrganization(ctx)` | `currentMembership().organizationId`, else **error `You're not in an organization yet. Ask a member for an invite link.`** (`NO_ORGANIZATION`). |
| `ownedProject(id)` | Project iff it exists and `project.organizationId == membership.organizationId`; else `null` (also `null` when signed out). |
| `ownedEnvironment(id)` | `{ environment, project }` iff environment exists and its project is owned; else `null`. |
| `ownedNode(id)` | `{ node, environment, project }` iff node exists and its environment is owned; else `null`. |
| `requireNode(id)` | `ownedNode` or **error `Node not found`**. |

Consequence: a signed-out caller of any node/environment-scoped function here gets `Node not found` / `Environment not found` (never `Not authenticated`). Role is never checked: every member may connect/disconnect the sink and toggle tracing.

---

## 3. Shared helpers (exact semantics)

### 3.1 Time ranges (`timeRange.ts`)

| Range | `ms` | APL `bin` | `binMs` | buckets |
| --- | --- | --- | --- | --- |
| `15m` | 900 000 | `30s` | 30 000 | 30 |
| `1h` | 3 600 000 | `2m` | 120 000 | 30 |
| `24h` | 86 400 000 | `1h` | 3 600 000 | 24 |
| `7d` | 604 800 000 | `6h` | 21 600 000 | 28 |

```ts
rangeWindow(range, now = Date.now()) {
  count = Math.round(ms / binMs)
  from  = Math.floor(now / binMs) * binMs - (count - 1) * binMs   // epoch-aligned, like APL bin()
  return { from, to: from + count * binMs, count }                 // last bucket contains `now`
}
```

`logs.recent` and `traces.overview` each call `rangeWindow` independently (same formula, so the same `from` unless a bucket boundary passes between the two calls).

### 3.2 Axiom HTTP basics (`logProviders/axiom.ts`)

- `baseUrl(domain)`: `domain` containing `://` is used as-is, else `https://<domain>`; trailing `/` stripped.
- `call(cfg, path, init)` (API-token calls): `fetch(baseUrl(cfg.domain)+path)` with headers `authorization: Bearer <cfg.token>`, `content-type: application/json` (init headers override). Non-2xx → `Error("Axiom <status>")` or `Error("Axiom <status>: <body>")` where body = response text with every whitespace run collapsed to one space, trimmed, first 200 chars. No timeout.
- `personal(domain, token, orgId|null, path, init)`: same, plus header `x-axiom-org-id: <orgId>` when given. Same error format.

### 3.3 APL query

```
POST <base>/v1/datasets/_apl?format=tabular
Authorization: Bearer <token>      Content-Type: application/json
{ "apl": "<apl>", "startTime": ISO(sinceMs), "endTime": ISO(untilMs ?? now + 60 000) }
```

ISO = JS `toISOString()` (UTC, millisecond precision, `Z`). Response `{ tables: [{ fields: [{name}], columns: [[...], ...] }] }` (column-major). Rows = for `i < columns[0].length`, `row[fields[c].name] = columns[c][i]`. No `tables[0]` → `[]`.

"Empty dataset" rule (`tailQuery` for logs, `spans` for traces): an error whose message matches `/Axiom 400.*invalid field/` is returned as `[]` (a dataset nothing reached yet has no fields; APL rejects unknown fields). Any other error propagates. `tailQuery` default window: `sinceMs = now - 30 days`.

APL string literals: `"` + s with every `\` and `"` prefixed by `\` + `"` (JS `s.replace(/[\\"]/g, "\\$&")`). Node ids are inlined unescaped in log queries (Convex ids are alphanumeric; a Go port with other id formats must quote them with the same escaping).

### 3.4 Time parsing

- `preciseTime(x)` (log lines in `recent`/`around`/`traces.get`): `Date.parse(str)` (ms), plus the sub-millisecond digits: if the string matches `/\.\d{3}(\d+)/`, add `Number("0." + extraDigits)`. Unparseable → `0`.
- `axiomTail` uses plain `Date.parse(...) || 0` (integer ms, no sub-ms).
- `timeOf(x)` (spans, buckets, events): number → `x > 1e17 ? x/1e6 : x > 1e14 ? x/1e3 : x` (ns/µs/ms); all-digit string → same on `Number(x)`; else `preciseTime`; empty/non-string → `0`.

---

## 4. Module `logSinks`

Every public function acts on the **caller's** organization; none takes a project.

### 4.1 `logSinks.get` — public query (reactive)

Args: none. Returns `null` when signed out / no org / no sink, else:

```json
{ "kind": "axiom", "domain": "api.axiom.co", "dataset": "keel-logs",
  "traces": "keel-traces" | null, "org": "Acme" | null, "tokenHint": "…ab12" }
```

`tokenHint` = `"…"` (U+2026) + last 4 chars of the token. Never the token. Resolves legacy rows via `sinkOf`.

### 4.2 `logSinks.forNode` — internal query (runs with the caller's identity)

Args `{ nodeId }`. `ownedNode` → `null` if not owned. Else `{ sink: <full LogSink incl. token> | null }` (`null` when the project has no `organizationId` or the org has no sink).

### 4.3 `logSinks.forEnvironment` — internal query

Args `{ environmentId }`. `ownedEnvironment` → `null` if not owned. Else `{ sink: LogSink | null, serviceIds: string[] }` where `serviceIds` = ids of **every node of the environment whose `type` is not `volume` or `group`** (services, databases, caches; regardless of `desired`).

### 4.4 `logSinks.save` — internal mutation

Args `{ sink: LogSink }`. `requireOrganization` (caller identity) → `clear(org)` → insert `{ organizationId, sink }`. Always a new row (new connect time). Invalidate `org:<id>`.

### 4.5 `logSinks.connectAxiom` — public action (token paste; no UI, for scripts/agents and as OAuth fallback)

Args `{ domain: string, dataset: string, traces?: string, token: string }`. Returns `{ dataset, traces: string | null }`.

Order of checks (first failure wins):

1. `organizations.current` is null → error `NO_ORGANIZATION`.
2. `domain` not in `["api.axiom.co","api.eu.axiom.co"]` and not (`KEEL_ALLOW_LOCAL_SINKS=="1"` and `domain` contains `://`) → error `Region must be US or EU`.
3. For `dataset` and (if `traces !== undefined`) `traces`: must match `DATASET_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/` → else error `Dataset: letters, digits, - _ . only`.
4. `token.trim().length < 8` → error `That does not look like an Axiom API token`.
5. `axiomVerify(sink)` and, if `traces`, `axiomVerify({...sink, dataset: traces})`. Any thrown error → error `<its message>` (e.g. `Axiom 403: forbidden`).
6. `logSinks.save({ kind: "axiom", domain, dataset, traces, token: trimmed })` (no `org`; `traces` omitted when undefined).

`axiomVerify(cfg)`:
- (`DATASET_RE` re-check → `Dataset name: letters, digits, - _ . only`; unreachable after step 3.)
- `POST /v2/datasets {"name": dataset, "description": "Keel container logs"}` (same description for the traces dataset on this path). On error: rethrow **only if** the message does not match `/exists|409/i` **and** matches `/40[13]/`; everything else (409, 400, 5xx) is ignored.
- `axiomCanQuery(cfg)`: APL `['<dataset>'] | limit 1` over `[now-60s, now+60s]`. Errors propagate (this is what proves the token).

### 4.6 `logSinks.disconnect` — public mutation

Args none. `requireOrganization` → `clear(org)`. Returns `null`. Does **not** touch `otlpKeys`, `desired.tracing` or `dirty`: traced services keep their OTEL vars and the relay then accepts-and-drops (§9). Invalidate `org:<id>`.

### 4.7 Sign in with Axiom (OAuth 2.0 authorization code + PKCE, with Dynamic Client Registration)

Axiom endpoints (overridable for a mock, §15):

- Auth server `AUTH = KEEL_AXIOM_AUTH_URL` (only when `KEEL_ALLOW_LOCAL_SINKS=1`) else `https://authorization.axiom.co`, trailing `/` stripped.
- API host for org listing `API = KEEL_AXIOM_API_URL` (only with the flag) else `api.axiom.co` (via `baseUrl`). When `API` is overridden it also becomes every org's `domain`.

Sequence:

```
browser                         control plane                         Axiom
Sign in ─ beginAxiomSignIn(<origin>/axiom/callback) ─▶ DCR (once per URI) ─▶ POST AUTH/oauth2/register
        ◀─ { url } (authorize URL, S256 challenge) ── store axiomSignIns(state, verifier)
─▶ AUTH/oauth2/authorize … user consents, picks an org ─▶ 302 <origin>/axiom/callback?code&state
callback page ─ signInAxiom(state, code) ─▶ take row ─▶ POST AUTH/oauth2/token ─▶ GET API/v2/orgs
   one org, or the token's axiomDefaultOrg claim names a listed org ─▶ provision ─▶ sink saved   → {choose:false, dataset, org}
   otherwise ─▶ axiomPending(token, orgs)                                                         → {choose:true}
Observability shows picker (pendingOrgs) ─ chooseAxiomOrg(orgId) ─▶ provision ─▶ sink saved     → {dataset, org}
```

#### `logSinks.beginAxiomSignIn` — public action

Args `{ redirectUri: string }` → `{ url: string }`.

1. Parse as URL; failure → error `Bad redirect URI`. Protocol must be `http:` or `https:` and `pathname` must equal exactly `/axiom/callback` → else error `Bad redirect URI`. (Query string not checked; the URI is stored and sent verbatim.)
2. `clientFor(redirectUri)` (internal query, no auth): `axiomClients.by_redirect.first()?.clientId`.
3. If none: DCR (no auth check before this; an org-less caller can trigger a registration, then fails at step 5):
   ```
   POST AUTH/oauth2/register   Content-Type: application/json
   {"client_name":"Keel","redirect_uris":[redirectUri],"grant_types":["authorization_code"],
    "response_types":["code"],"token_endpoint_auth_method":"none"}
   ```
   Body parsed as JSON (parse failure → `{}`). Non-2xx or no `client_id` → error `Axiom refused to register Keel: <msg>` where `msg = error_description || error || "HTTP <status>"`. Then `saveClient` (internal mutation: insert only if no row for the URI).
4. `axiomAuthorizeUrl(clientId, redirectUri)`: `verifier = base64url(32 random bytes)`, `state = base64url(16 random bytes)`, `challenge = base64url(SHA-256(ASCII verifier))`; base64url = standard base64 with `+`→`-`, `/`→`_`, trailing `=` stripped. URL = `AUTH/oauth2/authorize?` + form-encoded (`URLSearchParams`, space → `+`), in this order: `client_id`, `response_type=code`, `redirect_uri`, `scope=openid profile email`, `state`, `code_challenge`, `code_challenge_method=S256`.
5. `startSignIn` (internal mutation): `requireOrganization` (→ `NO_ORGANIZATION`); delete the org's existing `axiomSignIns` row; insert `{ organizationId, clientId, state, verifier, redirectUri }`; schedule `logSinks.dropSignIn({ id })` after **600 000 ms**.
6. Return `{ url }`.

Verifier/state are made server-side on purpose (the browser may be on plain http where `crypto.subtle` is missing). DCR refuses plain-http non-localhost redirect URIs (`invalid_redirect_uri`): Keel must be served over https or `http://localhost` for this to work.

#### `logSinks.signInAxiom` — public action (called by the `/axiom/callback` web route)

Args `{ state: string, code: string }` → `{ choose: true } | { choose: false, dataset: string, org: string }`.

1. `takeSignIn(state)` (internal mutation): row by `state` (`.unique()`); if no row or `row.organizationId != caller's org` (incl. no membership) → `null` (row **not** deleted on org mismatch); else delete it and return `{ clientId, verifier, redirectUri }`. `null` → error `Axiom sign-in expired, try again`.
2. Token exchange:
   ```
   POST AUTH/oauth2/token   Content-Type: application/x-www-form-urlencoded
   grant_type=authorization_code&code=<code>&code_verifier=<verifier>&redirect_uri=<redirectUri>&client_id=<clientId>
   ```
   Non-2xx or no `access_token` → error `Axiom sign-in failed: <error_description || error || "HTTP <status>">`.
3. `axiomOrgs(token)`: `GET <API>/v2/orgs` with `Authorization: Bearer <token>` (no org header). Error → error `<Axiom N…> (Axiom API rejected the sign-in token, aud <aud>)` where `<aud>` = `JSON.stringify(claims.aud ?? null)` of the JWT payload (base64url-decoded middle segment, unverified), or `(not a JWT)` if undecodable. Each org maps to `{ id, name, maxDatasets: license?.maxDatasets, domain }` with `domain = API override ?? (/eu-/ matches (defaultEdgeDeployment ?? region ?? "") ? "api.eu.axiom.co" : "api.axiom.co")`.
4. `orgs.length == 0` → error `This Axiom account has no organization`.
5. `chosen` = the JWT claim `axiomDefaultOrg` if it is a string (undocumented; an org id like `ramp-vcrw`), else null. `org = orgs.length == 1 ? orgs[0] : orgs.find(o => o.id == chosen)`.
6. `org` found → `provision(token, org)` → `{ choose: false, dataset, org }`.
7. Else `stashPending({ token, orgs })` (internal mutation: `requireOrganization`; delete the org's existing pending row; insert; schedule `logSinks.dropPending({ id })` after 600 000 ms) → `{ choose: true }`.

Wrapping: errors from steps 2–3 are re-thrown as `ConvexError(message)`; `provision` wraps its own (below).

#### `provision(token, org)` (shared by `signInAxiom` and `chooseAxiomOrg`)

1. `organizations.current` → null → error `NO_ORGANIZATION`. `label = "keel-" + <Keel org slug>`.
2. `axiomProvision(token, org, label)` (all calls `personal(org.domain, token, org.id, …)`, i.e. with `x-axiom-org-id`):
   1. `GET /v2/datasets` → `[{ name, sharedByOrg? }]`. Error → `Listing datasets: <msg>`. `have` = all names; `own` = names without `sharedByOrg` (Axiom's shared samples don't count toward the cap).
   2. `missing` = those of `[["keel-logs","Keel container logs"],["keel-traces","Keel OpenTelemetry traces"]]` not in `have`. For each, in order: `POST /v2/datasets {"name","description"}`. On error: `left` = names in `missing` still not in `have` (this one and later ones); `cap = org.maxDatasets ?? Infinity`; if message matches `/^Axiom 400\b/` **and** `own.length >= cap` →
      `"<org.name> is at its Axiom plan's limit of <cap> datasets (<own joined ", ">). Keel needs <left joined " and ">: delete <own.length + left.length - cap> in Axiom or pick another org. (<original message>)"`;
      else `Creating <name>: <msg>`. On success add the name to `have` and `own`. (No up-front cap check, by design.)
   3. Mint the scoped token:
      ```
      POST /v2/tokens
      {"name":"<label>","description":"Keel: logs and traces go in, the control plane reads them back",
       "datasetCapabilities":{"keel-logs":{"ingest":["create"],"query":["read"]},
                              "keel-traces":{"ingest":["create"],"query":["read"]}},
       "orgCapabilities":{}}
      ```
      Error → `Minting the ingest token: <msg>`. No `token` in the response → `Axiom did not return a token`.
   4. Result `{ domain: org.domain, dataset: "keel-logs", traces: "keel-traces", token: <minted> }`.
3. For `keel-logs` then `keel-traces`: `axiomCanQuery` with the minted token (no org header). Error → `Querying <dataset>: <msg>`.
4. `logSinks.save({ kind: "axiom", domain, dataset, traces, token, org: org.name })`.
5. Return `{ dataset: "keel-logs", org: org.name }`.

Any non-ConvexError inside is re-thrown as `ConvexError(message)`. The personal token is never stored in `logSinks`. The token being replaced stays valid in Axiom (not revoked). Re-signing in is how a pre-traces sink gains `traces`.

#### `logSinks.chooseAxiomOrg` — public action

Args `{ orgId: string }` → `{ dataset, org }`.
1. `takePending()` (internal mutation): no membership → null; the org's pending row → delete it, return `{ token, orgs }`; none → null. `null` → error `Sign-in expired, sign in with Axiom again`.
2. `orgs.find(o => o.id == orgId)` missing → error `Organization not found` (the pending row is already consumed: the user must sign in again).
3. `provision(token, org)`.

#### `logSinks.pendingOrgs` — public query (reactive)

Args none. No membership → `null`. Pending row → `[{ id, name }]` (names only, no domain/token). None → `null`.

#### `logSinks.cancelAxiomSignIn` — public mutation

Args none. No membership → no-op. Deletes the org's pending row if any. Returns `null`.

#### Scheduled expiries — internal mutations

- `logSinks.dropSignIn({ id })`: delete the `axiomSignIns` row if it still exists.
- `logSinks.dropPending({ id })`: delete the `axiomPending` row if it still exists (invalidate `org:<id>`).

Go: either a timer/job queue keyed by row id, or store `expires_at` and treat expired rows as absent on read plus a periodic sweep. Behaviour that must hold: after 10 minutes the state/pick is gone; deleting by id must not delete a newer row of the same org.

---

## 5. Module `logs` (read side; non-reactive, called as actions)

Result types (`logProviders/types.ts`):

```
LogLine     = { time: number, text: string, stream: "stdout"|"stderr", task: string /* "" when unknown */ }
Replica     = { task: string, slot: number, state: string }
Tail        = { source: "docker"|"axiom", lines: LogLine[], replicas: Replica[] }
ProjectLine = LogLine & { serviceId: string }
ProjectTail = { source: "docker"|"axiom", lines: ProjectLine[] }
```

### 5.1 `logs.tail` — public action (the per-service Logs tab and `keel logs`)

Args `{ nodeId: Id<nodes>, tail?: number /* default 200 */ }` → `Tail`.

1. `forNode(nodeId)` null → error `Node not found`.
2. `n = min(max(1, floor(tail)), 1000)`.
3. `sink.kind == "axiom"` → `axiomTail(sink, nodeId, n)`; else `dockerTail(nodeId, n)`.

Provider errors are **uncaught** (not wrapped in ConvexError) on this path.

`axiomTail(cfg, serviceId, n)`:
- APL: `['<dataset>'] | where service_id == "<serviceId>" | sort by _time desc | limit <n> | project _time, message, stream, task, replica`, window `[now-30d, now+60s]`, "invalid field" → `[]`.
- `lines` = rows mapped to `{ time: Date.parse(_time) || 0, text: str(message), stream: stream == "stderr" ? "stderr" : "stdout", task: str(task) }`, then **reversed** (oldest first). `str(x)` = string as-is, null/undefined → `""`, else `String(x)`.
- `replicas`: iterate rows (newest first); first time a non-empty `task` is seen, record `slot = Number(replica) || 0`; output `{ task, slot, state: "" }` sorted by `slot` asc then `task` (localeCompare).
- `source: "axiom"`.

`dockerTail(nodeId, n)` — runs on the **manager**, Docker socket `/var/run/docker.sock` (hard-coded path):
- In parallel:
  - `GET /services/svc-<nodeId>/logs?stdout=1&stderr=1&tail=<n>&timestamps=1&details=1` (non-follow; dockerode sends `true`). HTTP 404 → return `{ source: "docker", lines: [], replicas: [] }` immediately. Other errors propagate (uncaught).
  - `GET /tasks?filters={"service":["svc-<nodeId>"]}`; any error → `[]`.
- `replicas` = every task (Swarm keeps task history, so exited/shutdown tasks are included) as `{ task: ID, slot: Slot ?? 0, state: Status.State ?? "unknown" }`, sorted by slot then task.
- `lines` = `demux(body)`:
  - Multiplexed frames: 8-byte header `[type, 0, 0, 0, len u32 BE]` then `len` payload bytes. Loop while a full header is present; stop at `type > 2` or a truncated frame. Collect payloads per stream (`type 2` = stderr, else stdout) as UTF-8.
  - If no frame was parsed (`off == 0`) and the body is non-empty → treat the whole body as stdout text (TTY service).
  - Else: all stdout text concatenated, split on `\n`, then all stderr text likewise; drop empty lines; strip one trailing `\r`; parse each line; concatenate stdout lines + stderr lines and **stable-sort by `time` ascending**.
  - Line parse: first space-separated token, if it ends with `Z` and `Date.parse(token with fractional seconds truncated to 3 digits)` is valid → `time` = that, rest after the space. Then if the next token (up to the next space, or the whole rest) starts with `com.docker.swarm.` it is the details block (`k=v,k=v`): `task` = value of `com.docker.swarm.task.id=`; the text is what follows the details token (or `""`). Lines without a stamp keep `time: 0`.
- `source: "docker"`.

### 5.2 `logs.recent` — public action (Observability page stream)

Args `{ environmentId, search?: string /* default "" */, tail?: number /* default 300 */, range?: timeRange }` → `ProjectTail` (`source: "axiom"`, lines **oldest first**).

1. `forEnvironment` null → error `Environment not found`.
2. `sink.kind != "axiom"` → error `Connect Axiom to search all logs`.
3. `n = min(max(1, floor(tail)), 1000)`; `from = range ? rangeWindow(range).from : undefined` (→ default `now-30d`).
4. `axiomLines(sink, serviceIds, { n, search: search.slice(0, 200), from })`, newest `n`, returned oldest first. Any error → error `<message>`.

`axiomLines(cfg, serviceIds, { n, search = "", from, to, oldestFirst = false })`:
- `serviceIds` empty → `[]` (no query).
- `term = search.trim()`; where-clause ` | where message contains "<escaped term>"` only if non-empty (APL `contains` is case-insensitive).
- APL: `['<dataset>'] | where service_id in ("<id1>", "<id2>", …)<where> | sort by _time <asc|desc> | limit <n> | project _time, message, stream, task, service_id` (`asc` iff `oldestFirst`), window `[from ?? now-30d, to ?? now+60s]`, "invalid field" → `[]`.
- Row → `{ time: preciseTime(_time), text: str(message), stream: stderr|stdout, task: str(task), serviceId: str(service_id) }`. Result reversed unless `oldestFirst` (always oldest first out).

### 5.3 `logs.around` — public action (context of a line that names no trace)

Args `{ environmentId, at: number }` → `ProjectLine[]` (oldest first).

Same two checks/errors as `recent`, then `axiomLines(sink, serviceIds, { n: 500, from: at - 30 000, to: at + 30 000, oldestFirst: true })`. Errors → error `<message>`.

---

## 6. Module `traces` (read side; non-reactive actions; no Docker fallback)

Result types (`traceProviders/types.ts`, all times epoch ms and durations ms, fractional):

```
Attribute    = { key: string, value: string }           // lists sorted by key
SpanEvent    = { time, name, attributes: Attribute[] }
Span         = { spanId, parentId /* "" for root */, name, service /* service.name */,
                 kind /* server|client|internal|producer|consumer|"" */, start, duration,
                 status: "ok"|"error"|"unset", statusMessage, scope /* scope.name */,
                 attributes: Attribute[], resource: Attribute[], events: SpanEvent[] }
TraceSummary = { traceId, name, service, kind, start, duration, httpStatus: number|null,
                 spans: number, errors: number, error: boolean, local: boolean }
TraceBucket  = { time, requests, errors, p50: number|null, p95: number|null, p99: number|null }
TraceStats   = TraceBucket minus time
TraceOverview= { source: "axiom", from, to, bucketMs, stats: TraceStats,
                 buckets: TraceBucket[] /* every bucket, oldest first, empty ones included */,
                 traces: TraceSummary[] /* newest first, ≤ 100 */ }
Trace        = { source: "axiom", traceId, spans: Span[], logs: ProjectLine[] }
```

### 6.1 Scope helper `axiomScope(environmentId)`

`forEnvironment` null → error `Environment not found`; `sink.kind != "axiom"` → error `Connect Axiom to see traces` (`NO_SINK`). Returns `{ logs: sink without traces, traces: sink.traces ? {...sink, dataset: sink.traces} : null, serviceIds }`.

### 6.2 APL building blocks (exact strings)

```
ROOT       = where isempty(ensure_field("parent_span_id", typeof(string)))
FAILED     = ensure_field("error", typeof(bool)) == true or ensure_field("status.code", typeof(string)) contains "error"
SERVICE_ID = tostring(ensure_field("resource.custom", typeof(dynamic))["keel.service_id"])
STATS      = requests = count(), errors = countif(failed), p50 = percentile(duration, 50), p95 = percentile(duration, 95), p99 = percentile(duration, 99)

roots(ids, search) =
  ['<traces>'] | ROOT | where SERVICE_ID in ("<id1>", "<id2>")
  + (term = search.trim(); term ? ` | where name contains "<lit term>" or ensure_field("service.name", typeof(string)) contains "<lit term>"` : "")
```

Ids and terms go through the APL literal escaping (§3.3). A request = a root span. `keel.service_id` is a non-semantic-convention resource attribute, so Axiom files it in the `resource.custom` map; spans without it are invisible here.

`requests(cfg, ids, search, from, to, limit)` (`ids` empty → `[]`):
1. `latest` = `<roots(ids, search)> | sort by _time desc | limit <limit>` over `[from, to ?? now+60s]`.
2. `traceIds` = distinct `str(trace_id)` of `latest` that match `TRACE_ID_RE = /^[0-9a-f]{16,32}$/i`. If any: `['<traces>'] | where trace_id in ("<t1>", …) | extend failed = FAILED | summarize spans = count(), errors = countif(failed) by trace_id` over `[from, now+60s]` (deliberately not `to`: children start after the root).
3. Map each `latest` row (order kept, newest first) to `TraceSummary`:
   - `traceId = str(trace_id)`, `name = str(name)`, `service = str(pick(r,"service.name"))`, `kind = kindOf(kind)`, `start = timeOf(_time)`, `duration = durationOf(duration)`.
   - `error = statusOf(r) == "error"`.
   - `httpStatus`: `Number(attr(r,"http.response.status_code") ?? attr(r,"http.status_code"))`, kept if finite and `> 0`, else `null`.
   - `spans = counts?.spans ?? 1`, `errors = counts?.errors ?? (error ? 1 : 0)`.
   - `local = pick(r, "resource.deployment.environment.name") === "local"`.

All span queries use the "invalid field" → `[]` rule.

### 6.3 `traces.overview` — public action (Observability KPIs/charts/requests; `keel traces`)

Args `{ environmentId, range: timeRange, search?: string /* default "" */, nodeId?: Id<nodes> }` → `TraceOverview`.

1. `axiomScope` (errors above). `traces == null` → error `Sign in with Axiom again to turn on traces` (`NO_TRACES`).
2. `nodeId` given and not in `serviceIds` → error `Node not found`. `ids = nodeId ? [nodeId] : serviceIds`.
3. `{from, to, count} = rangeWindow(range)`; `search = search.slice(0, 200)`; `matching = roots(ids, search)`.
4. If `ids` empty: no queries (totals/series/traces empty). Else in parallel:
   - totals: `<matching> | extend failed = FAILED | summarize STATS` over `[from, to]`
   - series: `<matching> | extend failed = FAILED | summarize STATS by bin(_time, <bin>)` over `[from, to]`
   - traces: `requests(ids, search, from, undefined, 100)`
5. Buckets: for each series row, `t = floor(timeOf(r._time ?? <first column value>) / binMs) * binMs`, map `t → { time: t, ...statsOf(r) }`. Output `count` buckets `time = from + i*binMs`, missing ones `{ requests: 0, errors: 0, p50: null, p95: null, p99: null }`.
6. `stats` = `statsOf(totals[0])` or the zero/null stats. `statsOf(r)`: `requests = num(r.requests)`, `errors = num(r.errors)`, `pXX = (r.pXX == null || r.pXX === "") ? null : durationOf(r.pXX)`. `num(x)` = number as-is else `Number(x) || 0`.
7. Return `{ source: "axiom", from, to, bucketMs: binMs, stats, buckets, traces }`. Provider errors → error `<message>`.

Totals and series stop at `to` (end of the last bucket) so totals equal the sum of buckets.

### 6.4 `traces.get` — public action (trace waterfall)

Args `{ environmentId, traceId: string, at?: number }` → `Trace`.

1. `traceId` not matching `TRACE_ID_RE` → error `Not a trace id` (checked **before** scope/auth). `id = traceId.toLowerCase()`.
2. `axiomScope` (errors above). **Not** an error when `traces == null`: spans are `[]`, lines still searched.
3. Spans (`traces != null`): `['<traces>'] | where trace_id == "<id>" | sort by _time asc | limit 2000` over `[at ? at - 3 600 000 : now - 7d, now+60s]` → `spanOf` each (§7). Not scoped to the environment: the trace id is the key.
4. Lines window (`LOG_SLACK_MS = 5000`, `LOG_WINDOW_MS = 900 000`):
   ```
   from = spans.length ? min(...spans.start, at ?? +Inf) - 5000 : at ? at - 900000 : now - 7d
   to   = spans.length ? max(...(start+duration), at ?? -Inf) + 5000 : at ? at + 900000 : undefined
   ```
5. `logs = axiomLines(logsCfg, serviceIds, { n: 500, search: id, from, to, oldestFirst: true })`.
6. Return `{ source: "axiom", traceId: id, spans, logs }`. Any error in 3–5 → error `<message>`.

### 6.5 `traces.around` — public action (requests near a line)

Args `{ environmentId, at: number }` → `TraceSummary[]` (newest first).

`axiomScope` (errors above); `traces == null` → `[]`; else `requests(serviceIds, "", at - 30000, at + 30000, 100)`. Errors → error `<message>`.

---

## 7. Span row parsing (defensive; must match exactly)

Rows come back with every dataset field as a column; dotted names flat (`attributes.http.method`), maps as objects (`attributes.custom`).

- `pick(obj, path)`: not an object → undefined. If `path` is an own key → its value. Else for each `.` position `i` left to right with `head = path[:i]` an existing key, recurse `pick(obj[head], path[i+1:])`; first non-undefined wins.
- `attr(row, name) = pick(row, "attributes."+name) ?? pick(row, "attributes.custom."+name)`.
- `durationOf(x)` → ms: number → `x / 1e6` (ns); all-digit/decimal string → `Number(x)/1e6`; else sum over regex `/(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|h|m|s)/g` with units ns 1e-6, us/µs/μs 1e-3, ms 1, s 1000, m 60 000, h 3 600 000 (Go duration strings like `1m30.5s`, `5ms`); if no match, .NET `[d.]hh:mm:ss[.f]` (`/^(?:(\d+)\.)?(\d+):(\d+):(\d+(?:\.\d+)?)$/`) → `((d*24+h)*3600 + m*60 + s) * 1000`; else `0`.
- `kindOf(x)`: `str(x).toLowerCase()` minus leading `span_kind_`; `"unspecified"` → `""`.
- `statusOf(row)`: `code = lower(str(pick(row,"status.code")))`; `pick(row,"error") === true` or `code` contains `error` → `"error"`; else `code` contains `ok` → `"ok"`; else `"unset"`.
- `flatten(out, key, value)`: skip `null`/`undefined`/`""`; plain object (not array) → recurse with `key ? key+"."+k : k`; else if `key` non-empty → `out[key] = text(value)` where `text` = string as-is, object/array → `JSON.stringify`, else `String`.
- `collect(row, root)` for `root ∈ {attributes, resource}`: for each row key `k`: `k == root` → `flatten(out, "", v)`; `k` starts with `root+"."` → `flatten(out, k[len(root)+1:], v)`. Then strip a leading `custom.` from every key (later entries overwrite earlier on collision, insertion order), sort by key (`localeCompare`).
- `eventsOf(x)`: not an array → `[]`; for each object element: `{ time: timeOf(e.time ?? e.timestamp ?? e._time ?? e.timeUnixNano), name: str(e.name), attributes: sorted(flatten(e.attributes)) }`.
- `spanOf(r)`: `{ spanId: str(span_id), parentId: str(parent_span_id), name: str(name), service: str(pick(r,"service.name")), kind: kindOf(kind), start: timeOf(_time), duration: durationOf(duration), status: statusOf(r), statusMessage: str(pick(r,"status.message")), scope: str(pick(r,"scope.name")), attributes: collect(r,"attributes"), resource: collect(r,"resource"), events: eventsOf(pick(r,"events")) }`.

Expected Axiom span columns (as observed): `_time`, `trace_id`, `span_id`, `parent_span_id`, `name`, `kind`, `duration` (ns, or a Go duration string in tabular results), `error` (bool), `status.code`, `status.message`, `service.name`, `scope.name`, `attributes.*` (+ `attributes.custom` map), `resource.*` (`resource.deployment.environment.name` is a column; `keel.service_id`/`keel.environment_id` in `resource.custom`), `events`.

---

## 8. Module `tracing` (the per-service switch and env injection)

Constants: `NO_SINK = "Connect Axiom to see traces"`, `NO_TRACES = "Sign in with Axiom again to turn on traces"`, `HEADERS = "OTEL_EXPORTER_OTLP_HEADERS"`, `KEY_PREFIX = "keel_otlp_"`.

### 8.1 Endpoint and the variable set

```
endpoint() = (KEEL_OTLP_URL || (CONVEX_SITE_URL ?? "") + "/otlp") with trailing "/" stripped
```

`tracingEnv(node, environment, key, { local })` → ordered `[key, value]` list:

| # | Key | Value |
| --- | --- | --- |
| 1 | `OTEL_EXPORTER_OTLP_ENDPOINT` | `endpoint()` — **omitted when `local`** |
| 2 | `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` |
| 3 | `OTEL_EXPORTER_OTLP_HEADERS` | `Authorization=Bearer%20<key>` |
| 4 | `OTEL_SERVICE_NAME` | `node.name` |
| 5 | `OTEL_RESOURCE_ATTRIBUTES` | `keel.service_id=<enc(node._id)>,keel.environment_id=<enc(environment._id)>,deployment.environment.name=<enc(local ? "local" : environment.name)>` (`enc` = `encodeURIComponent`) |
| 6 | `OTEL_TRACES_EXPORTER` | `otlp` |
| 7 | `OTEL_METRICS_EXPORTER` | `none` |
| 8 | `OTEL_LOGS_EXPORTER` | `none` |

The SDKs append `/v1/traces` to the endpoint, so deployed services post to `<endpoint>/v1/traces` = the relay (§9).

`overridden(ownKeys, k) = ownKeys.has(k) || (k == HEADERS && (ownKeys.has("OTEL_EXPORTER_OTLP_ENDPOINT") || ownKeys.has("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")))` — the service's own variables win key by key, and the ingest key is never sent to an endpoint the service chose itself.

### 8.2 `withTracing(node, env)` — applied at Swarm apply time

Called by `nodesInternal.applyInput` as `env = withTracing(node, computeEnv(node))`, where `computeEnv` yields `KEY=value` strings from the node's variables (references expanded). Rules:

- `node.desired.tracing` falsy → `env` unchanged.
- Load environment and `keyFor(environmentId)`; either missing → `env` unchanged (no key = nothing to send).
- `own` = set of keys in `env` (text before the first `=`). Append, in table order, every `k=v` from `tracingEnv(..., { local: false })` with `!overridden(own, k)`. Own variables come first in the Swarm `ContainerSpec.Env`.

Turning tracing off and shipping removes all eight (they are never stored, only computed).

### 8.3 `tracing.scope` — internal query

Args `{ nodeId }`. `requireNode` (→ `Node not found`); `node.type != "service"` or no `desired` → error `Only services can be traced`. Returns `{ node, environment, traces: tracesState(project) }` where `tracesState` = `"off"` (project without org, or org without an axiom sink), `"old"` (sink without `traces`), `"on"`.

### 8.4 `tracing.setEnabled` — internal mutation

Args `{ nodeId, on: boolean }`. `requireNode`. If no `desired` or `(desired.tracing ?? false) == on` → no-op (does **not** touch `dirty`). Else patch `desired` = `{...desired, tracing: true}` when on, or `desired` **without the `tracing` key** when off; and `dirty: true`. No revision bump (Ship does that). Toggling on then off before a ship leaves `dirty: true`. Invalidate `node:<id>` and `env:<environmentId>` (the canvas counts dirty nodes).

### 8.5 `tracing.enable` — public action (Settings tab switch; `keel tracing enable|disable`)

Args `{ nodeId, on: boolean }` → `null`.
1. `scope(nodeId)` (errors above).
2. If `on`: `traces == "off"` → error `NO_SINK`; `"old"` → error `NO_TRACES`; then `ensureKey(environmentId)` (§9.1). Turning **off** has no precondition.
3. `setEnabled(nodeId, on)`.

### 8.6 `tracing.forNode` — public query (reactive; Settings tab; `keel tracing`)

Args `{ nodeId }`. `ownedNode` null, or not a service, or no `desired` → `null`. Else:

```json
{ "enabled": false,
  "traces": "off" | "old" | "on",
  "env": [ { "key": "OTEL_EXPORTER_OTLP_ENDPOINT", "value": "<endpoint()>", "secret": false, "overridden": false },
           { "key": "OTEL_EXPORTER_OTLP_HEADERS",  "value": "Authorization=Bearer%20keel_otlp_…ab12", "secret": true, "overridden": false },
           … all 8 in table order … ] }
```

- `enabled = desired.tracing ?? false`.
- The key in `env` is masked: `keel_otlp_…<last 4>` or `keel_otlp_…` when the environment has no key yet (`…` = U+2026).
- `secret` is true only for `OTEL_EXPORTER_OTLP_HEADERS`.
- `overridden` uses the node's **variable row keys** (raw `variables` table, not the computed env).
- Built with `local: false` (so the endpoint row is present).

### 8.7 `tracing.localEnv` — public action (`keel run`)

Args `{ nodeId }` → `{ env: Record<string,string> | null, reason: string | null }`.
`scope` (errors above). `traces != "on"` → `{ env: null, reason: traces == "off" ? NO_SINK : NO_TRACES }` (not an error: the run proceeds without tracing). Else `key = ensureKey(environmentId)`; `env` = object of `tracingEnv(..., { local: true })` (7 keys, no endpoint, `deployment.environment.name=local`); `reason: null`.

The CLI then adds `OTEL_EXPORTER_OTLP_ENDPOINT = <ConvexSiteURL>/otlp` (the address the laptop reaches) and layers: shell env > service variables > tracing vars; skips `OTEL_EXPORTER_OTLP_HEADERS` if the shell/vars already set an own endpoint. A local run reuses the deployed service's id, so its requests show in the environment, tagged `local: true`.

### 8.8 `tracing.prompt` — public query (reactive; Copy agent prompt; `keel tracing prompt`)

Args `{ nodeId?: Id<nodes>, environmentId?: Id<environments> }` → string. Never throws.
- `nodeId` given, owned, and `type == "service"` → `agentPrompt({ service: node.name, project: project.slug })`.
- Else (incl. a non-service node) `environmentId` given and owned → `agentPrompt({ project: project.slug })`.
- Else `agentPrompt({})` (also for signed-out callers).

Text in §10.

---

## 9. Module `otlp` (the relay)

### 9.1 Keys

- `keyFor(environmentId)`: `otlpKeys.by_environment.first()?.key ?? null`.
- `keyOf` — internal query wrapper.
- `saveKey({ environmentId, key })` — internal mutation: if the environment already has a key return it (discarding the new one), else insert and return `key`. Serializable, so concurrent `ensureKey` calls converge on one key.
- `ensureKey(environmentId)` (in actions): `keyOf` → existing; else `saveKey(KEY_PREFIX + base64url(24 random bytes))`. Invalidate `env:<id>` on insert (forNode's masked key).
- `route({ key })` — internal query: row by key (`.unique()`); none → `null`. Environment → project; project missing or without `organizationId` → `null`. `sinkOf(org)?.sink`: not axiom, or no `traces` → `{ sink: null }`; else `{ sink: { domain, dataset: sink.traces, token } }`. Looked up **per request**: a new or replaced sink takes effect without redeploying services; apps never see the sink token.

### 9.2 `POST /otlp/v1/traces` (HTTP route, public, no session auth)

Steps, in order (first failure answers):

| Step | Condition | Response |
| --- | --- | --- |
| 1 | `Authorization` header must start with `Bearer ` (case-sensitive); `key` = rest, trimmed. Key not starting with `keel_otlp_`, or `route(key)` null | `401`, body `unauthorized` |
| 2 | `type` = `Content-Type` up to the first `;`, trimmed, lowercased; not `application/x-protobuf` or `application/json` | `415`, body `OTLP over HTTP: application/x-protobuf or application/json` |
| 3 | `Number(Content-Length ?? 0) > 4 194 304` | `413`, body `too large` |
| 4 | Read body; `byteLength > 4 194 304` | `413`, body `too large` |
| 5 | `route.sink == null` (org has no sink, or a pre-traces one) | `200`, body `{}` if `type == application/json` else empty, `Content-Type: <type>` (spans accepted and dropped so exporters don't log failures every few seconds) |
| 6 | Forward: `POST <baseUrl(domain)>/v1/traces` with headers `authorization: Bearer <sink token>`, `x-axiom-dataset: <traces dataset>`, `content-type: <type>` (normalized, without parameters), and `content-encoding: <same>` iff the request had one. Body = the request bytes **unchanged** (protobuf or JSON, gzip or not; never decoded). | |
| 7 | `fetch` throws (network) | `503`, body `sink unreachable` (logs `otlp: Axiom unreachable: <msg>`) |
| 8 | Axiom 2xx | `200`, Axiom's response body, `Content-Type: <Axiom's content-type ?? type>` |
| 9 | Axiom 429/502/503/504 | same status, body = `detail` (Axiom text, whitespace collapsed, trimmed, ≤200 chars) |
| 10 | Axiom other ≥500 | `503`, body `detail \|\| "rejected"` |
| 11 | Axiom other (4xx) | `400`, body `detail \|\| "rejected"` (OTLP exporters drop on non-retryable) |

Errors in 9–11 are logged as `otlp: Axiom <status>[: <detail>]`. No CORS, no other methods. No timeout on the forward (Go: add one, then map to 503).

Trust model (accepted): the relay does not read the body, so `keel.service_id` / `keel.environment_id` in the spans are the **sender's claim**. A holder of one environment's key can tag spans as another environment's service in the same organization and they would show there. The org is the boundary (members can obtain every env's key via `keel run`; the dataset is the org's). If the Go agent later decodes OTLP, it can set `keel.*` from the key instead.

---

## 10. The agent prompt (`agentPrompt`)

`svc = service ?? "<service>"`. The second sentence of the first paragraph (`where`) is:

- with `service`: ``It runs on Keel as the service `<service>`[ in the project `<project>`].`` (project part only when `project` is set)
- without `service`: ``It runs on Keel[ in the project `<project>`]; find which service it is with `keel service list` and use that name wherever this says `<service>`.``

`svc` is substituted in test steps 1 and 3 and in the two final commands. The text ends with a single trailing `\n`. Rendered for `agentPrompt({})` (no service, no project), byte-exact:

~~~~text
# Set up OpenTelemetry tracing for Keel

Instrument this repo with OpenTelemetry so each request it serves becomes a trace in Keel, then prove it works locally before anything ships. It runs on Keel; find which service it is with `keel service list` and use that name wherever this says `<service>`.

Keel provides the exporter configuration through environment variables at run time. Your job is the code, and checking that spans arrive.

## What Keel sets

When tracing is on for a service, and under `keel run`, the process gets:

- `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`
- `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` (the Keel service, the environment, `deployment.environment.name`)
- `OTEL_TRACES_EXPORTER=otlp`, `OTEL_METRICS_EXPORTER=none`, `OTEL_LOGS_EXPORTER=none`

## Rules

1. Use the official OpenTelemetry SDK for this language, with auto-instrumentation for the HTTP server, outgoing HTTP calls, and the database and cache clients this app uses. No vendor SDKs, and no Keel-specific code.
2. Configure the SDK only from those variables. Do not hardcode an endpoint, headers, a service name or an exporter, and do not add a console exporter.
3. Export traces only, over OTLP/HTTP with protobuf. Keel does not take OTLP logs or metrics.
4. Start the SDK only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, so that without it (tests, plain dev scripts) the app runs exactly as before.
5. Initialize it before the app loads anything it instruments: a preload, `--import`, or a wrapper command. Otherwise auto-instrumentation silently patches nothing.
6. On SIGTERM, flush and shut the SDK down, then exit. Keel stops containers with SIGTERM and kills them 10 seconds later, and in a container a SIGTERM handler replaces the default exit, so a handler that only flushes leaves the process hanging.
7. Name each request's server span `METHOD route` (`GET /users/:id`, not `GET` or `GET /users/42`) and set `http.route`. Framework instrumentations do this. Where the app routes by hand (plain `node:http`, `Bun.serve`, a hand-written router), set `http.route` on the active span and rename it where the route is matched. A request that matches no route keeps the bare method: never put the raw path in the name.
8. Keep logging to stdout and stderr, as one JSON object per line that carries the active span's `trace_id` and `span_id` (lowercase hex, as OpenTelemetry formats them). Keel ships stdout and links each line to its trace by that id. Where the logger has an OpenTelemetry integration, use it rather than adding the ids by hand.
9. Add spans by hand only around work no request covers, such as queue consumers, cron jobs and startup tasks. Name spans after the operation, not the data, and never put secrets or personal data in attributes.
10. Keep the new dependencies few, and pin them the way this repo pins everything else.

## Per stack

- **Node.js** (Express, Fastify, Hono, NestJS…): `@opentelemetry/api`, `@opentelemetry/sdk-node`, `@opentelemetry/auto-instrumentations-node`, `@opentelemetry/exporter-trace-otlp-proto`, and `@opentelemetry/instrumentation` for the ESM hook below. Put the setup in its own file and load it with `node --import ./instrumentation.mjs` (ESM) or `--require` (CommonJS), in both the dev script and the Dockerfile `CMD`. In an ESM app that file must also register OpenTelemetry's loader hook (`register("@opentelemetry/instrumentation/hook.mjs", import.meta.url)` from `node:module`) before the SDK starts; without it, modules loaded with `import` are not patched. Turn off the `fs`, `dns` and `net` instrumentations: they add a span per file read and lookup. For logs, use pino or winston: their instrumentations (included in auto-instrumentations) add `trace_id` and `span_id` to each line.
- **Next.js**: the `instrumentation.ts` hook with the same packages, or `registerOTel` from `@vercel/otel` (it reads the same variables).
- **Bun**: the Node SDK covers outgoing calls and many clients, but `Bun.serve` is not auto-instrumented. Wrap the fetch handler in a SERVER span yourself, with `http.request.method`, `url.path`, `http.route` and `http.response.status_code`, and status ERROR on 5xx and on thrown errors. Continue an incoming `traceparent` with the W3C propagator.
- **Python**: `opentelemetry-distro` and `opentelemetry-exporter-otlp-proto-http`, then `opentelemetry-bootstrap -a install` for the libraries present. Run the app under `opentelemetry-instrument` in both the dev command and the Dockerfile. For logs, `opentelemetry-instrumentation-logging` puts `otelTraceID` and `otelSpanID` on each record; write them as JSON.
- **Go**: the `go.opentelemetry.io/otel` SDK with `otlptracehttp` (it reads the `OTEL_EXPORTER_OTLP_*` variables) and `resource.WithFromEnv()`. Wrap the server handler and HTTP clients in `otelhttp`, and use the contrib packages for the database drivers. For logs, a `slog` JSON handler that adds `trace_id` and `span_id` from `trace.SpanContextFromContext(ctx)`.
- **Anything else**: the official SDK and its OTLP/HTTP protobuf trace exporter, configured from the environment.

## Test it locally before shipping

The Keel CLI is `keel`. Check it with `keel whoami`. If it is missing or not logged in, stop and ask me, the human: logging in needs my approval in the dashboard.

1. Run the app the way you would in development, through Keel: `keel run <service> -- <dev command>`, in the background. Prefer the real command (`node --import ./instrumentation.mjs server.mjs`) over a package-manager script, so the process you stop is the app. That adds this service's variables and Keel's tracing variables for a local run (`deployment.environment.name=local`). Anything already set in your shell wins.
2. Make a few requests to it: a page, an API route, one that touches the database or another service if it has one, and one that fails if you can.
3. Run `keel traces <service> --since 15m --json`. Spans are batched for up to 5 seconds, so retry for about 30 seconds. You should see your requests with `"local": true`, route-shaped names (`GET /users/:id`, not `GET`, except for paths no route matches), the right status, and child spans for database and outgoing calls (`"spans"` above 1).
4. Check that a log line written during one of those requests carries the same `trace_id`.
5. If nothing arrives, check that the SDK actually started (the variable was set when it initialized, and the preload was loaded), that the exporter is OTLP/HTTP protobuf, and that the process did not exit before flushing. If `keel traces` fails with `TRACES_OFF`, the Keel organization has no traces store yet: ask me to open Observability in the dashboard and sign in with Axiom. Do not move on until your requests show up.

## Turn it on in Keel

Tracing is a per-service switch, staged like a variable change. Once this change is merged and its image is published:

    keel tracing enable <service>
    keel redeploy <service>

`redeploy` pulls the image again and applies the staged switch, and works for a first deploy too. If you are not the one who deploys, list these two commands in your summary as the last step.
~~~~

For `agentPrompt({ service: "api", project: "shop" })` only these lines differ: line 3 ends with ``It runs on Keel as the service `api` in the project `shop`.``; ``keel run api -- <dev command>``; ``keel traces api --since 15m --json``; ``keel tracing enable api``; ``keel redeploy api``. For `agentPrompt({ project: "shop" })` only line 3 differs: ``It runs on Keel in the project `shop`; find which service it is with `keel service list` and use that name wherever this says `<service>`.``

---

## 11. Worker config and log shipping (per-node agent)

### 11.1 `worker.config` — internal query, served as `GET /worker/config`

HTTP: `Authorization: Bearer <KEEL_WORKER_TOKEN>`; constant-time compare; `KEEL_WORKER_TOKEN` unset, header missing or not `Bearer ` → `401` body `unauthorized`. `200` with `Content-Type: application/json`, `Cache-Control: no-store`. (`install.sh` curls this route to verify the worker token; keep it or change install.sh with it.)

Body:

```json
{ "sinks": [
  { "projectId": "<project id>",
    "serviceIds": ["<node id>", "…"],
    "sink": { "kind": "axiom", "domain": "api.axiom.co", "dataset": "keel-logs",
              "traces": "keel-traces", "token": "<API token>", "org": "Acme" },
    "since": 1759912345678.25 } ] }
```

Algorithm:
1. No `logSinks` row at all → `{ "sinks": [] }`.
2. For every project with `organizationId`, `row = sinkOf(org)` (memoized per org). Projects without org or whose org has no sink are skipped.
3. Entry per remaining project: `serviceIds` = ids of nodes **with `desired` set** (service/database/cache) in any environment of the project; `sink` = the row's full `LogSink` object (token included; worker ignores `traces`/`org`); `since` = the row's `_creationTime` (connect time, float ms).

Per project because the worker routes per project; equal sinks share one queue in the worker.

### 11.2 Agent process (env, loop, shutdown)

| Env var | Default | Meaning |
| --- | --- | --- |
| `KEEL_URL` | required (exit if empty) | Control plane HTTP base (Convex site URL today, `http://<KEEL_ADDR>:3211`). Trailing `/` stripped. |
| `KEEL_WORKER_TOKEN` | else file `/run/secrets/keel_worker_token` (trimmed); neither → fatal | Bearer for `/worker/*`. |
| `KEEL_CONFIG_POLL_MS` | `30000` | Config poll interval. |
| `KEEL_STATE` | `/var/lib/keel-worker/state.json` | Resume-point file (on a per-node volume). |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Read-only socket; every Docker call is a GET. |

Deployment today: Swarm service `keel-worker`, `--mode global --network host`, socket bind-mounted read-only, volume `keel-worker-state:/var/lib/keel-worker`, secret `keel_worker_token`, restart any, stop grace 10s. Nothing listens.

Startup: load state; start state writer; `GET /info` → `nodeId = Swarm.NodeID ?? Name ?? ""`. Then run forever, concurrently: the config loop and the Docker event forwarder (events → `/worker/events`, documented in the workers spec; this area only uses its container hooks, §11.6).

Config loop: `fetchConfig()` = `GET <KEEL_URL>/worker/config` with bearer, 10 s timeout, non-2xx → error `config <status>`; then `applyConfig(cfg.sinks)`; then `reconcileFollowers()`; errors logged (`poll failed: …`). Then wait `KEEL_CONFIG_POLL_MS`, or less when woken early by `refreshConfig()` (§11.6).

Shutdown on SIGTERM/SIGINT: flush all queues and wait for their drains, at most 5 s, then exit 0.

### 11.3 State file

`{ "eventsSince"?: string, "logsSince": { "<full container id>": "<seconds.nanoseconds>" } }`. Loaded at start (unreadable → `{ logsSince: {} }`). Written whole at most once per second when marked dirty. `logsSince[c]` is a **delivery-confirmed** resume point: only advanced after the sink accepted the batch containing the line. Losing the file = re-read every container from its sink's connect time (duplicates, never gaps).

### 11.4 Routing (`applyConfig`) and followers

In-memory:
- `routes: projectId → { sink, serviceIds }`; `sinkByService: serviceId → Sink`; `sinceByService: serviceId → dockerSince(entry.since)` (only when `since` is a number).
- `readSince: containerId → since` (in-process, last line read; avoids re-sending on a re-opened tail).
- `followers: containerId → abort handle`; `finished: Set<containerId>` (read to EOF after exit).
- `queues: sinkKey → { sink, entries, draining, room waiters }`.

`sinkKey(cfg) = "<kind>:<domain>:<dataset>:<last 6 chars of token>"`. `applyConfig` reuses an existing Sink object when its key matches, else builds a new one. After rebuilding the maps, every queue whose key no route uses any more is deleted: its queued lines are **dropped** (log `dropping N queued lines for a removed sink`) and waiters released. So a re-sign-in (new token) drops lines still queued for the old token. Logs `config applied {"sinks":<routes>,"services":<serviceIds>}` when the routing changed.

`dockerSince(ms) = floor(ms/1000) + "." + pad9(min(round((ms % 1000) * 1e6), 999999999))`.

`reconcileFollowers()`:
1. `GET /containers/json?filters={"label":["com.docker.swarm.service.name"],"status":["running","exited"]}`.
2. For each container: `serviceId` = label `com.docker.swarm.service.name` minus `svc-` prefix (non-`svc-` → null). `routed = serviceId && sinkByService.has(serviceId)`; `pending = id in state.logsSince && !finished.has(id)`. If `routed && (State == "running" || pending)` → `start(c)` (exited containers with undelivered lines after a restart are read to EOF); else `stop(id)` (keep resume point).
3. Followers, `logsSince` keys and `finished` entries for containers no longer listed → `stop(id, forget=true)` / removed.

`start(c)`: no-op if already following, or not routed. Otherwise spawn `follow(c)`.

`stop(id, forget)`: abort follower. If `forget`: also drop `finished`, `readSince` and `state.logsSince[id]` (persisted).

`follow(c)` loop until aborted:
- Labels: `service = com.docker.swarm.service.name`, `task = com.docker.swarm.task.id ?? ""`, `replica = Number(com.docker.swarm.task.name.split(".")[1]) || 0` (task name `svc-<id>.<slot>.<taskid>`), `container = Id[:12]`.
- Sink for the service gone → return.
- `since = readSince[id] ?? state.logsSince[id] ?? sinceByService[serviceId]` (in process → delivered before restart → sink connect time → none).
- `GET /containers/<id>/logs?follow=1&stdout=1&stderr=1&timestamps=1` + `&since=<since>` or, with no since, `&tail=0` (new lines only).
- For each parsed line (§11.5): if the service lost its sink → return; wait for room in its queue (or abort); `after = sinceAfter(line.time)` (if stamped) → `readSince[id] = after`; enqueue `{ event, container: id, since: after }` with event (§13):
  `{ _time: line.time || now ISO, message, stream, service_id: serviceId, service, task, replica, node: nodeId, container }`.
- EOF (container exited) → `finished.add(id)`; return. Error → if aborted return, else log `follow <container> failed (<err>), retry in 3s` and retry after 3 s.

### 11.5 Frame and line parsing (`frames.ts`)

- `FrameParser.push(chunk)`: prepend carry; while ≥8 bytes: `type = b[0]`, `len = u32 BE b[4..8]`; `type > 2` → whole remaining buffer is raw stdout text (TTY), carry cleared, return; incomplete frame → stop; else emit `{ stream: type == 2 ? stderr : stdout, text: utf8(payload) }`. Remainder becomes the carry.
- `LineSplitter.push(frame)`: per-stream partial carry; split `partial + text` on `\n`, the last piece becomes the new partial; drop empty pieces; parse each.
- Line parse: strip one trailing `\r`; first token (before the first space) is a stamp iff `length >= 20`, ends with `Z`, and its 5th char is `-` → `{ time: stamp, text: rest after the space }`; else `{ time: "", text: whole line }`.
- `sinceAfter(stamp)`: `/^(.+?)(?:\.(\d{1,9}))?Z$/`; `secs = floor(Date.parse(<prefix>Z)/1000)`; `nanos = Number(frac padded right to 9) + 1` with carry into seconds at 1e9; return `"<secs>.<pad9(nanos)>"` (Docker `since` is inclusive, so +1 ns makes it exclusive). Unparseable → undefined.

### 11.6 Container event hooks

The event forwarder calls `onContainerEvent(Action, id, Actor.Attributes)` for **every** `container`-type event that has an `id` (before its own relevance filter):
- `start`: `serviceId` from attrs (non-`svc-` → ignore). Routed → `start({ Id, Labels: attrs, State: "running" })` (labels on the event equal the container's). Unrouted and last refresh > 5 s ago → `refreshConfig()` (fetch config now; the tail then starts at the sink's connect time, before the container started, so nothing is missed).
- `destroy`: `stop(id, forget=true)`.

### 11.7 Queue, batching, delivery

- Constants: `FLUSH_MS = 1000`, `FLUSH_LINES = 500`, `RETRY_MS = 5000`, `MAX_QUEUE = 20000` (per sink).
- `enqueue`: push; if `entries >= 500` flush now, else arm a single global 1 s flush timer.
- `flush`: clear timer; start `drain(q)` for every non-empty, non-draining queue.
- `drain(q)`: while entries and the queue is still current: take the first 500; `ok = sink.send(events)`; not ok → put the batch back at the front, arm one global 5 s retry timer that calls `flush`, return. Ok → `checkpoint(batch)` (for each entry with `since`: `state.logsSince[container] = since`, later wins; mark state dirty); if `entries < 10000` release all room waiters.
- Backpressure: a follower waits before enqueuing while its queue holds ≥ 20000; Docker's json-file log is the buffer. A slow sink never blocks another sink's queue.

### 11.8 Axiom sink (write side)

- URL: `domain` ends with `.edge.axiom.co` → `<base>/v1/ingest/<urlenc(dataset)>`; else `<base>/v1/datasets/<urlenc(dataset)>/ingest` (`/v1/ingest/…` is 404 on `api.axiom.co`). `<base>` as `baseUrl` (§3.2).
- `send(events)`: body = NDJSON (one `JSON.stringify(event)` per line, joined by `\n`, no trailing newline); headers `authorization: Bearer <token>`, `content-type: application/x-ndjson`; 15 s timeout per attempt. Up to 5 attempts: 2xx → `true`; 4xx other than 429 → log `rejected <status>, dropping <n> events` and `true` (malformed: dropped); else log and sleep `1000 * 2^attempt` ms (1, 2, 4, 8, 16 s, also after the last attempt) → after 5 → log `unreachable, keeping <n> events for a later attempt`, `false`.

Adding a sink kind: a variant in `LogSink`, a `Sink` impl keyed by `sinkKey`, a read provider (`<kind>Tail`, lines/recent), a branch in `logs.tail`/`recent`/`around`, a `connect<Kind>`.

---

## 12. Docker log provider recap (manager side)

Used only by `logs.tail` when the node's organization has no Axiom sink (no shipping happens then). It reads `docker service logs` through the manager's socket, so every call fans out to every node running a task, and it only holds what the nodes' json-file driver kept. Exact calls in §5.1. Volume/group nodes have no Swarm service → 404 → empty tail.

---

## 13. Event shape contract (worker ↔ every read provider)

| Field | Type | Value |
| --- | --- | --- |
| `_time` | string | RFC3339Nano from Docker `timestamps=1` (Axiom indexes on `_time`); `new Date().toISOString()` when the line had no stamp |
| `message` | string | the line, stamp stripped (ANSI escapes kept; the web renders/strips them) |
| `stream` | string | `stdout` \| `stderr` |
| `service_id` | string | node id (the Swarm service `svc-<id>` minus prefix); every log query filters on it |
| `service` | string | Swarm service name `svc-<id>` |
| `task` | string | Swarm task id (one per replica run); the Logs tab tags lines with it |
| `replica` | number | Swarm slot from the task name |
| `node` | string | Swarm node id of the agent |
| `container` | string | 12-char container id |

Field names are the contract: change one side, change both. Trace correlation is read-side only (§17.4): nothing extra is shipped.

---

## 14. External integrations summary

### 14.1 Axiom (control plane)

| Purpose | Call | Auth | Caller |
| --- | --- | --- | --- |
| DCR | `POST <AUTH>/oauth2/register` (JSON) | none | `beginAxiomSignIn` |
| Authorize (browser redirect) | `GET <AUTH>/oauth2/authorize?…` | — | browser |
| Token exchange | `POST <AUTH>/oauth2/token` (form) | PKCE | `signInAxiom` |
| List orgs | `GET <API>/v2/orgs` | user token | `signInAxiom` |
| List datasets | `GET <org.domain>/v2/datasets` | user token + `x-axiom-org-id` | `provision` |
| Create dataset | `POST <domain>/v2/datasets {name, description}` | user token + org header (`provision`), or API token (`connectAxiom`) | |
| Mint scoped token | `POST <org.domain>/v2/tokens` | user token + org header | `provision` |
| APL query | `POST <domain>/v1/datasets/_apl?format=tabular` | API token | verify, all reads |
| OTLP traces | `POST <domain>/v1/traces` + `x-axiom-dataset` | API token | relay |

### 14.2 Axiom (agent)

`POST <domain>/v1/datasets/<dataset>/ingest` (or `/v1/ingest/<dataset>` on `*.edge.axiom.co`), NDJSON, API token.

### 14.3 Docker Engine API

| Where | Call |
| --- | --- |
| manager (logs.tail) | `GET /services/svc-<id>/logs?stdout&stderr&tail=N&timestamps&details`; `GET /tasks?filters={"service":["svc-<id>"]}` |
| agent | `GET /info`; `GET /containers/json?filters={"label":["com.docker.swarm.service.name"],"status":["running","exited"]}`; `GET /containers/<id>/logs?follow=1&stdout=1&stderr=1&timestamps=1&(since=S|tail=0)`; `GET /events?filters={"type":["container","service","node"]}&since=S` |

Labels relied on: `com.docker.swarm.service.name` (`svc-<nodeId>`), `com.docker.swarm.task.id`, `com.docker.swarm.task.name` (`svc-<id>.<slot>.<task>`). Details keys in service logs: `com.docker.swarm.task.id=…`.

---

## 15. Environment variables (control plane)

| Var | Default | Meaning |
| --- | --- | --- |
| `KEEL_OTLP_URL` | unset | Full relay base injected as `OTEL_EXPORTER_OTLP_ENDPOINT` (must include `/otlp`). Dev sets `http://<tailnet ip>:3211/otlp` because the dev site URL is loopback. |
| `CONVEX_SITE_URL` | Convex-provided | Public HTTP-actions origin; endpoint fallback `<it>/otlp`. On an install `http://<KEEL_ADDR>:3211`, reachable from containers over the host's tailnet route. Go: the control plane's public base URL. |
| `KEEL_ALLOW_LOCAL_SINKS` | unset | `"1"` allows a full-origin sink `domain` in `connectAxiom` and enables the two overrides below. Mock/testing only. |
| `KEEL_AXIOM_AUTH_URL` | `https://authorization.axiom.co` | OAuth server origin (only with the flag). |
| `KEEL_AXIOM_API_URL` | `api.axiom.co` | API origin for `/v2/orgs` and every org's `domain` (only with the flag). |
| `KEEL_WORKER_TOKEN` | required for `/worker/*` | Bearer shared with the agent. |

Agent vars in §11.2.

---

## 16. Error strings (exact) and CLI mapping

| Message | Raised by |
| --- | --- |
| `Node not found` | `logs.tail`; `tracing.enable`/`localEnv`/`scope`/`setEnabled` (`requireNode`); `traces.overview` (nodeId not in env) |
| `Environment not found` | `logs.recent`, `logs.around`, `traces.overview/get/around` |
| `Connect Axiom to search all logs` | `logs.recent`, `logs.around` |
| `Connect Axiom to see traces` | `traces.overview/get/around`; `tracing.enable(on)`; `localEnv.reason` (not an error there) |
| `Sign in with Axiom again to turn on traces` | `traces.overview`; `tracing.enable(on)`; `localEnv.reason` |
| `Not a trace id` | `traces.get` |
| `Only services can be traced` | `tracing.enable`, `tracing.localEnv` |
| `You're not in an organization yet. Ask a member for an invite link.` | `connectAxiom`, `disconnect`, `beginAxiomSignIn`, `signInAxiom`, `chooseAxiomOrg`, internal `save`/`startSignIn`/`stashPending` |
| `Region must be US or EU` | `connectAxiom` |
| `Dataset: letters, digits, - _ . only` | `connectAxiom` |
| `That does not look like an Axiom API token` | `connectAxiom` |
| `Bad redirect URI` | `beginAxiomSignIn` |
| `Axiom refused to register Keel: <msg>` | `beginAxiomSignIn` |
| `Axiom sign-in expired, try again` | `signInAxiom` |
| `Axiom sign-in failed: <msg>` | `signInAxiom` |
| `<Axiom …> (Axiom API rejected the sign-in token, aud <aud>)` | `signInAxiom` |
| `This Axiom account has no organization` | `signInAxiom` |
| `Sign-in expired, sign in with Axiom again` | `chooseAxiomOrg` |
| `Organization not found` | `chooseAxiomOrg` |
| `Listing datasets: …`, `Creating <name>: …`, cap message (§4.7), `Minting the ingest token: …`, `Axiom did not return a token`, `Querying <dataset>: …` | `provision` |
| `Axiom <status>` / `Axiom <status>: <≤200 chars>` | any Axiom call, surfaced via the wrappers above |

CLI (`apps/cli/internal/keel/api.go translate`, exact match unless noted): `Node not found` → `SERVICE_NOT_FOUND`; `Connect Axiom to see traces` and `Sign in with Axiom again to turn on traces` → `TRACES_OFF`; `Environment not found` → `PROJECT_NOT_FOUND`; prefix `You're not in an organization` → `NO_ORGANIZATION`; any other non-empty ConvexError → `INVALID_INPUT`; non-ConvexError → `SERVER_ERROR`. Rewording any of these needs the CLI updated. Argument validation failures (bad id, bad range literal) are not ConvexErrors today (→ `SERVER_ERROR`).

---

## 17. Web and CLI consumers

### 17.1 Web: who calls what

| Component | Function | Kind | Args | Cadence |
| --- | --- | --- | --- | --- |
| `observability/page.tsx` | `logSinks.get` | reactive query | `{}` | subscribed; `undefined` → spinner; null/non-axiom → gate; else `Explorer` |
| `observability/axiom-gate.tsx` (`AxiomSignIn`, `TracesBanner`) | `logSinks.pendingOrgs` | reactive query | `{}` | subscribed; non-null → org picker |
| ″ `useSignIn` | `logSinks.beginAxiomSignIn` | action | `{ redirectUri: location.origin + "/axiom/callback" }` | on click; then `sessionStorage["keel.axiom.return"] = {"slug": <project slug>}` and `location.assign(url)` |
| ″ picker | `logSinks.chooseAxiomOrg` | action | `{ orgId }` | on click; toast `Every project's logs and traces now go to Axiom · <org> · <dataset>` |
| ″ picker | `logSinks.cancelAxiomSignIn` | mutation | `{}` | on Cancel |
| `routes/_auth/axiom/callback.tsx` | `logSinks.signInAxiom` | action | `{ state, code }` from the query string | once (ref-guarded: state is single-use). `error`/missing code → toast `Axiom: <error_description \|\| error \|\| "no code returned">`, no call. Then navigate to `/p/<slug>?view=observability` (from sessionStorage, removed on read) or `/` |
| `settings.tsx` | `logSinks.get`, `organizations.current` | reactive queries | `{}` | subscribed |
| ″ Disconnect dialog | `logSinks.disconnect` | mutation | `{}` | on confirm |
| `observability/explorer.tsx` `useStream` | `logs.recent` | action | `{ environmentId, range, search, tail: 300 }` | poll: both calls together (`allSettled`), next run 10 s **after both settle**; only while the overview is visible; restarts on range/search change (search trimmed, debounced 300 ms; default range `1h`) |
| ″ | `traces.overview` | action | `{ environmentId, range, search }` | same poll, only when `sink.traces` is non-null |
| `observability/trace/trace-detail.tsx` | `traces.get` | action | `{ environmentId, traceId, at? }` | once per (traceId, at); `at` = root start (from a request) or line time (from a line); absent for a pasted `&trace=` link |
| `observability/log-context.tsx` | `logs.around`, `traces.around` | actions | `{ environmentId, at }` | once per `at` (`&around=<ms>`) |
| `observability/chrome.tsx` `useServices` | `nodes.list` | reactive query | `{ environmentId }` | subscribed (service names/tones; other spec) |
| `copy-prompt.tsx` (lamp empty state, NoRequests card, Tracing section) | `tracing.prompt` | reactive query | `{ nodeId? , environmentId? }` | subscribed |
| `bottom-panel/tabs/tracing.tsx` | `tracing.forNode` | reactive query | `{ nodeId }` | subscribed; switch disabled when `traces != "on" && !enabled` |
| ″ | `tracing.enable` | action | `{ nodeId, on: !enabled }` | on toggle |
| `bottom-panel/tabs/logs.tsx` | `logs.tail` | action | `{ nodeId, tail: 300 }` | `setInterval` every 3 s (does not wait for the previous call) while the node is not a volume and its status is not `pending`; shows `· via Axiom` when `source == "axiom"`; replica tags `r<slot>` from `replicas` |
| `bottom-panel/log-stream.tsx` | none | — | presentational (mono, ANSI rendered client-side) | — |

URL state on `/p/<slug>`: `?view=observability` (old `logs`/`traces` values land there), `&trace=<id>`, `&around=<epoch ms>`; `?view=settings`.

### 17.2 Web expectations on shapes

- `REQUESTS = 100` in `explorer.tsx` must equal the backend's `LIST` (a full list triggers the "Showing the latest events, back to …" cut-off logic); `LINES = 300`.
- `StatRow` computes rate per minute as `requests / ((to - from) / 60000)`.
- Charts read `buckets[].{time, requests, errors, p50, p95, p99}` and `bucketMs`.
- The lamp empty state needs `stats.requests == 0` and zero lines with an empty search; a failed load with nothing to show uses the error string verbatim.

### 17.3 CLI

| Command | Function | Args |
| --- | --- | --- |
| `keel logs <svc> [-n N] [-f]` | `logs:tail` | `{ nodeId, tail: N }` (1–1000, default 100); `-f` polls every 2 s with `tail: 200` and de-dupes client-side |
| `keel traces [svc] [--since r] [--search s]` | `traces:overview` | `{ environmentId, range, nodeId?, search? }`; reads `stats.*` and `traces[]` (`duration` → `durationMs`) |
| `keel tracing [svc]` | `tracing:forNode` | `{ nodeId }` (`traces` → `store`) |
| `keel tracing enable\|disable <svc>` | `tracing:enable` | `{ nodeId, on }` |
| `keel run <svc> -- cmd` | `tracing:localEnv` | `{ nodeId }`; endpoint `<ConvexSiteURL>/otlp` |
| `keel tracing prompt [svc]` | `tracing:prompt` | `{ nodeId? , environmentId? }` |

### 17.4 Log ↔ trace correlation (web only, `correlate.ts`)

Done on the read side on the line with ANSI stripped:
- `TRACE_KEY = (?<![\w.-])(?:trace[_.-]?id|otelTraceID)["']?\s*[:=]\s*["']?([0-9a-f]{32})(?![0-9a-f])` (case-insensitive), `SPAN_KEY` likewise with `span[_.-]?id|otelSpanID` and 16 hex, `TRACEPARENT = \b00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}\b`. All-zero ids ignored; ids lowercased.
- A trace's lines are the environment's lines containing the id (`traces.get` searches `message contains "<id>"`), so the backend needs nothing else. A line hangs under the span it names, else under the first root.

---

## 18. Realtime: reactive reads and invalidation keys

Only these reads in this area are subscriptions; everything else (`logs.*`, `traces.*`) is a request/response the client polls.

| Reactive read | Depends on | Subscribe to |
| --- | --- | --- |
| `logSinks.get` | caller's org sink row (incl. legacy rows) | `org:<orgId>` |
| `logSinks.pendingOrgs` | caller's org `axiomPending` row | `org:<orgId>` |
| `tracing.forNode(nodeId)` | node (type, name, desired.tracing), environment (name), env's `otlpKeys` row, node's variable keys, org sink (traces state) | `node:<id>`, `env:<envId>`, `org:<orgId>` |
| `tracing.prompt({nodeId?, environmentId?})` | node (type, name), project (slug) | `node:<id>` and/or `project:<projectId>` |

| Write | Invalidate |
| --- | --- |
| `logSinks.save` (via `connectAxiom`, `provision`), `logSinks.disconnect` | `org:<orgId>` (also makes `tracing.forNode` for every service of the org recompute `traces`) |
| `stashPending`, `takePending`, `cancelAxiomSignIn`, `dropPending` | `org:<orgId>` |
| `startSignIn`, `takeSignIn`, `dropSignIn`, `saveClient` | nothing (no reactive reader) |
| `otlp.saveKey` (insert) | `env:<envId>` |
| `tracing.setEnabled` | `node:<id>`, `env:<envId>` (dirty count on the canvas) |
| variable set/remove, node rename (other specs) | `node:<id>` (already required there) |
| project slug change (other spec) | `project:<id>` |

The worker config is polled (30 s, plus an early fetch on an unrouted `svc-*` container start); no push needed.

---

## 19. Subtle behaviour, races, idempotency

1. **Connect time is a row's creation.** Any sink change creates a new row; the worker treats it as a new start point for containers without a resume point. Containers with a persisted `logsSince` keep their own point.
2. **Token change drops in-flight lines.** A new token → new `sinkKey` → the old queue is deleted at the next config poll; queued lines go (logged). Resume points of delivered lines stand; undelivered ones re-read from Docker only after a restart (in-process `readSince` already passed them). Accepted.
3. **Concurrent sign-ins per org**: `startSignIn` deletes the previous row, so only the last tab's callback succeeds; others get `Axiom sign-in expired, try again`. `takeSignIn` is single use; a replayed callback fails the same way.
4. **Pending pick is single use**: `chooseAxiomOrg` consumes the pending row even when the org id is invalid.
5. **DCR idempotency**: two first-time sign-ins on the same URI may both register; `saveClient` keeps the first id; both sign-ins still work with their own client ids.
6. **Key creation race**: `saveKey` returns the existing key if one appeared; `ensureKey` returns what `saveKey` returns.
7. **Tracing on without a key** cannot ship tracing vars: `withTracing` skips if the key is missing (only possible if the row was removed by hand).
8. **Disconnect leaves tracing on**: services keep sending; the relay answers 200 and drops. `tracing.forNode` shows `traces: "off"`; the switch can still be turned off.
9. **Pre-traces sink** (`traces` unset): logs work; `traces.overview` → `NO_TRACES`; `traces.get` returns lines only; `traces.around` → `[]`; relay drops spans with 200; `tracing.enable(on)` → `NO_TRACES`.
10. **Empty environment**: no services → no APL queries; overview returns zero stats and `count` empty buckets; `axiomLines` → `[]`.
11. **"invalid field" means empty** for every read query on both datasets.
12. **Scoping**: logs are scoped by `service_id in (env services)`; requests by `resource.custom["keel.service_id"] in (…)`; a trace opened by id is not scoped (spans), but its lines are.
13. **`axiomTail` vs `axiomLines` time precision** differ (integer ms vs sub-ms); keep both.
14. **`forEnvironment.serviceIds` vs `worker.config.serviceIds`** differ (type filter vs `desired` filter); keep both.
15. **Search truncation**: `logs.recent` and `traces.overview` cut `search` to 200 chars *before* trimming; `traces.get` uses the trace id as the search.
16. **Legacy rows** stand in for the org sink until a sign-in or disconnect; see §1.8.
17. **Orphaned `otlpKeys`** after environment/project deletion → 401 at the relay.

---

## 20. Go port compatibility hazards

1. **Baked endpoints and keys.** Every service shipped with tracing on carries `OTEL_EXPORTER_OTLP_ENDPOINT=<CONVEX_SITE_URL or KEEL_OTLP_URL>/otlp` and `Authorization=Bearer%20keel_otlp_…` in its Swarm spec until it is re-shipped. The Go control plane must serve `POST /otlp/v1/traces` at the same origin (today `http://<KEEL_ADDR>:3211`) and import `otlpKeys` verbatim, or traced services silently lose spans.
2. **Agent URL.** `keel-worker` tasks have `KEEL_URL=http://<addr>:3211`; `/worker/config` (and `/worker/events`) must stay there with the same bearer until the agent is replaced. `install.sh` also probes `/worker/config`.
3. **Sink rows**: import `logSinks` with `connected_at = _creationTime`; otherwise every node re-reads every container from the import time (duplicates) or skips lines.
4. **Agent state file** (`state.json`) format must be read by the Go agent on first start (`logsSince` keyed by full container id, `"<s>.<9 digit ns>"`), or log shipping duplicates from the connect time.
5. **Error strings** in §16 are an API contract for the CLI and the web.
6. **JSON numbers**: times and durations must stay JSON numbers (fractional allowed); `Attribute` stays a `{ key, value }` object; `httpStatus`/percentiles use `null`, not omission; `traces`/`org` in `logSinks.get` use `null`.
7. **No timeouts today** on control-plane Axiom calls (only Convex's action limit). Adding them is fine; map relay timeouts to `503`.
