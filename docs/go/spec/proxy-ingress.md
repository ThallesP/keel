# Porting spec: public ingress and keel-proxy

Status: normative for the Go rewrite. Describes the behaviour of the TypeScript/Convex code at
`36c2ded` (branch `go-single-binary`) closely enough to reimplement it without reading the TS.
Where the current code has a quirk, section 13 says so and recommends what the Go version should do.

Sources (all paths relative to the repo root):

| Area | Files |
| --- | --- |
| Endpoint data + helpers | `packages/backend/convex/schema.ts` (`endpoint*`), `packages/backend/convex/endpoints.ts` |
| Public functions | `packages/backend/convex/nodes.ts` (`expose`, `unexpose`, `publicAddress`, side effects in `remove`, `duplicate`), `nodeHelpers.ts` (`view`) |
| Proxy sync | `packages/backend/convex/proxy.ts` (action, "use node"), `proxyInternal.ts` |
| Port following | `packages/backend/convex/nodesInternal.ts` (`followPort`), call site in `swarm.ts` (`apply`) |
| Upgrades | `packages/backend/convex/migrations.ts`, `swarm.ts` (`removeLegacyTunnels`), `deploy/functions-entrypoint.sh` |
| Cron | `packages/backend/convex/crons.ts` |
| HTTP route | `packages/backend/convex/http.ts` (`POST /proxy/events`) |
| Caddy plugin + image | `apps/proxy/{keel.go,hostns.go,hostns_test.go,Dockerfile,caddy.json,go.mod}` |
| Deployment | `deploy/compose.yml` (`proxy` service), `scripts/bootstrap-swarm.sh` (overlay), `install.sh` (public IP detection) |
| Design doc | `docs/networking.md` |

---

## 1. Concepts and invariants

- **Endpoint** = one way in from the internet to one node (service, database or cache). Stored as an
  element of the array `nodes.endpoints`. Three protocols:
  - `http`: `https://<domain>` on the control plane's TCP 80/443 → reverse proxy to `svc-<nodeId>:<port>`.
  - `tcp`: `<KEEL_PUBLIC_IP>:<publicPort>` (TCP) → raw TCP proxy to `svc-<nodeId>:<port>`.
  - `udp`: `<KEEL_PUBLIC_IP>:<publicPort>` (UDP) → raw UDP proxy to `udp/svc-<nodeId>:<port>`.
- **keel-proxy**: Caddy 2.11.7 + caddy-l4 v0.1.2 + Keel's plugin, a single container on the
  control plane. It is the only public listener of the whole install. Workers never listen
  publicly. Swarm services never publish ports (no `EndpointSpec`).
- **Convex owns the whole proxy config.** `proxy.sync` rebuilds the entire Caddy `apps` object
  from every endpoint in the database and loads it atomically. Nothing is incremental; the proxy
  config is never edited by hand.
- **Expose / unexpose are immediate**, not gated by Ship (unlike image/port/variables). Writing
  an endpoint schedules a sync in the same transaction.
- **Upstreams are names, not addresses**: `svc-<nodeId>:<port>` is resolved by Docker's embedded
  DNS on the `keel` overlay at connection time. Redeploys, rescheduling and replica changes never
  touch the proxy. A stopped service answers 502 (http) or a closed connection (tcp/udp); the
  endpoint stays `live`.
- **Never a wildcard bind.** `tailscale serve` holds 443 on the tailnet address (dashboard over
  HTTPS), so `0.0.0.0:443` cannot bind. The proxy binds each non-tailnet, non-Docker, non-loopback
  host address explicitly (section 7.4).
- **The user's only networking job**: let TCP 80, TCP 443 and each exposed tcp/udp public port
  reach the control plane.

---

## 2. Data model

### 2.1 `nodes.endpoints` (array, optional)

Top-level field on the `nodes` table (not inside `desired`, because Ship bumps `desired` and
observe replaces `observed` wholesale). Absent/`undefined` when the node has no endpoint; the code
never stores an empty array (unexpose writes `undefined` when the last one goes).

`Endpoint` object:

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `protocol` | `"http" \| "tcp" \| "udp"` | yes | Kind of endpoint. |
| `port` | number (integer 1–65535) | yes | Container port the proxy dials on `svc-<nodeId>`. |
| `pinnedPort` | boolean | no | `true` when Expose was given a container port different from `node.desired.port` at that time. When absent/false, `port` follows the node's port each time a port change ships (`followPort`). Never sent to clients. |
| `domain` | string | http only | Lower-case hostname. Unique across the whole install (all orgs, projects, environments). Absent for tcp/udp. |
| `publicPort` | number (integer 1–65535) | tcp/udp only | Port on the control plane. Unique per protocol across the whole install. A tcp endpoint can never use 80 or 443; a udp endpoint can. Absent for http. |
| `status` | `EndpointStatus` | yes | Written only by the proxy code paths (section 8). |

`EndpointStatus` object:

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `state` | `"starting" \| "live" \| "failed"` | yes | `starting`: not loaded yet, or (http) loaded and waiting for its certificate. `live`: serving. `failed`: see `error`. |
| `error` | string | no | Human-readable, already rewritten into the next step for the user (cert hints, port-in-use text). Present only with `failed`. |
| `at` | number (ms since epoch) | yes | When this status was set. Not compared when deciding whether a status changed. |

Go suggestion: store endpoints either as a JSON column on `nodes` (closest to current semantics:
whole-array rewrites) or as a child table `node_endpoints(node_id, idx, protocol, port, pinned_port,
domain, public_port, state, error, at)` with unique indexes `(domain) WHERE protocol='http'` and
`(protocol, public_port) WHERE protocol IN ('tcp','udp')`. The TS has **no indexes** for these: it
does full scans of `nodes` (section 13, Q7). If you add the unique indexes, keep the exact
user-facing error messages of section 4.1 by checking before inserting.

### 2.2 Legacy fields (Quick Tunnel era, removed 2026-10-06)

`nodes.public: any (optional)` and `nodes.ingress: any (optional)`. Only `migrations.run` reads
them (truthiness of `public`), and it always clears both. The Go schema should not have them; the
Convex→Go data import must apply the conversion of section 5.7 step 1 instead (or refuse to import
an install that still has them).

### 2.3 Other node fields this area reads

| Field | Used by | How |
| --- | --- | --- |
| `_id` | `defaultDomain`, `movedDefaultDomain`, upstream name | `svc-<_id>`; FNV hash input. See 13/Q1 about ID format changes. |
| `name` | `defaultDomain`, error messages | `[a-z0-9-]{1,40}` (validated elsewhere). |
| `type` | expose | Only `service`, `database`, `cache`. Default protocol `http` for `service`, `tcp` otherwise. |
| `desired` | expose, followPort | Must exist. `desired.port` default container port; `desired.image` → `engineOf` (Redis guard); `desired.revision` (Redis guard). |
| `dirty` | expose (Redis guard) | Must be falsy for Redis. |
| `deployedRevision` | expose (Redis guard) | Must equal `desired.revision` for Redis. |
| `environmentId` | realtime | Invalidation key of the node's canvas (section 10). |

### 2.4 `variables` table (read only here)

Expose reads `variables` rows `by_node` (index on `nodeId`) to check that a row with
`key === "REDIS_PASSWORD"` exists (Redis guard). `migrations.run` inserts that row (section 5.7).

### 2.5 Constants

| Name | Value | Meaning |
| --- | --- | --- |
| `MAX_ENDPOINTS` | `10` | Per node (counting endpoints other than the one being replaced). |
| `HTTP_PORTS` | `{80, 443}` | tcp endpoints cannot take them. |
| `FIRST_SPARE_PORT` | `20000` | First fallback public port. |
| `ZEROSSL_CA` | `"https://acme.zerossl.com/v2/DV90"` | Second ACME issuer when an email is set. |
| Proxy admin socket | `KEEL_PROXY_SOCKET` or `"/run/keel-proxy/admin.sock"` | Unix socket, mode 0600. |
| Admin request timeout | 30 s (socket idle timeout) | Then error `keel-proxy did not answer within 30s`. |
| Sync passes | max 3 | See 5.1. |
| Resync cron | every 2 minutes | See 5.4. |
| Status error max length | 300 chars (proxy/admin errors), 200 chars (cert hint detail) | JS `slice`, UTF-16 units; use rune-safe truncation in Go. |
| Upstream name | `svc-<nodeId>` | Swarm service name on the `keel` overlay (created by `swarm.apply`). |
| Overlay | `keel` | `docker network create -d overlay --attachable --opt com.docker.network.driver.mtu=1200 keel` (bootstrap-swarm.sh). |
| Host netns file in container | `/run/hostns/net` | Bind mount of `/proc/1/ns/net` (compose). Constant `HostNetNS` in hostns.go. |

---

## 3. Helpers (exact algorithms)

### 3.1 `shortHash(s)` — 6 base-36 chars of 32-bit FNV-1a

```ts
function shortHash(s: string) {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193);
  return (h >>> 0).toString(36).padStart(6, "0").slice(-6);
}
```

Go equivalent (IDs are ASCII so bytes == UTF-16 code units):

```go
func shortHash(s string) string {
	h := uint32(0x811c9dc5)
	for i := 0; i < len(s); i++ { h ^= uint32(s[i]); h *= 0x01000193 }
	b := strconv.FormatUint(uint64(h), 36) // lower-case 0-9a-z, same as JS
	for len(b) < 6 { b = "0" + b }
	return b[len(b)-6:] // last 6
}
```

Test vectors (computed with the TS code):

| input | uint32 | base36 | shortHash |
| --- | --- | --- | --- |
| `""` | 2166136261 | `ztntfp` | `ztntfp` |
| `"a"` | 3826002220 | `1r9wi7g` | `r9wi7g` |
| `"j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4"` | 72042100 | `16w41g` | `16w41g` |
| `"k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj"` | 3279870680 | `1i8r0ew` | `i8r0ew` |
| `"jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y"` | 4097315020 | `1vrfoi4` | `vrfoi4` |

Note: base-36 of a uint32 has 1–7 digits; the result keeps the **last** 6 (drops the leading digit
when there are 7) and left-pads with `0` when shorter.

### 3.2 `defaultDomain(node, ip)`

```ts
`${node.name}-${shortHash(node._id)}.${ip.replaceAll(".", "-")}.sslip.io`
```

Example: node `api`, id `j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4`, ip `203.0.113.7` →
`api-16w41g.203-0-113-7.sslip.io`. sslip.io resolves the name to the IP embedded in it, so HTTPS
works with no DNS setup. The hash keeps two projects' `api` apart (Convex ids share their tail, hence
a hash rather than an id suffix). Renaming a node does **not** change an existing endpoint's
domain (the domain is stored, never recomputed). Only IPv4 is supported (`KEEL_PUBLIC_IP`).

### 3.3 `movedDefaultDomain(node, domain, ip)` → string | undefined

```ts
const m = /^(.+-([0-9a-z]{6}))\.(\d+-\d+-\d+-\d+)\.sslip\.io$/.exec(domain);
const dashed = ip.replaceAll(".", "-");
if (!m || m[2] !== shortHash(node._id) || m[3] === dashed) return undefined;
return `${m[1]}.${dashed}.sslip.io`;
```

A default-pattern domain of **this** node (hash matches its id) on a **different** IP → the same
`<name>-<hash>` label on the current IP. Custom domains, other nodes' patterns and already-current
domains → `undefined`. Note the greedy `.+` before `-`: `m[2]` is the last 6 chars before the first
`.`.

### 3.4 `validDomain(raw)` → normalized domain or error

```ts
const LABEL_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const domain = raw.trim().toLowerCase().replace(/\.$/, "");   // one trailing dot removed
const labels = domain.split(".");
if (domain.length > 253 || labels.length < 2 || !labels.every((l) => LABEL_RE.test(l)))
  throw new ConvexError("Domain must look like app.example.com");
return domain;
```

Accepts IDN in punycode (`xn--…`), `app.localhost` (dev: Caddy internal CA), and also dotted IPs
like `203.0.113.7` (all-digit labels; see 13/Q9). Rejects wildcards, single labels (`localhost`),
underscores.

### 3.5 `endpointKey(e)` — identity of an endpoint across the install

```ts
e.protocol === "http" ? `http:${e.domain}` : `${e.protocol}:${e.publicPort}`
```

Examples: `http:api-16w41g.203-0-113-7.sslip.io`, `tcp:5432`, `udp:27015`. Numbers render as JS
does (`5432`, never `5432.0`).

### 3.6 `endpointAddress(e, ip = KEEL_PUBLIC_IP)`

- http: `https://<domain>`
- tcp/udp: `<ip>:<publicPort>`, or `<public IP>:<publicPort>` (literal text `<public IP>`) when
  `KEEL_PUBLIC_IP` is unset.

### 3.7 `endpointView(e)` — the shape clients see (web canvas and CLI)

```json
{
  "protocol": "http" | "tcp" | "udp",
  "port": 8080,
  "domain": "api-16w41g.203-0-113-7.sslip.io",   // omitted for tcp/udp
  "publicPort": 5432,                             // omitted for http
  "address": "https://api-16w41g.203-0-113-7.sslip.io",
  "state": "starting" | "live" | "failed",
  "error": "..."                                  // omitted unless present
}
```

`pinnedPort` and `status.at` are not exposed. Field names are a client contract
(`apps/web/src/components/canvas/types.ts` `Endpoint` mirrors it).

The node view (`nodeHelpers.view`, returned by `nodes.list`) adds, per node:

| Field | Value |
| --- | --- |
| `public` | `true` iff the node has at least one endpoint |
| `publicUrl` | `endpointAddress(first http endpoint in array order)`, omitted when none (the CLI prints it as `publicUrl`) |
| `endpoints` | `endpoints.map(endpointView)`, `[]` when none |

### 3.8 `allocatePublicPort(port, taken)`

```ts
if (!taken.has(port)) return port;
for (let p = 20000; p <= 65535; p++) if (!taken.has(p)) return p;
throw new ConvexError("No free public port left");
```

`taken` = every existing `publicPort` of the **same protocol** across the install (other nodes and
this node), plus `{80, 443}` when the protocol is `tcp`. Only Keel's own endpoints count: a port
held by some other host process (sshd on 22, a host Postgres) is "free" here and fails later at
bind time (section 5.1, failure attribution).

### 3.9 `certHint(error, ip = KEEL_PUBLIC_IP)` — Caddy ACME error → next step

```ts
if (!error) return "Could not get a certificate";
const acme = /urn:ietf:params:acme:error:(\w+) - (.+)$/.exec(error);
const detail = (acme?.[2] ?? error)
  .replace(/Fetching \S+: /, "")                 // first occurrence only
  .replace(/\s*\(ca=[^)]*\)\s*$/, "")
  .replace(/\s*\(likely firewall problem\)/, "")  // first occurrence only
  .slice(0, 200);
switch (acme?.[1]) {
  case "connection": case "unauthorized": case "tls":
    return `Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (${detail})`;
  case "dns":
    return `${detail}. Point the domain at ${ip ?? "the control plane's public IP"} with an A record.`;
  default:
    return `Could not get a certificate: ${detail}`;
}
```

The input has no newlines (the plugin collapses whitespace), so `$` is end of string. Examples:

| Caddy error (abridged) | Result |
| --- | --- |
| `… authorization failed: HTTP 400 urn:ietf:params:acme:error:connection - 203.0.113.7: Fetching http://api-16w41g.203-0-113-7.sslip.io/.well-known/acme-challenge/tok: Timeout during connect (likely firewall problem) (ca=https://acme-staging-v02.api.letsencrypt.org/directory)` | `Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (203.0.113.7: Timeout during connect)` |
| `HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: NXDOMAIN looking up A for app.example.com - check that a DNS record exists for this domain (ca=https://acme-v02.api.letsencrypt.org/directory)` | `DNS problem: NXDOMAIN looking up A for app.example.com - check that a DNS record exists for this domain. Point the domain at 203.0.113.7 with an A record.` |
| `HTTP 429 urn:ietf:params:acme:error:rateLimited - too many certificates (50) already issued for "sslip.io" in the last 168h0m0s` | `Could not get a certificate: too many certificates (50) already issued for "sslip.io" in the last 168h0m0s` |
| `something else entirely` | `Could not get a certificate: something else entirely` |
| (none) | `Could not get a certificate` |

### 3.10 Shared helpers used here (defined elsewhere, same semantics)

- `validPort(p)`: `undefined` passes through; otherwise must be an integer in 1..65535 else
  `ConvexError("Port must be 1–65535")` (en dash U+2013).
- `engineOf(image)`: `image.split("@")[0].split("/").pop().split(":")[0]`, returned if it is one of
  `postgres|mysql|mongo|redis`, else undefined. Applies to the image of **any** node type.
- `requirePublicIp()`: `KEEL_PUBLIC_IP` (empty string = unset) or
  `ConvexError("Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP")`.
- `requireNode(id)`: the node if the caller's organization owns it (node → environment → project →
  `project.organizationId === membership.organizationId`), else `ConvexError("Node not found")`.
  **Signed-out callers also get `Node not found`**, not `Not authenticated` (ownedProject returns
  null without a membership).
- `requireUser()`: signed-in user or `ConvexError("Not authenticated")` (no org check).

---

## 4. Public functions

All three are called by the web (`apps/web/src/components/canvas/actions.tsx`,
`bottom-panel/tabs/networking.tsx`, `nodes/node-toolbar.tsx`). The CLI does not call them yet
(`keel expose` is on its roadmap with "the same options as the dashboard"); it reads endpoints
through `nodes:list` (`publicUrl`).

The CLI maps ConvexError messages (`apps/cli/internal/keel/api.go` `translate`): `Node not found` →
`SERVICE_NOT_FOUND`, `Not authenticated` → not-authenticated; every other message in this file →
`INVALID_INPUT` with the message as text. Keep every string byte-identical.

### 4.1 `nodes.expose` — mutation, public

Args:

| Arg | Type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | node id | yes | |
| `protocol` | `"http" \| "tcp" \| "udp"` | no | Default: `http` for `type === "service"`, `tcp` for database/cache. |
| `port` | number | no | Container port the proxy dials. Default: `node.desired.port`. |
| `domain` | string | no | http only: custom hostname. Default: `defaultDomain(node, KEEL_PUBLIC_IP)`. |
| `publicPort` | number | no | tcp/udp only. Default: container port if free on that protocol, else first free from 20000. |

Returns: `endpointView(endpoint)` (section 3.7) of the endpoint that now exists (new, replaced,
or the already-existing one).

Algorithm, in this exact order (first failing check wins):

1. `node = requireNode(id)` → `Node not found`.
2. If `!node.desired` or `node.type ∉ {service, database, cache}` →
   `Only services, databases and caches can be exposed`.
3. **Redis guard**: if `engineOf(node.desired.image) === "redis"` (any node type, so also a
   `service` running `redis:7`): load the node's variables; if none has `key === "REDIS_PASSWORD"`,
   **or** `node.dirty` is truthy, **or** `node.deployedRevision !== node.desired.revision`
   (undefined counts as different) → `Ship this Redis first: its password takes effect on the next Ship`.
   Rationale: Redis runs with `--requirepass` only once a Ship with the password converged; until
   then exposing would publish an unauthenticated Redis.
4. `protocol = args.protocol ?? (node.type === "service" ? "http" : "tcp")`.
5. `port = validPort(args.port ?? node.desired.port)` (→ `Port must be 1–65535`); if undefined →
   `Set the service's port first`.
6. `ip = requirePublicIp()` → `Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP`.
   Required for every protocol, custom domains included.
7. `own = node.endpoints ?? []`; `others` = every endpoint of every **other** node in the install
   (full table scan, all organizations), each tagged with `owner = <that node's name>`.
8. Build `wanted`:
   - **http**:
     1. `args.publicPort !== undefined` → `HTTP is always served on 80 and 443`.
     2. `domain = args.domain === undefined ? defaultDomain(node, ip) : validDomain(args.domain)`
        (→ `Domain must look like app.example.com`).
     3. If an `other` http endpoint has the same domain → `` `${domain} is already used by ${owner}` ``.
     4. `wanted = { protocol, port, domain }`.
   - **tcp / udp**:
     1. `args.domain !== undefined` → `Only HTTP endpoints have a domain`.
     2. **Idempotent shortcut**: if `args.publicPort === undefined` and `own` has an endpoint with the
        same `protocol` and the same `port` → return `endpointView(existing)` (no write, no sync).
     3. `taken` = publicPorts of same-protocol endpoints in `others ∪ own`, plus 80 and 443 for tcp.
     4. `publicPort = args.publicPort === undefined ? allocatePublicPort(port, taken) : validPort(args.publicPort)`
        (→ `No free public port left` / `Port must be 1–65535`).
     5. tcp and publicPort ∈ {80, 443} → `80 and 443 serve HTTP; pick another public port`.
     6. If an `other` endpoint has the same protocol and publicPort →
        `` `Port ${publicPort}/${protocol} is already used by ${owner}` `` (e.g. `Port 5432/tcp is already used by postgres`).
        A clash with this node's **own** endpoint is not an error: same key → replace (step 10).
     7. `wanted = { protocol, port, publicPort }`.
9. `key = endpointKey(wanted)`. If `own` has an endpoint with that key **and** the same `port` →
   return `endpointView(thatEndpoint)` (no write, no sync).
10. `rest = own.filter(e => endpointKey(e) !== key)`. If `rest.length >= 10` →
    `At most 10 endpoints per node`. (Replacing an endpoint when there are already 10 is allowed.)
11. `endpoint = { ...wanted, ...(port !== node.desired.port && { pinnedPort: true }), status: { state: "starting", at: now } }`.
    Note: when `node.desired.port` is undefined and a port was given, `pinnedPort` is `true`;
    re-exposing the same key with the node's own port drops `pinnedPort` (un-pins).
12. Write `node.endpoints = [...rest, endpoint]` (a replaced endpoint moves to the end of the array).
13. Schedule `proxy.sync` (after commit, delay 0).
14. Return `endpointView(endpoint)`.

Realtime: invalidates the node's environment (section 10).

### 4.2 `nodes.unexpose` — mutation, public

Args: `id` (node id, required), `protocol?`, `domain?` (string), `publicPort?` (number). Returns null.

1. `node = requireNode(id)` → `Node not found`.
2. `all = protocol === undefined && domain === undefined && publicPort === undefined` ("Make private").
3. `named = protocol === "http" ? domain !== undefined : publicPort !== undefined`.
4. If `!all && !(protocol && named)` → `Name the endpoint: protocol and domain (http) or public port`.
   (A partial selector must never fall through to "close everything".)
5. If the node has no endpoints → return (no-op).
6. `key = protocol && endpointKey({ protocol, domain: domain && validDomain(domain), publicPort })`.
   `validDomain` runs whenever `protocol` is set and `domain` is a non-empty string, even for
   tcp/udp (where the key ignores it) → may throw `Domain must look like app.example.com`.
7. `endpoints = key ? node.endpoints.filter(e => endpointKey(e) !== key) : []`.
8. If nothing was removed (same length) → return (no-op, no error for an unknown endpoint).
9. Write `endpoints` (or remove the field entirely when empty). Schedule `proxy.sync`.

### 4.3 `nodes.publicAddress` — query, public

Args: none. Auth: `requireUser` (any signed-in user, no organization check) → `Not authenticated`.
Returns `KEEL_PUBLIC_IP` as a string, or `null` when unset/empty. Used by the Networking section
for "point your domain here" and "open this port" hints.

### 4.4 Endpoint side effects in other node mutations

- `nodes.remove` (node delete): after deleting the row, if the deleted node had at least one
  endpoint, schedule `proxy.sync` (the next config simply omits it). The row is deleted first, in
  the same transaction as the schedule.
- `nodes.duplicate`: the copy gets **no** endpoints (and no `public`/`ingress`).
- `nodes.list` (query, `environmentId`): returns node views including the endpoint fields of 3.7.
  This is the reactive query the canvas renders endpoints from.

### 4.5 Error message catalogue (this area)

| Message | Thrown by |
| --- | --- |
| `Node not found` | expose, unexpose |
| `Not authenticated` | publicAddress |
| `Only services, databases and caches can be exposed` | expose |
| `Ship this Redis first: its password takes effect on the next Ship` | expose |
| `Port must be 1–65535` | expose (port, publicPort) |
| `Set the service's port first` | expose |
| `Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP` | expose |
| `HTTP is always served on 80 and 443` | expose |
| `Domain must look like app.example.com` | expose, unexpose |
| `<domain> is already used by <nodeName>` | expose |
| `Only HTTP endpoints have a domain` | expose |
| `No free public port left` | expose |
| `80 and 443 serve HTTP; pick another public port` | expose |
| `Port <publicPort>/<protocol> is already used by <nodeName>` | expose |
| `At most 10 endpoints per node` | expose |
| `Name the endpoint: protocol and domain (http) or public port` | unexpose |

---

## 5. Internal functions

### 5.1 `proxy.sync` — internal action (Node runtime)

No args, no return. Makes keel-proxy serve exactly the endpoints in the database. Idempotent,
whole-config. Scheduled (delay 0) by: `nodes.expose`, `nodes.unexpose`, `nodes.remove` (when the
node had endpoints), `nodesInternal.followPort`, `proxyInternal.resync` (cron), `migrations.run`
(every install/upgrade).

```ts
for (let pass = 0; pass < 3; pass++) {
  const { routes, reporter, acme } = await ctx.runQuery(internal.proxyInternal.syncInput, {});
  const statuses = await apply(routes, reporter, acme);
  await ctx.runMutation(internal.proxyInternal.setStatuses, { statuses });
  const after = await ctx.runQuery(internal.proxyInternal.syncInput, {});
  if (JSON.stringify(after.routes) === JSON.stringify(routes)) return;
}
```

Two syncs can overlap (Convex runs scheduled actions concurrently). Each re-reads after loading
and goes again if the routes moved, so the last one to finish loads the latest set. Route equality
is deep and order-sensitive over `{nodeId, protocol, port, domain, publicPort}` (undefined fields
dropped).

**Go recommendation**: one long-lived goroutine with a coalescing trigger (`chan struct{}` of
capacity 1, `Kick()` does a non-blocking send). Loop: wait for a kick → read input → apply →
write statuses → if the routes changed meanwhile, go again. This makes overlap impossible and is
behaviourally equivalent. Kick only **after** the triggering transaction commits.

#### 5.1.1 `apply(routes, reporter, acme)` → statuses

```ts
const at = Date.now();
const failed = new Map<string, string>();               // endpointKey → error
try {
  const addrs = routes.length > 0 ? await adminJSON<string[]>("/keel/host-addrs") : [];
  if (routes.length > 0 && addrs.length === 0)
    throw new Error("keel-proxy found no public network address on the control plane");
  for (;;) {                                             // each failed pass removes ≥1 endpoint
    const live = routes.filter((r) => !failed.has(endpointKey(r)));
    const res = await admin("POST", "/config/apps", caddyApps(live, addrs, reporter, acme));
    if (res.status < 300) break;
    const message = caddyError(res.text);
    const blamed = blame(message, live);
    if (blamed.size === 0) throw new Error(message);
    for (const [key, why] of blamed) failed.set(key, why);
  }
} catch (err) {
  // Proxy unreachable, or an error no endpoint can own: it keeps serving its previous config.
  const error = errorText(err);
  return routes.map((r) => ({ nodeId: r.nodeId, key: endpointKey(r), status: { state: "failed", error, at } }));
}
const domains = routes.filter((r) => r.protocol === "http" && !failed.has(endpointKey(r))).map((r) => r.domain);
const certs = domains.length > 0
  ? await adminJSON(`/keel/certs?${domains.map((d) => `name=${encodeURIComponent(d)}`).join("&")}`).catch(() => ({}))
  : {};
return routes.map((r) => {
  const error = failed.get(endpointKey(r));
  if (error) return { ..., status: { state: "failed", error, at } };
  if (r.protocol !== "http") return { ..., status: { state: "live", at } };
  const cert = certs[r.domain];
  if (cert?.state === "ok") return { ..., status: { state: "live", at } };
  if (cert?.state === "failed") return { ..., status: { state: "failed", error: certHint(cert.error), at } };
  return { ..., status: { state: "starting", at } };   // pending/unknown: the event handler reports later
});
```

Details:

- With zero routes, `addrs` is not fetched and the POST body is `{}` (removes every app: no
  listeners at all). CI asserts this state after a fresh install (`GET /config/` → `.apps // {} == {}`).
- Any `/keel/certs` failure (non-2xx, unreachable, bad JSON) is swallowed → `{}` → every loaded
  http endpoint is `starting`.
- The catch-all case overwrites earlier per-endpoint errors: every route (blamed or not) gets the
  same `error`.
- `at` is one timestamp for the whole pass.
- `errorText(err)` = message, all whitespace runs → one space, trimmed, first 300 chars.

#### 5.1.2 Admin client (unix-socket HTTP)

- `admin(method, path, body?)`: HTTP/1.1 over the unix socket `KEEL_PROXY_SOCKET` (default
  `/run/keel-proxy/admin.sock`). Header `content-type: application/json` only when there is a body;
  body is `JSON.stringify(body)`. Resolves `{status, text}` for any HTTP status.
- Socket idle timeout 30 s → `Error("keel-proxy did not answer within 30s")`.
- `ENOENT` or `ECONNREFUSED` → `Error("keel-proxy is not running (no admin socket at <socket path>)")`.
- `adminJSON(path)`: GET; status ≥ 300 → `Error(caddyError(text))`; else `JSON.parse(text)`.
- `caddyError(text)`: if `text` is JSON with a string `error`, take it; else the raw text. Then strip
  any number of leading `loading config: ` / `loading new config: ` prefixes
  (`/^(loading (new )?config: )+/`) and trim.

Admin endpoints used:

| Method + path | Request | Success response | Notes |
| --- | --- | --- | --- |
| `GET /keel/host-addrs` | – | JSON array of IP strings, e.g. `["203.0.113.7","2001:db8::1"]` (Go encoder: `null` when empty, see 13/Q2) | Plugin route (7.2). |
| `GET /keel/certs?name=<d1>&name=<d2>` | names URL-encoded, repeated `name` | `{"<d1>":{"state":"ok","notAfter":"2027-01-06T10:00:00Z"},"<d2>":{"state":"failed","error":"…"},"<d3>":{"state":"pending"}}` | Plugin route (7.2). |
| `POST /config/apps` | the `apps` object (section 6) | 200, empty body | Caddy core admin: replaces the whole `apps` value, keeps `admin`. Atomic: on error Caddy keeps the previous config and answers 4xx/5xx with `{"error":"loading config: loading new config: …"}`. An unchanged config is answered without reloading. |

#### 5.1.3 `blame(message, routes)` — failure attribution

Caddy loads all or nothing; a port held by another process blocks the whole config. Caddy's error
names the listener: e.g. `http app module: start: listening on host-tcp/[2001:db8::1]:443: listen tcp [2001:db8::1]:443: bind: address already in use`
or `layer4 app module: start: listen udp 203.0.113.7:53: bind: permission denied`.

```ts
const m = /listen (tcp|udp) \S*?:(\d+): (.+?)(?:$|\n)/.exec(message);   // first match only
if (!m) return new Map();
const [, protocol, rawPort, reason] = m;
const port = Number(rawPort);
const why = reason.includes("address already in use")
  ? `port ${port}/${protocol} is already in use on the control plane`
  : `cannot listen on ${port}/${protocol}: ${reason}`;
for (const r of routes) {
  const web = protocol === "tcp" && (port === 80 || port === 443) && r.protocol === "http";
  if (web || (r.protocol === protocol && r.publicPort === port))
    blamed.set(endpointKey(r), why[0].toUpperCase() + why.slice(1));
}
```

- Resulting texts: `Port 5432/tcp is already in use on the control plane`,
  `Cannot listen on 53/udp: bind: permission denied`.
- A failure on TCP 80 or 443 blames **every** http endpoint (the auto-HTTPS :80 server and the :443
  server are shared). UDP 443 is a normal udp endpoint port.
- `\S*?:(\d+): ` handles bracketed IPv6 (`[2001:db8::1]:443: `).
- Blamed endpoints are dropped and the config is posted again; if the error cannot be attributed
  to a still-live route (no match, or the port belongs to nobody live) → the whole sync fails
  (catch-all above). Termination: each retry removes at least one endpoint.

### 5.2 `proxyInternal.syncInput` — internal query

No args. Full scan of `nodes` (default order: creation time ascending), flattening endpoints in
array order:

```json
{
  "routes": [
    { "nodeId": "<id>", "protocol": "http", "port": 8080, "domain": "api-16w41g.203-0-113-7.sslip.io" },
    { "nodeId": "<id2>", "protocol": "tcp", "port": 5432, "publicPort": 5432 }
  ],
  "reporter": { "url": "<KEEL_PROXY_REPORT_URL || CONVEX_SITE_URL + '/proxy/events'>", "token": "<KEEL_WORKER_TOKEN or ''>" },
  "acme": { "ca": "<KEEL_ACME_CA or undefined>", "email": "<KEEL_ACME_EMAIL or undefined>" }
}
```

(`domain`/`publicPort` are absent rather than null for the protocol that lacks them; empty-string
env values count as unset.) **Ordering must be deterministic in Go**: it decides the order of http
routes and of `tls.automation.policies[0].subjects`; a changing order makes Caddy reload on every
sync instead of answering "config is unchanged".

### 5.3 `proxyInternal.setStatuses` — internal mutation

Args: `statuses: Array<{ nodeId: Id<"nodes">, key: string, status: EndpointStatus }>`.

For each distinct `nodeId`: load the node (skip if deleted or without endpoints). Build
`next: key → status` for that node. `same(a, b) = a.state === b.state && a.error === b.error`
(`at` ignored). If no endpoint of the node has a `next` entry that differs from its current status,
skip (no write). Otherwise rewrite the array: each endpoint whose key has a differing `next` status
gets it, every other endpoint (including ones added since the sync read them) keeps its status.
Endpoints removed since the read are simply absent. Matching is **by key only** (13/Q4).

Effect: a status that did not change is never written, so the 2-minute cron costs no writes and
no client invalidations while all is well.

### 5.4 `proxyInternal.resync` — internal mutation, run by cron

Cron (`crons.ts`): `crons.interval("keel-proxy resync", { minutes: 2 }, internal.proxyInternal.resync, {})`.

Handler: full scan of nodes; if any node has a non-empty `endpoints` array → schedule `proxy.sync`.
Otherwise nothing. Purpose: retry what failed for a passing reason (proxy restarting, a port freed),
pick up a changed host address (DHCP), and correct a status that lost a race with a cert report.
An unchanged config is a no-op in Caddy, so live endpoints' certificate work is not disturbed.

Go: a `time.Ticker` (2 min) that calls the same check and `Kick()`s the sync loop.

### 5.5 `proxyInternal.certReport` — internal mutation

Args: `event: "cert_obtained" | "cert_failed"`, `name: string`, `error?: string`.

Full scan of nodes. For every node with an http endpoint whose `domain === name`, rewrite those
endpoints' status (always written, no "same" check):

- `cert_obtained` → `{ state: "live", at: now }`
- `cert_failed` → `{ state: "failed", error: certHint(error), at: now }`

Unknown names are ignored. A failure is final only for this attempt: Caddy keeps retrying with
backoff, and the next `cert_obtained` flips it to live (e.g. once the user opens 80/443).

### 5.6 `nodesInternal.followPort` — internal mutation

Args: `id: Id<"nodes">`, `port: number`.

Called by `swarm.apply` after a successful create/update of `svc-<id>` (after clearing
`applyError`), **only when `desired.port` is set**, with `port = desired.port` of the revision just
applied:

```ts
const moves = (e: Endpoint) => !e.pinnedPort && e.port !== port;
if (!node?.endpoints?.some(moves)) return;       // also: node deleted → no-op
patch endpoints: moves(e) ? { ...e, port, status: { state: "starting", at: now } } : e
schedule proxy.sync
```

So a port change takes effect on the endpoint when it **ships**, not when it is staged; pinned
endpoints (exposed with an explicit different port) never move.

### 5.7 `migrations.run` — internal mutation (every install/upgrade)

Run by `deploy/functions-entrypoint.sh` (`convex run migrations:run`) after every functions deploy,
i.e. on every `install.sh` run. **Go: run at control-plane startup, every boot, before (or right
after) the API starts serving.** Each step is idempotent. Returns
`{ quickTunnelsConverted: number, redisPasswords: number, domainsMoved: number }`.

1. **Quick Tunnel → keel-proxy** (legacy fields): for every node with `public !== undefined || ingress !== undefined`:
   - If `node.public` is truthy **and** `type === "service"` **and** `desired.port` truthy **and**
     `KEEL_PUBLIC_IP` set **and** the node has no endpoints → it gets
     `endpoints = [{ protocol: "http", port: desired.port, domain: defaultDomain(node, ip), status: { state: "starting", at: now } }]`
     (counted in `quickTunnelsConverted`).
   - Always clear `public` and `ingress`. Without an IP the service goes private on purpose (a tunnel
     left running would be public with nothing in Keel to show or stop it).
2. **Redis password backfill**: for every node with `type === "cache"` and `engineOf(desired.image) === "redis"`
   without a `REDIS_PASSWORD` variable: insert `{ nodeId, key: "REDIS_PASSWORD", value: randomSecret(), secret: true }`
   (20 chars from `abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789`), set `dirty: true`,
   mark every node that references it dirty (`markReferrersDirty`, variables spec). Count in
   `redisPasswords`. (Only `cache` nodes; a `service` running redis is not backfilled.)
3. **Default domains follow the public IP**: only if `KEEL_PUBLIC_IP` is set. For every node with
   endpoints, every http endpoint with `movedDefaultDomain(node, domain, ip)` defined gets
   `domain = moved`, `status = { state: "starting", at: now }`; write the node if any moved. Count in
   `domainsMoved`. No uniqueness check (cannot collide: hash + name).
4. Schedule `swarm.removeLegacyTunnels` and `proxy.sync` (always; this is what makes a fresh or
   recreated proxy serve what the database holds).

### 5.8 `swarm.removeLegacyTunnels` — internal action

Docker Engine API on the manager socket:

1. `GET /services?filters={"label":["keel.ingress"]}` (services carrying the label `keel.ingress`,
   the old `ingress-<id>` cloudflared services).
2. For each: `DELETE /services/<ID>`, ignoring 404.
3. Returns the number found.

No-op once they are gone. Keep it for one release in Go only if you import installs that never ran
the TS `migrations.run`; otherwise drop it.

### 5.9 `POST /proxy/events` — HTTP action (cert reports from keel-proxy)

Served on the Convex site origin (`CONVEX_SITE_URL`, e.g. `http://<tailnet IP>:3211`).

| Step | Response |
| --- | --- |
| `Authorization` must be `Bearer <token>`, token (trimmed) equal to `KEEL_WORKER_TOKEN` by constant-time compare (lengths may differ: XOR over the max length, length mismatch counts). Unset `KEEL_WORKER_TOKEN` → always reject. | `401` body `unauthorized` |
| Body text longer than 256 × 1024 chars | `413` body `too large` |
| Not JSON | `400` body `bad json` |
| `event` not `"cert_obtained"`/`"cert_failed"`, or `name` not a string | `400` body `bad report` |
| Else run `certReport({ event, name, error: typeof error === "string" ? error : undefined })` | `200` body `ok` |

Request body (sent by the plugin): `{"event":"cert_failed","name":"api-16w41g.203-0-113-7.sslip.io","error":"…"}`
(`error` omitted when empty), headers `Content-Type: application/json`,
`Authorization: Bearer <KEEL_WORKER_TOKEN>`.

In Go with Caddy embedded in-process (section 12) this route disappears: the event handler calls
the cert-report logic directly. Keep it only if the edge stays a separate process.

---

## 6. The Caddy configuration built by `proxy.sync`

### 6.1 Base config (shipped in the image, never changed by Convex)

`apps/proxy/caddy.json`:

```json
{
  "admin": { "listen": "unix//run/keel-proxy/admin.sock|0600" }
}
```

Started with `caddy run --resume --config /etc/keel-proxy/caddy.json`: after a restart Caddy loads
its autosave (`/config/caddy/autosave.json`, the last config Convex pushed) instead of this file.
Convex only ever `POST`s `/config/apps`.

### 6.2 Builder (`caddyApps(routes, addrs, reporter, acme)`)

```ts
const hostPort = (addr, port) => addr.includes(":") ? `[${addr}]:${port}` : `${addr}:${port}`;
const upstream = (r) => `svc-${r.nodeId}:${r.port}`;
const internalName = (d) => d === "localhost" || d.endsWith(".localhost");

const apps = {};
const web = routes.filter((r) => r.protocol === "http");
const raw = routes.filter((r) => r.protocol !== "http");
if (web.length > 0) {
  apps.http = { servers: { public: {
    listen: addrs.map((a) => `host-tcp/${hostPort(a, 443)}`),
    protocols: ["h1", "h2"],                     // no h3: would need a UDP 443 host listener
    routes: web.map((r) => ({
      match: [{ host: [r.domain] }],
      handle: [{ handler: "reverse_proxy", upstreams: [{ dial: upstream(r) }] }],
      terminal: true,
    })),
  } } };
  const managed = web.map((r) => r.domain).filter((d) => !internalName(d));
  if ((acme.ca || acme.email) && managed.length > 0)
    apps.tls = { automation: { policies: [{ subjects: managed, issuers: issuers(acme) }] } };
  apps.events = { subscriptions: [{
    events: ["cert_obtained", "cert_failed"],
    handlers: [{ handler: "keel", url: reporter.url, token: reporter.token }],
  }] };
}
if (raw.length > 0) {
  apps.layer4 = { servers: Object.fromEntries(raw.map((r) => [
    `${r.protocol}-${r.publicPort}`,                                  // e.g. "tcp-5432", "udp-27015"
    { listen: addrs.map((a) => `host-${r.protocol}/${hostPort(a, r.publicPort)}`),
      routes: [{ handle: [{ handler: "proxy",
        upstreams: [{ dial: [`${r.protocol === "udp" ? "udp/" : ""}${upstream(r)}`] }] }] }] },
  ])) };
}

function issuers({ ca, email }) {
  if (ca) return [{ module: "acme", ca, ...(email && { email }) }];
  return [{ module: "acme", email }, { module: "acme", ca: "https://acme.zerossl.com/v2/DV90", email }];
}
```

Rules in words:

- `http` app: exactly one server named `public`, one `host-tcp/<addr>:443` listen per host address,
  one host-matched `reverse_proxy` route per http endpoint (`dial` is a **string**). Caddy's
  automatic HTTPS then manages one certificate per host and synthesizes a server named
  `remaining_auto_https_redirects` on `host-tcp/<addr>:80` (same network and hosts, port swapped)
  that redirects HTTP→HTTPS and answers ACME HTTP-01. TLS-ALPN-01 is served on 443.
- `tls` app: only when `KEEL_ACME_CA` or `KEEL_ACME_EMAIL` is set **and** there is at least one
  non-`localhost` http domain. One policy, `subjects` = those domains in route order.
  - `KEEL_ACME_CA` set (dev/CI: Let's Encrypt staging): the only issuer, with `email` when set.
  - Only `KEEL_ACME_EMAIL`: Let's Encrypt (`{"module":"acme","email":…}`, default CA) then ZeroSSL
    (Caddy auto-generates ZeroSSL EAB credentials from the email). This is the pair Caddy builds
    itself when it has an email.
  - Neither: no `tls` app; Caddy's defaults = Let's Encrypt alone.
  - `localhost`/`*.localhost` names get Caddy's internal CA through automatic HTTPS (dev HTTPS
    end to end); they are never in a policy.
- `events` app: present iff there is an http endpoint; subscribes the plugin handler `keel`.
- `layer4` app (caddy-l4): one server per tcp/udp endpoint, named `<protocol>-<publicPort>`,
  listening on `host-<protocol>/<addr>:<publicPort>` per host address, one route with handler
  `proxy` whose upstream `dial` is an **array** with one entry: `svc-<id>:<port>` (tcp) or
  `udp/svc-<id>:<port>` (udp). No matchers.
- No `admin`, `storage`, `logging` keys are sent (the POST targets `/config/apps` only).

### 6.3 Full example

Inputs: http endpoints `api-16w41g.203-0-113-7.sslip.io` and `app.example.com` (both node
`j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4`, port 8080), tcp 5432 → node `k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj`:5432,
udp 27015 → node `jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y`:27015; host addresses `203.0.113.7`,
`2001:db8::1`; `KEEL_ACME_EMAIL=ops@example.com`; reporter URL `http://100.64.0.1:3211/proxy/events`.
Resulting full Caddy config (base + POSTed `apps`):

```json
{
  "admin": { "listen": "unix//run/keel-proxy/admin.sock|0600" },
  "apps": {
    "http": {
      "servers": {
        "public": {
          "listen": ["host-tcp/203.0.113.7:443", "host-tcp/[2001:db8::1]:443"],
          "protocols": ["h1", "h2"],
          "routes": [
            {
              "match": [{ "host": ["api-16w41g.203-0-113-7.sslip.io"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "svc-j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4:8080" }] }],
              "terminal": true
            },
            {
              "match": [{ "host": ["app.example.com"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "svc-j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4:8080" }] }],
              "terminal": true
            }
          ]
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["api-16w41g.203-0-113-7.sslip.io", "app.example.com"],
            "issuers": [
              { "module": "acme", "email": "ops@example.com" },
              { "module": "acme", "ca": "https://acme.zerossl.com/v2/DV90", "email": "ops@example.com" }
            ]
          }
        ]
      }
    },
    "events": {
      "subscriptions": [
        {
          "events": ["cert_obtained", "cert_failed"],
          "handlers": [{ "handler": "keel", "url": "http://100.64.0.1:3211/proxy/events", "token": "<KEEL_WORKER_TOKEN>" }]
        }
      ]
    },
    "layer4": {
      "servers": {
        "tcp-5432": {
          "listen": ["host-tcp/203.0.113.7:5432", "host-tcp/[2001:db8::1]:5432"],
          "routes": [{ "handle": [{ "handler": "proxy", "upstreams": [{ "dial": ["svc-k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj:5432"] }] }] }]
        },
        "udp-27015": {
          "listen": ["host-udp/203.0.113.7:27015", "host-udp/[2001:db8::1]:27015"],
          "routes": [{ "handle": [{ "handler": "proxy", "upstreams": [{ "dial": ["udp/svc-jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y:27015"] }] }] }]
        }
      }
    }
  }
}
```

Variants: `KEEL_ACME_CA=https://acme-staging-v02.api.letsencrypt.org/directory` (no email) →
`"issuers": [{ "module": "acme", "ca": "https://acme-staging-v02.api.letsencrypt.org/directory" }]`.
Nothing exposed → `"apps": {}`.

Caddy compares the re-marshaled raw config (map keys sorted by Go's encoder, arrays ordered) to
decide "config is unchanged"; key order in the generated JSON does not matter, array order does.

Security note: the reporter token (`KEEL_WORKER_TOKEN`, also the worker's bearer) is written into
Caddy's config and therefore into the autosave file in the `proxy-config` volume.

---

## 7. keel-proxy (`apps/proxy`): the Caddy plugin and image

Go package `proxy`, module `github.com/ThallesP/keel/apps/proxy`, compiled into Caddy with xcaddy.

### 7.1 Versions (pin together, bump deliberately)

| Component | Version | Where |
| --- | --- | --- |
| Caddy | `v2.11.7` (`caddy:2.11.7-builder`, `caddy:2.11.7-alpine`, `xcaddy build v2.11.7`) | Dockerfile, go.mod |
| caddy-l4 | `github.com/mholt/caddy-l4@v0.1.2` (its go.mod requires caddy ≥ v2.11.4; MVS picks 2.11.7) | Dockerfile `--with` |
| Go | `go 1.27.1` | go.mod |
| Direct deps | `go.uber.org/zap v1.28.0`, `golang.org/x/sys v0.48.0` | go.mod |
| Notable indirect | `github.com/caddyserver/certmagic v0.25.6`, `github.com/mholt/acmez/v3 v3.1.7`, `github.com/caddyserver/zerossl v0.1.6`, `github.com/quic-go/quic-go v0.63.0`, `github.com/miekg/dns v1.1.73` | go.mod |

Dockerfile:

```dockerfile
FROM caddy:2.11.7-builder AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY *.go ./
RUN xcaddy build v2.11.7 --output /usr/bin/caddy \
  --with github.com/mholt/caddy-l4@v0.1.2 \
  --with github.com/ThallesP/keel/apps/proxy=/src
FROM caddy:2.11.7-alpine
COPY --from=build /usr/bin/caddy /usr/bin/caddy
COPY caddy.json /etc/keel-proxy/caddy.json
CMD ["caddy", "run", "--resume", "--config", "/etc/keel-proxy/caddy.json"]
```

`--with github.com/mholt/caddy-l4` imports the root package, which blank-imports **every** l4
module (layer4 app plus l4clock, l4close, l4dns, l4echo, l4http, l4openvpn, l4postgres, l4proxy,
l4proxyprotocol, l4quic, l4rdp, l4regexp, l4remoteiplist, l4socks, l4ssh, l4subroute, l4tee,
l4throttle, l4tls, l4vars, l4winbox, l4wireguard, l4xmpp). Keel uses only `layer4` and `l4proxy`.

### 7.2 Modules registered by the plugin

| Module ID | Kind | Behaviour |
| --- | --- | --- |
| `admin.api.keel` | admin router | Routes `GET /keel/host-addrs`, `GET /keel/certs` on the admin endpoint (the unix socket). Non-GET → 405 `method not allowed`. |
| `events.handlers.keel` | event handler | Fields `url`, `token` (JSON `omitempty`). Reports certificate outcomes (7.3). |
| network `host-tcp`, `host-udp` | `caddy.RegisterNetwork` | Listeners opened in the host's network namespace (7.4). |

`GET /keel/host-addrs`: `hostAddrs()` (7.4) as JSON; error → 500 with the error.

`GET /keel/certs?name=…` (repeated): for each `name`, `certOf(name)`:

```go
for _, c := range caddytls.AllMatchingCertificates(name) {     // in-memory cert cache, wildcards included
	if c.Leaf != nil && time.Now().Before(c.Leaf.NotAfter) {
		return Cert{State: "ok", NotAfter: c.Leaf.NotAfter}
	}
}
if msg, ok := failures[name]; ok { return Cert{State: "failed", Error: msg} }
return Cert{State: "pending"}   // ACME still working, or the name is not managed
```

`Cert` JSON: `{"state":"ok|failed|pending","error":"…" (omitempty),"notAfter":"RFC3339" (omitzero)}`.
`failures` is a package-level `map[string]string` under a mutex: last obtain/renew failure per
hostname, cleared when a cert arrives. Package state on purpose: it outlives config reloads (which
recreate every module) but not process restarts.

### 7.3 Event handler `keel` (cert reports)

`Provision`: builds an `http.Client` with `Timeout: 10s` whose `DialContext` dials **from the host
namespace** (`inHost`, 5 s dial timeout), because Convex listens on the host (tailnet address, or
loopback in development) which the container namespace may not route to.

`Handle(ctx, e)` (always returns nil):

1. `name := e.Data["identifier"].(string)`; empty → ignore.
2. `cert_obtained`: delete `failures[name]`; report `{event, name}`.
3. `cert_failed`: `error = errorText(e.Data["error"])` (`error.Error()`, string, or `fmt.Sprint`;
   whitespace runs collapsed to single spaces via `strings.Fields`).
   - contains `context canceled` → ignore (a config reload cancelled the attempt; the new config
     starts another).
   - `certOf(name).State == "ok"` → ignore (a failed **renewal**: the current certificate keeps
     serving while Caddy retries).
   - else `failures[name] = error`; report `{event, name, error}`.
4. Any other event → ignore.
5. Report only when `url != ""`, asynchronously (`go r.post(report)`).

`post`: JSON body `{"event","name","error"(omitempty)}`, `POST url`, headers
`Content-Type: application/json`, `Authorization: Bearer <token>`. Up to 4 attempts; before attempt
n ∈ {1,2,3} sleep n×2 s (2, 4, 6 s). Success = status < 300. Failures are logged (warn) and
dropped: a lost report only delays the canvas, the next sync reads `/keel/certs`.

certmagic v0.25.6 event payloads (for reference): `cert_obtained` data has `renewal`, `identifier`,
`issuer`, `storage_path`, `private_key_path`, `certificate_path`, `metadata_path`, `csr_pem`
(+ `remaining` on renewal); `cert_failed` has `renewal`, `identifier`, `issuers`, `error`
(+ `remaining` on renewal). A certificate loaded from storage at startup emits **no** event; that is
why sync also reads `/keel/certs`.

### 7.4 Host-namespace listeners (`hostns.go`)

Why: the proxy container sits on the `keel` overlay (to resolve and dial `svc-<id>:<port>` through
Docker DNS), but its public sockets must be in the host namespace. A Docker port mapping is fixed at
container create time (a new tcp/udp port would need a restart), and `network_mode: host` cannot
reach the overlay. A socket keeps the namespace it was created in, so: create it in the host netns,
keep using it from the container netns.

```go
const HostNetNS = "/run/hostns/net"      // /proc/1/ns/net bind-mounted read-only (compose)

func init() {
	caddy.RegisterNetwork("host-tcp", listenInHost("tcp"))
	caddy.RegisterNetwork("host-udp", listenInHost("udp"))
}

func listenInHost(network string) caddy.ListenerFunc {
	return func(ctx context.Context, _, host, portRange string, portOffset uint, cfg net.ListenConfig) (any, error) {
		port, err := strconv.ParseUint(portRange, 10, 16)
		if err != nil { return nil, fmt.Errorf("host networks take a single port, got %q", portRange) }
		addr := caddy.NetworkAddress{Network: network, Host: host, StartPort: uint(port), EndPort: uint(port)}
		var ln any
		err = inHost(func() error { var err error; ln, err = addr.Listen(ctx, portOffset, cfg); return err })
		return ln, err
	}
}
```

Going through Caddy's own `tcp`/`udp` listen path keeps `SO_REUSEPORT` and Caddy's listener pool
(key = `tcp/<host>:<port>`): a reload re-binds the same address while the old listener still serves,
and open connections survive (a Postgres session through the proxy kept its backend pid across a
reload that added a port). For udp the result is a `net.PacketConn`, which caddy-l4 handles.

`inHost(fn)`:

1. `runtime.LockOSThread()`.
2. Open own netns `/proc/self/task/<gettid>/ns/net` and the host netns `HostNetNS`
   (error: `host network namespace: <err>`).
3. `unix.Setns(hostFd, CLONE_NEWNET)` (error: `enter host network namespace: <err>`); on any early
   error unlock the thread and return.
4. Run `fn`.
5. Deferred: `Setns(ownFd, CLONE_NEWNET)`; unlock the thread **only if** switching back succeeded.
   A thread that cannot switch back stays locked, so Go destroys it when the goroutine exits.

Requires `CAP_SYS_ADMIN` (setns) and the host netns file mounted at `/run/hostns/net`.

`hostAddrs()` (run inside `inHost`): for every interface that is **up**, every `*net.IPNet` address
where `public(ifaceName, ip)`:

```go
skipPrefixes = []string{"lo", "tailscale", "docker", "br-", "veth"}   // interface name prefixes
public(iface, ip) = no skip prefix matches &&
	!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() &&
	!100.64.0.0/10.Contains(ip) && !fd7a:115c:a1e0::/48.Contains(ip)
```

Returned as `ip.String()` in interface order. Private LAN addresses (homelab behind a router
port-forward) and global IPv6 are included. Test table (`hostns_test.go`): `enp1s0 192.168.0.197`
true; `eth0 203.0.113.7` true; `eth0 2001:db8::1` true; `eth0 fe80::1` false; `lo 127.0.0.1` false;
`tailscale0 100.123.155.61` false; `eth0 100.100.1.1` false (tailnet range on any interface);
`eth0 fd7a:115c:a1e0::1` false; `docker_gwbridge 172.19.0.1` false; `br-7f74391f29c5 172.22.0.1` false.

### 7.5 Deployment (`deploy/compose.yml`, service `proxy`)

```yaml
proxy:
  image: ${KEEL_IMAGE_PREFIX:-ghcr.io/thallesp}/keel-proxy:${KEEL_VERSION:-latest}
  restart: unless-stopped
  cap_drop: [ALL]
  cap_add: [SYS_ADMIN, NET_BIND_SERVICE]       # setns; 80/443 in the host namespace
  security_opt: ["no-new-privileges:true"]
  volumes:
    - /proc/1/ns/net:/run/hostns/net:ro
    - proxy-admin:/run/keel-proxy               # admin socket, shared with `backend` only
    - proxy-data:/data                          # certificates + ACME accounts (/data/caddy/...)
    - proxy-config:/config                      # autosave of the last pushed config (--resume)
  networks: [keel]                              # external overlay; no `ports:` on purpose
  healthcheck:
    test: ["CMD", "test", "-S", "/run/keel-proxy/admin.sock"]
    interval: 5s
    start_period: 5s
```

The Convex `backend` container mounts the same `proxy-admin` volume at `/run/keel-proxy`. Nothing on
the overlay can reach the admin API. `install.sh` creates Swarm and the overlay
(`bootstrap-swarm.sh --swarm-only`) before `compose up`, because the proxy joins the overlay. CI
(`.github/workflows/ci.yml`) asserts, through the backend container:
`curl --unix-socket /run/keel-proxy/admin.sock http://proxy/keel/host-addrs | jq 'type=="array" and length>0'`
and `http://proxy/config/ | jq '(.apps // {}) == {}'` after a fresh install.

---

## 8. Endpoint status state machine

```
                       expose (new or replaced) ─┐
     followPort (port moved, not pinned) ────────┼──► starting(at=now)
     migrations: legacy conversion / domain move ┘
                                 │
                       proxy.sync (apply) outcome
      ┌──────────────────────────┼────────────────────────────────────────────┐
      │ whole load failed        │ attributed listen error                     │ loaded
      ▼                          ▼                                             ▼
 failed(errorText)      failed("Port N/p is already in use on      tcp/udp ─► live
 (ALL endpoints)               the control plane" |                 http: /keel/certs
                               "Cannot listen on N/p: <reason>")      ok      ─► live
                                                                      failed  ─► failed(certHint(err))
                                                                      pending/unknown/certs error ─► starting
 certReport (any time, by domain, all nodes):
   cert_obtained ─► live(at=now)       cert_failed ─► failed(certHint(err), at=now)
```

- `setStatuses` writes only real changes (state or error differ); `certReport` always writes.
- Nothing else writes `status`. Node lifecycle (stop, crash, redeploy) never changes it.
- Retry: the cron re-syncs every 2 minutes while anything is exposed; Caddy itself retries ACME
  with backoff, and its success arrives as `cert_obtained`.
- Race: a `cert_obtained` report processed between a sync's `/keel/certs` read (pending) and its
  `setStatuses` (starting) is overwritten back to `starting`; the next sync (≤ 2 min) reads `ok` and
  sets `live`. In Go, with the sync loop and the event handler in one process, process cert events
  through the same goroutine (or re-read `certOf` inside the status write) to avoid the window.

---

## 9. Environment variables

| Name | Read by | Meaning | Default |
| --- | --- | --- | --- |
| `KEEL_PUBLIC_IP` | endpoints.ts (expose, views, cert hint, migrations), nodes.publicAddress | Control plane's public IPv4 (behind NAT: the router's). Names default domains and tcp/udp addresses. Detected by `install.sh` on every run (`curl -4` to api.ipify.org, ifconfig.me/ip, icanhazip.com; must match `^[0-9]{1,3}(\.[0-9]{1,3}){3}$`) unless set explicitly (`KEEL_PUBLIC_IP_OVERRIDE` in `.env`); detection failure keeps the last value. `functions-entrypoint.sh` removes it from Convex env when empty. | unset → Expose refuses; addresses show `<public IP>` |
| `KEEL_ACME_EMAIL` | proxyInternal.syncInput | ACME account email; adds ZeroSSL after Let's Encrypt. Removed from Convex env when empty. | unset |
| `KEEL_ACME_CA` | proxyInternal.syncInput | ACME directory URL that replaces both issuers (dev/CI: Let's Encrypt staging). | unset |
| `KEEL_PROXY_SOCKET` | proxy.ts | Admin socket path. | `/run/keel-proxy/admin.sock` |
| `KEEL_PROXY_REPORT_URL` | proxyInternal.syncInput | Where the `keel` handler POSTs cert reports. | `${CONVEX_SITE_URL}/proxy/events` |
| `CONVEX_SITE_URL` | proxyInternal.syncInput | Convex HTTP-actions origin (`CONVEX_SITE_ORIGIN`, `http://<KEEL_ADDR>:3211`). | built in |
| `KEEL_WORKER_TOKEN` | http.ts, proxyInternal.syncInput | Bearer for `/proxy/events` (same token as the worker). Unset → reporter token `""` and every report 401s. | required by install |

Proxy-side constants (not env): `HostNetNS=/run/hostns/net`; the official Caddy image sets
`XDG_DATA_HOME=/data`, `XDG_CONFIG_HOME=/config` (storage at `/data/caddy`, autosave at
`/config/caddy/autosave.json`).

Go: read these once at startup into config; empty string = unset (as in TS).

---

## 10. Realtime (WebSocket invalidation)

Reactive queries the web subscribes to in this area:

| Query | Where | Invalidation key |
| --- | --- | --- |
| `nodes.list({environmentId})` (node views incl. `public`, `publicUrl`, `endpoints[]`) | canvas, Settings → Public networking, Deployments meta strip, card subtitle | `env:<environmentId>` |
| `nodes.publicAddress()` | Settings → Public networking | `install:public-ip` (changes only when `KEEL_PUBLIC_IP` changes, i.e. on restart/upgrade; clients reconnect and refetch anyway) |

Writes and the keys they must publish (after commit):

| Write | Publish |
| --- | --- |
| `nodes.expose` (when it writes), `nodes.unexpose` (when it writes), `nodes.remove` | `env:<node.environmentId>` |
| `proxyInternal.setStatuses` (only nodes actually patched) | `env:<environmentId of each patched node>` |
| `proxyInternal.certReport` | `env:<environmentId of each patched node>` |
| `nodesInternal.followPort` (when it writes) | `env:<node.environmentId>` |
| `migrations.run` | `env:<…>` for every patched node (or a global `install:*` flush at boot) |

The 2-minute resync must publish nothing when no status changed.

Suggested HTTP API (non-normative; keep the JSON shapes and messages):
`POST /api/nodes/{id}/expose` body `{protocol?, port?, domain?, publicPort?}` → `endpointView`;
`POST /api/nodes/{id}/unexpose` body `{protocol?, domain?, publicPort?}` → `null`;
`GET /api/public-address` → `string | null`. Errors as the shared error envelope with the exact
messages of 4.5.

---

## 11. External calls summary

| Target | Call | When |
| --- | --- | --- |
| keel-proxy admin (unix socket) | `GET /keel/host-addrs`, `POST /config/apps`, `GET /keel/certs?name=…` | every `proxy.sync` pass |
| Convex HTTP (from keel-proxy, host netns) | `POST /proxy/events` | cert obtained / failed |
| Docker Engine (manager socket) | `GET /services?filters={"label":["keel.ingress"]}`, `DELETE /services/{id}` | `migrations.run` |
| ACME CAs (from Caddy) | Let's Encrypt (default), ZeroSSL `https://acme.zerossl.com/v2/DV90` (+ EAB from `https://api.zerossl.com/acme/eab-credentials-email`), or `KEEL_ACME_CA` | certificate management |
| Swarm DNS (from keel-proxy) | resolve `svc-<nodeId>` on the `keel` overlay | every proxied connection |

No Axiom calls in this area.

---

## 12. Embedding Caddy in the Go control-plane binary

### 12.1 What disappears

- The admin unix socket, `KEEL_PROXY_SOCKET`, the `proxy-admin` volume, the 30 s timeout and the
  `keel-proxy is not running …` error (the proxy cannot be "not running" independently any more).
- `admin.api.keel` routes: `/keel/host-addrs` → direct call to `hostAddrs()`; `/keel/certs` →
  direct call to `certOf(name)` (same `caddytls.AllMatchingCertificates`, same package-level
  `failures` map).
- `POST /config/apps` → `caddy.Load(fullConfigJSON, false)` (12.3).
- `POST /proxy/events`, `KEEL_PROXY_REPORT_URL`, the reporter's HTTP client, its host-netns dialer
  and the token inside the Caddy config: the `events.handlers.keel` module stays (event subscriptions
  are configured by JSON, so the handler must still be a registered module), but its `Handle` calls
  an in-process function with the same filtering (context canceled, renewal while a valid cert
  exists, clear `failures` on obtain) that runs `certReport` directly.
- `caddy run --resume` and the `proxy-config` autosave: on every boot the control plane runs
  `migrations` then sync from the database, which is the source of truth. Set
  `"admin": {"disabled": true, "config": {"persist": false}}`.
- The xcaddy build and the separate keel-proxy image (if fully embedded; see 12.5).

### 12.2 Modules to import (Caddy v2.11.7, caddy-l4 v0.1.2)

```go
import (
	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"              // http app, servers, host matcher, automatic HTTPS
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy" // http.handlers.reverse_proxy + http transport + LB policies
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"               // tls app, tls.issuance.acme / internal / zerossl, cert cache (AllMatchingCertificates)
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"               // pki app: needed by tls.issuance.internal (localhost / *.localhost names, dev)
	_ "github.com/caddyserver/caddy/v2/modules/caddyevents"            // events app (cert_obtained / cert_failed subscriptions)
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"            // caddy.storage.file_system, if storage is set explicitly (recommended)
	_ "github.com/mholt/caddy-l4/layer4"                                // layer4 app, servers, routes (+ remote_ip/local_ip/not matchers)
	_ "github.com/mholt/caddy-l4/modules/l4proxy"                       // layer4.handlers.proxy (tcp/udp proxy, its LB policies)
)
```

- Do **not** import the caddy-l4 root package (`github.com/mholt/caddy-l4`): it pulls every l4
  module (section 7.1). No l4 matchers are used today; per-endpoint IP allowlists later would use
  `layer4.matchers.remote_ip` (already in `layer4`).
- `modules/caddytls/standardstek` is only needed if `tls.session_tickets` is configured (Keel does
  not); `modules/logging` only if `logging` sets encoders/writers beyond the core defaults.
- Low-risk alternative: `_ "github.com/caddyserver/caddy/v2/modules/standard"` (all standard
  modules, larger binary).
- Keel's own modules: the `events.handlers.keel` handler (in-process variant), and the networks of
  12.4 when applicable.

### 12.3 Loading config in-process

```go
cfg := map[string]any{
	"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
	"storage": map[string]any{"module": "file_system", "root": "<KEEL data dir>/caddy"},
	"apps":    caddyApps(live, addrs, acme),            // same builder as 6.2, minus reporter url/token
}
b, _ := json.Marshal(cfg)
err := caddy.Load(b, false)   // nil when the config is unchanged (no reload, errSameConfig swallowed)
```

- `caddy.Load` is atomic exactly like the admin API: on error the previous config keeps running and
  the error is `loading new config: <app> app module: start: …`. Keep `caddyError`'s prefix strip
  and the `blame` regex unchanged (Go's `net.OpError` text is the same `listen tcp <addr>: bind: …`).
- Call it only from the sync goroutine. On shutdown call `caddy.Stop()`.
- **Certificate storage must persist** across restarts and upgrades. Migrating an existing install:
  copy the `keel_proxy-data` volume's `caddy/` tree (`certificates/`, `acme/`, `pki/`, …;
  `/var/lib/docker/volumes/keel_proxy-data/_data/caddy`) to the new storage root before the first
  start, or every default domain re-issues and burns the shared sslip.io Let's Encrypt quota.
- The CI assertions of 7.5 need a replacement (e.g. an authenticated debug endpoint or a CLI
  command that prints `hostAddrs()` and the loaded apps).

### 12.4 Listeners: is the host-namespace trick still needed?

- **Binary on the host (systemd, or a container with `network_mode: host`): not for listening.**
  Sockets are already in the host netns. Drop `host-tcp`/`host-udp`, `inHost` for listeners,
  `/run/hostns/net` and `CAP_SYS_ADMIN`-for-listening. Generate `"listen": ["tcp/203.0.113.7:443"]`
  (http) and `"tcp/<addr>:<port>"` / `"udp/<addr>:<port>"` (layer4); everything else in section 6
  is unchanged (automatic HTTPS derives `tcp/<addr>:80` the same way). `hostAddrs()` runs without
  setns, same filter (still never a wildcard: `tailscale serve` owns 443 on the tailnet IP). Keep
  `CAP_NET_BIND_SERVICE` (or root) for 80/443 and ports < 1024.
- **But the upstream side breaks on the host.** `svc-<nodeId>` resolves only through Docker's
  embedded DNS (`127.0.0.11`, which exists only inside the network namespace of a container attached
  to the `keel` overlay), and overlay/VIP addresses (pool `10.200.0.0/16`) are not routed from the host
  namespace. A host process gets a DNS error: http → 502, tcp/udp → connection closed. Swarm's
  routing mesh is not an option (services must never publish ports). So the trick inverts: dial
  inside an overlay-attached namespace. Options, in order of recommendation:
  1. **Edge as its own container, same binary** (`keel edge` role on the `keel` overlay, exactly
     today's container settings of 7.5: `/proc/1/ns/net` mounted, `cap_drop: ALL`,
     `cap_add: SYS_ADMIN, NET_BIND_SERVICE`, `no-new-privileges`, no docker.sock). Keep `hostns.go`
     verbatim (`host-tcp`/`host-udp`), keep the admin API on a unix socket (or a small RPC), keep
     `--resume`-style persistence of the last config, keep `POST /proxy/events` (or an equivalent RPC)
     for cert reports. The control-plane side (sections 4–5) is ported to Go unchanged. Smallest
     behaviour change and keeps the only internet-facing parser away from the Docker socket.
  2. **Whole control plane in one container on the overlay** (needs docker.sock for Swarm): keep
     `hostns.go` verbatim for listeners; upstream dialing works natively. Everything in 12.1–12.3
     applies. Security regression to accept explicitly: the process that parses internet traffic
     then holds root-equivalent Docker access (today keel-proxy has no docker.sock).
  3. **Binary on the host + overlay "anchor" namespace for dialing** (mirror image of hostns.go):
     keep (or create via the Docker API) a minimal container attached to `keel`
     (`HostConfig.NetworkMode: "keel"`, restart `unless-stopped`), take its netns
     (`/proc/<State.Pid>/ns/net` or `NetworkSettings.SandboxKey`, re-resolved when the container
     restarts) and run every upstream dial inside it with `setns` (still `CAP_SYS_ADMIN`). Name
     resolution must also happen there: a `net.Resolver{PreferGo: true, Dial: …}` that dials
     `127.0.0.11:53` inside the anchor netns (the host's `/etc/resolv.conf` does not know
     `svc-*`). Hooks needed: a custom reverse-proxy transport module wrapping
     `reverseproxy.HTTPTransport` that replaces `Transport.DialContext` after `Provision`
     (`Transport *http.Transport` is an exported field); and a Keel layer4 handler replacing
     `layer4.handlers.proxy`, because `l4proxy` dials with plain `net.Dial` and has no dialer hook
     (tcp: bidirectional copy with half-close; udp: one upstream `net.Conn` per client session that
     caddy-l4 hands the handler, with an idle timeout). Most code, most moving parts; only worth it
     if "no container for the control plane" is a hard requirement.

### 12.5 Unchanged by embedding

Endpoint data model, every public/internal function's behaviour and messages, the default-domain
scheme, port allocation, failure attribution, the cert hint, the status state machine, the cron,
the startup migration + sync, and the generated `apps` JSON (modulo network names and the reporter
fields).

---

## 13. Quirks, edge cases and decisions for the Go port

| # | Current behaviour | Recommendation |
| --- | --- | --- |
| Q1 | `shortHash` takes the Convex node id. Stored domains survive an id change, but `movedDefaultDomain` (IP change) and `defaultDomain` for new endpoints depend on the id string, and the upstream is `svc-<id>`. | Keep Convex ids as the node id string in Go (they are opaque strings), or store a `hashSeed`/legacy id per node and use it for `shortHash`. Changing ids also requires renaming every Swarm service. |
| Q2 | With no qualifying host address, `/keel/host-addrs` encodes a nil slice as `null`; `addrs.length` then throws, so every endpoint fails with `Cannot read properties of null (reading 'length')` instead of the intended `keel-proxy found no public network address on the control plane`. | Treat nil as empty and use the intended message. |
| Q3 | Unauthenticated callers of expose/unexpose get `Node not found`. | Preserve (the CLI maps it to `SERVICE_NOT_FOUND`); the auth layer may still reject earlier with `Not authenticated` if all API routes require a session. |
| Q4 | `setStatuses` matches by `endpointKey` only: an endpoint replaced with the same key (new port) between read and write receives the old pass's status; the next pass corrects it. | Fine with a single sync goroutine; optionally also compare `port`. |
| Q5 | Cert report vs sync race (section 8). | Serialize cert events with the sync loop in Go. |
| Q6 | `resync` only kicks a sync while some endpoint exists. If the sync after the last unexpose failed, the proxy keeps serving the old config until the next install. | Embedded: moot (restart rebuilds). Separate edge: kick a sync at control-plane boot and when the edge (re)connects. |
| Q7 | Expose's uniqueness checks and `syncInput`/`certReport`/`resync` are full table scans across all organizations; `<domain> is already used by <nodeName>` can name a node of another organization. | Keep install-wide uniqueness (it is a property of the single proxy); use indexes; consider not leaking foreign node names (message text must stay the same shape). |
| Q8 | Port allocation ignores ports held by non-Keel host processes; they fail at bind with `Port N/tcp is already in use on the control plane`. | Preserve. |
| Q9 | `validDomain` accepts dotted IPv4 (`203.0.113.7`) and `*.localhost`; IPs then fail ACME. | Preserve (or reject all-numeric last label; that would be a new message, avoid). |
| Q10 | The Redis guard uses `engineOf(image)` for every node type, but the migration backfills only `cache` nodes, so a `service` running a redis image can only be exposed after the user adds `REDIS_PASSWORD` and ships. | Preserve. |
| Q11 | `certReport` always writes (no "same" check), publishing an invalidation on every renewal. | Harmless; optionally skip identical status. |
| Q12 | Text truncation (`slice(0,300)`, `slice(0,200)`) counts UTF-16 units; whitespace collapse uses JS `\s`. | Use rune-safe truncation and `strings.Fields`; differences only matter for non-ASCII errors. |
| Q13 | `unexpose` with a non-integer `publicPort` builds a key like `tcp:5432.5` and silently removes nothing. | Preserve (no error), or validate with `Port must be 1–65535`. |
| Q14 | The worker token is stored in Caddy's autosaved config. | Embedded: gone. Separate edge: prefer a dedicated edge token. |
