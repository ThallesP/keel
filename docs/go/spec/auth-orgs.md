# Porting spec: authentication, organizations, access control, HTTP router

Scope: everything a Go rewrite needs to reproduce how Keel authenticates people, the CLI, the
per-node worker and keel-proxy; how the one organization of an install is founded and joined; which
access rule every function applies; and every HTTP route the Convex deployment serves.

Sources read (worktree `go`, commit `36c2ded`):
`packages/backend/convex/{auth.ts, auth.config.ts, convex.config.ts, access.ts, organizations.ts,
projects.ts (founding), privateData.ts, healthCheck.ts, http.ts, otlp.ts (route only), schema.ts}`,
`packages/backend/convex/betterAuth/*`, the installed libraries `better-auth@1.6.17`,
`@convex-dev/better-auth@0.12.5`, `convex-helpers@0.1.124` (cors), `@better-auth/utils@0.4.1`
(password hashing), web `apps/web/src/{lib/auth-client.ts, lib/config.ts, main.tsx,
components/{auth-forms,sign-in-form,sign-up-form,invite-dialog}.tsx, components/canvas/account-menu.tsx,
routes/{index,invite.$invitationId}.tsx, routes/_auth/{route,device}.tsx}`, CLI
`apps/cli/internal/{keel/{auth,api,types}.go, convex/convex.go, cli/{auth,context}.go, config/config.go,
output/output.go}` and tests, `.github/workflows/ci.yml` (the auth smoke test), `install.sh`,
`deploy/functions-entrypoint.sh`, `apps/web/docker-entrypoint.sh`, `apps/worker/src/controlPlane.ts`,
`apps/proxy/keel.go`.

Conventions in this file:

- "Current" = what the TypeScript/Convex system does today. "Go" = a recommendation for the port,
  aligned with `docs/go/ARCHITECTURE.md`. Where they differ the spec says so explicitly.
- Strings in `code font` inside quotes are exact and load-bearing (UI shows them; the CLI matches
  some of them). Do not reword.
- Times are unix milliseconds unless stated.

---

## 1. Model in one page

- **One organization per install.** It is never created by a user through Better Auth
  (`allowUserToCreateOrganization: false`). The first signed-in account founds it lazily
  (`projects.ensureDefault` or `projects.create` → `joinOrFound`). Name `"Default"`, slug `"default"`.
  The founder's role is `"owner"`.
- **Sign-up is open only until the first account exists.** Every later sign-up must carry a
  pending, unexpired invitation id whose email equals the sign-up email (case-insensitive, trimmed).
  The new account joins the invitation's organization with the invitation's role (UI always sends
  `"member"`), and the invitation flips to `"accepted"`.
- **Every member has the same access to Keel resources.** No Keel function looks at the role. The
  role only matters to Better Auth's own organization endpoints (only `owner`/`admin` may create
  invitations). Access rule for every resource: "the resource's project belongs to the caller's
  organization".
- **A user has at most one membership** (assumed everywhere; lookups use "first member row with
  `userId`").
- **Sessions** are Better Auth sessions (opaque 32-char token, 7-day expiry, rolling refresh).
  Browsers keep the session cookie in localStorage (cross-domain plugin) and get a 15-minute RS256
  JWT that the Convex client presents to Convex; Convex functions resolve the user from that JWT
  plus a live session row. The CLI holds the raw session token, swaps it for a JWT each run, and
  calls Convex's HTTP API.
- **CLI login** is RFC 8628 device authorization (`client_id` `keel-cli`); a signed-in member
  approves on the dashboard's `/device` page.
- **Machine routes** (`/worker/*`, `/proxy/events`) use one shared bearer `KEEL_WORKER_TOKEN`
  compared in constant time. `/otlp/v1/traces` uses per-environment ingest keys.

---

## 2. Environment variables (this area)

| Name | Read by | Meaning | Default / required |
| --- | --- | --- | --- |
| `SITE_URL` | `auth.ts` | Dashboard origin as users open it, e.g. `http://100.64.0.1` (port appended only when not 80). It is Better Auth's only trusted origin (`trustedOrigins: [SITE_URL]`), the CORS allow-list of `/api/auth/*`, the device `verificationUri` base (`${SITE_URL}/device`), and the cross-domain plugin's `siteUrl` (relative callback URLs are resolved against it). | Required. `install.sh` sets `SITE_URL="http://$KEEL_ADDR"` (+`:$KEEL_WEB_PORT` when ≠80); `functions-entrypoint.sh deploy` refuses to run without it. |
| `CONVEX_SITE_URL` | `auth.ts` (`baseURL`), convex plugin, auth config | Convex HTTP-actions origin (`http://$KEEL_ADDR:3211`). Better Auth `baseURL` (so auth lives at `${CONVEX_SITE_URL}/api/auth/*`), JWT `iss`, JWKS URL. Built into Convex from `CONVEX_SITE_ORIGIN`. | Provided by Convex. |
| `BETTER_AUTH_SECRET` | Better Auth (implicit) | HMAC key for signed session cookies (`<token>.<sig>`), the bearer plugin's signature check, encryption of JWKS private keys. | Required. `install.sh`: 32 random bytes as 64 hex chars, persisted and reused on upgrade. |
| `KEEL_WORKER_TOKEN` | `http.ts` | Bearer for `POST /worker/events`, `GET /worker/config`, `POST /proxy/events`. Unset → every one of those requests is 401. | Required (install: 64 hex chars). |
| `NODE_ENV` | Better Auth | `production` enables the built-in in-memory rate limiter (see §4.6). | Convex runtime decides. |

Not in this area but on the same router: `KEEL_PUBLIC_IP`, `KEEL_ACME_EMAIL`, `KEEL_OTLP_URL`, etc.

Go (`docs/go/ARCHITECTURE.md`): `KEEL_SITE_URL` replaces `SITE_URL`; `BETTER_AUTH_SECRET` is only
needed if the port keeps signed cookies or must verify old signed tokens (it does not need to: the
stored session token is enough, see §13). `CONVEX_SITE_URL` disappears (same origin).

---

## 3. Data

### 3.1 Better Auth component tables (`convex/betterAuth/generatedSchema.ts` + `schema.ts`)

These live in the Convex component `betterAuth` (local install), not in the app schema. App code
reads them through `components.betterAuth.adapter.{findOne,findMany,create,...}` with `where`
clauses; the adapter maps the field `id` to Convex `_id`. Every row also has Convex system fields
`_id` (string id, the Better Auth `id`) and `_creationTime` (ms float). Dates are stored as ms
numbers. "opt" = `v.optional(v.union(v.null(), T))` (absent or null).

#### `user`

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | Display name (sign-up form requires ≥2 chars client-side; server accepts any string). |
| `email` | string | Lower-cased by Better Auth on sign-up (`email.toLowerCase()`); lookups lower-case too. |
| `emailVerified` | bool | Always `false` in Keel (no verification flow). Matters: see §7.4 (accept-invitation quirk). |
| `image` | opt string | Unused. |
| `createdAt`, `updatedAt` | number | ms. |
| `userId` | opt string | Added by the convex plugin schema (legacy app-user link). Unused. |

Indexes: `email_name [email,name]`, `name [name]`, `userId [userId]`.

#### `session`

| Field | Type | Meaning |
| --- | --- | --- |
| `token` | string | The session token: `generateId(32)`, 32 chars `[a-zA-Z0-9]`. What the CLI stores and sends as `Bearer`; cookie value is `token + "." + base64url(HMAC-SHA256(BETTER_AUTH_SECRET, token))`. |
| `expiresAt` | number | ms. Created at now+7d (now+1d if `rememberMe:false`, never sent by Keel). |
| `createdAt`, `updatedAt` | number | ms. |
| `ipAddress`, `userAgent` | opt string | Request metadata at creation. |
| `userId` | string | `user._id`. |
| `activeOrganizationId` | opt string | Set by Keel's `session.create.before` hook to the user's membership org id, else null (§6.1). Read only by Better Auth's org endpoints as a default; Keel code never reads it. Not updated when the founder founds the org afterwards. |

Indexes: `expiresAt`, `expiresAt_userId`, `token`, `userId`.

#### `account`

One row per credential. Keel only has `providerId: "credential"`, `accountId = user id`,
`password` = hash (§4.5). Other columns (`accessToken`, `refreshToken`, `idToken`,
`accessTokenExpiresAt`, `refreshTokenExpiresAt`, `scope`) are unused (OAuth). `userId`,
`createdAt`, `updatedAt`. Indexes: `accountId`, `accountId_providerId`, `providerId_userId`, `userId`.

#### `verification`

`identifier` string, `value` string, `expiresAt`, `createdAt`, `updatedAt`. Used only by the
cross-domain one-time-token flow (OAuth redirects, not used by Keel) and Better Auth internals.
Indexes: `expiresAt`, `identifier`. Not needed in Go.

#### `organization`

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | `"Default"` (only value ever written; nothing renames it). |
| `slug` | string | `"default"`. Used by logSinks to name Axiom datasets (`keel-<slug>`). |
| `logo`, `metadata` | opt string | Unused. |
| `createdAt` | number | ms. |

Indexes: `name`, `slug`. Exactly zero or one row per install.

#### `member`

| Field | Type | Meaning |
| --- | --- | --- |
| `organizationId` | string | `organization._id`. |
| `userId` | string | `user._id`. |
| `role` | string | `"owner"` (founder) or the invitation's role (`"member"` from the UI). Better Auth also knows `"admin"`; a comma list is legal in Better Auth but never produced. |
| `createdAt` | number | ms. |

Indexes: `organizationId`, `userId`, `role`, `organizationId_userId [organizationId,userId]`.
Invariant (not enforced by a unique index): at most one row per user.

#### `invitation`

| Field | Type | Meaning |
| --- | --- | --- |
| `organizationId` | string | Target org. |
| `email` | string | Lower-cased by `invite-member`. |
| `role` | opt string | Role to grant; `"member"` from the UI. Null → `"member"` in Keel's sign-up hook. |
| `status` | string | `"pending"` → `"accepted"` \| `"canceled"` (re-invite) \| `"rejected"` (reject endpoint, unused). Never deleted. |
| `expiresAt` | number | createdAt + 7 days (`INVITATION_TTL_S = 604800`). |
| `createdAt` | number | ms. |
| `inviterId` | string | `user._id` of the inviter. |

> **Go now:** a member's or invitation's role is exactly one of `owner`, `admin`, `member` (`members.role` CHECK, typed `domain.Role`), never a comma list; an invitation's role defaults to `member` in the schema, so no code falls back; invitation status is `pending`, `accepted` or `canceled` (no `rejected`); email columns are `COLLATE NOCASE`.

Indexes: `organizationId`, `email`, `role`, `status`, `inviterId`, `organizationId_status`,
`email_organizationId_status`. **The invitation `_id` is the secret in the invite link**
(`<web origin>/invite/<id>`). Convex ids are not guessable in practice; the Go port must use ≥128
bits of randomness for invitation ids (the architecture's 20-char base32 `NewID` is 100 bits: use a
longer id or a separate random token for invitations).

#### `deviceCode`

| Field | Type | Meaning |
| --- | --- | --- |
| `deviceCode` | string | 40 chars `[a-zA-Z0-9]`. Secret held by the CLI. |
| `userCode` | string | 8 chars from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` (`byte % 32` of random bytes). Shown as `ABCD-EFGH`. |
| `userId` | opt string | Null until a signed-in user opens `GET /device?user_code=…` (binding). Only that user may approve/deny. |
| `expiresAt` | number | now + 30 min. |
| `status` | string | `"pending"` \| `"approved"` \| `"denied"`. |
| `lastPolledAt` | opt number | Last `/device/token` poll. |
| `pollingInterval` | opt number | 5000 (ms). |
| `clientId` | opt string | `"keel-cli"`. |
| `scope` | opt string | Unused (CLI sends none). |

Indexes: `deviceCode`, `userCode`. Rows are deleted when consumed (token issued), denied-and-polled,
or expired-and-polled.

> **Go now:** `device_codes` keeps `device_code_hash` (SHA-256; the code is `NewSecret(25)`, 40 base32 chars), `user_code` (`NewUserCode`, base32 over the alphabet above), `status`, `user_id`, `last_polled_at` (`NOT NULL DEFAULT 0`, 0 = never polled), `expires_at`, `created_at`. No `clientId`, `pollingInterval` or `scope`: the client is always `keel-cli` and the interval 5 s, checked against `domain.DeviceClientID` / `DeviceIntervalS`. Issuing a code purges those expired for over an hour.

#### `jwks`

`publicKey` string (JWK JSON), `privateKey` string (JWK JSON encrypted with `BETTER_AUTH_SECRET`),
`createdAt`, `expiresAt` opt. RS256 key pairs for the Convex JWT bridge (§4.3). Not needed in Go.

### 3.2 App-table fields tied to organizations (`convex/schema.ts`)

| Table.field | Type | Meaning |
| --- | --- | --- |
| `projects.organizationId` | opt string | Owning org (`organization._id`). Unset only on rows from before organizations; adopted by `ensureDefault` (§6.3). Index `by_organization [organizationId]`, `by_slug [organizationId, slug]`. |
| `projects.ownerId` | opt string | Legacy per-user owner (`user._id`). Cleared on adoption. |
| `logSinks.organizationId` | opt string | Sink per org (index `by_organization`). Legacy rows have `projectId` instead. |
| `axiomSignIns.organizationId`, `axiomPending.organizationId` | string | Sign-in-with-Axiom state is per org. |

### 3.3 Go storage (recommendation)

Tables `users`, `accounts` (or a `password_hash` column on users), `sessions`, `organizations`,
`members` (UNIQUE(user_id) — makes "one membership" an invariant), `invitations`, `device_codes`.
Drop `verification`, `jwks`. Keep `sessions.token` UNIQUE and indexed; store it hashed
(SHA-256) if you want, but then existing tokens must be imported hashed the same way. An install
row (or `organizations` with a CHECK that allows one row) makes founding race-free (§6.3).

---

## 4. Authentication mechanics (current)

### 4.1 Where Better Auth lives

- `authComponent.registerRoutes(http, createAuth, { cors: true })` mounts Better Auth at
  `${CONVEX_SITE_URL}/api/auth/*` for `GET` and `POST` (prefix routes), plus `OPTIONS` preflight,
  plus `GET /.well-known/openid-configuration` → `302` to
  `${CONVEX_SITE_URL}/api/auth/convex/.well-known/openid-configuration`.
- The web app is served from a **different origin** (`SITE_URL`, nginx) than Better Auth
  (`:3211`), hence the cross-domain plugin.
- Options (`createAuthOptions`): `baseURL: CONVEX_SITE_URL`, `trustedOrigins: [SITE_URL]`,
  `advanced.database.generateId: false` (Convex assigns ids; required or the org plugin mints
  invitation ids the component rejects), `emailAndPassword: { enabled: true,
  requireEmailVerification: false }`, `databaseHooks` (§6.1), plugins `organization` (§7),
  `deviceAuthorization` (§8), `crossDomain({ siteUrl })`, `convex({ authConfig,
  jwksRotateOnTokenGenerationError: true })` (which embeds `bearer` and `jwt`).

### 4.2 How a request carries the session

1. **Browser (cross-domain plugin).** Responses carry `Set-Better-Auth-Cookie` (the Set-Cookie
   value moved into a custom header; CORS exposes it). The client keeps a JSON map
   `{cookieName: {value, expires}}` in `localStorage["better-auth_cookie"]` and sends
   `Better-Auth-Cookie: better-auth.session_token=<token>.<sig>; better-auth.convex_jwt=<jwt>`
   with `credentials: "omit"`. The server's before-hook copies it into a `Cookie` header unless an
   `Authorization` header is present. The session cache lives in
   `localStorage["better-auth_session_data"]`. `/sign-out` clears both keys client-side.
2. **Bearer (bearer plugin, CLI and CI).** `Authorization: Bearer <token>` (scheme
   case-insensitive). If the token contains no `.`, the server signs it with the secret itself; if
   it does, it is used as the signed cookie value. The HMAC is verified, then a synthetic
   `Cookie: better-auth.session_token=<signed>` is set. Responses that set a session cookie also
   return `set-auth-token: <signed token>`.
3. **Cookie name.** `better-auth.session_token` (no `__Secure-` prefix: `baseURL` is http).

### 4.3 The Convex JWT bridge (convex plugin)

- `GET /api/auth/convex/token` (session required via `sessionMiddleware`; 401 `{"message":
  "Unauthorized","code":"UNAUTHORIZED"}` without one) → `200 {"token": "<JWT>"}` and sets cookie
  `better-auth.convex_jwt` (max-age 900). After-hooks also set that cookie on `/sign-in*`,
  `/sign-up*`, `/update-session`, `/get-session` (with a session); and clear it on `/sign-out`,
  `/delete-user`, `/get-session` (without one).
- JWT: header `alg: RS256`, `kid` = `jwks._id`. Claims: every user field except `id` and `image`
  (`name`, `email`, `emailVerified`, `createdAt`, `updatedAt`, …), `sessionId` = `session._id`,
  `iat`, `sub` = `user._id`, `iss` = `CONVEX_SITE_URL`, `aud` = `"convex"`, `exp` = iat + 900 s.
  Key pairs come from `jwks` (generated on first use; deleted and regenerated if signing fails with
  `ERR_JOSE_NOT_SUPPORTED`, because `jwksRotateOnTokenGenerationError: true`).
- Convex verifies it with the `customJwt` provider from `auth.config.ts`:
  `{ type: "customJwt", issuer: CONVEX_SITE_URL, applicationID: "convex", algorithm: "RS256",
  jwks: CONVEX_SITE_URL + "/api/auth/convex/jwks" }`.
- `GET /api/auth/convex/jwks` → `{ keys: [{ kid, kty, alg, n, e, … }] }`.
  `GET /api/auth/convex/.well-known/openid-configuration` → OIDC metadata (issuer, jwks_uri).
- **User resolution in every Convex function** (`authComponent.safeGetAuthUser`):
  1. `ctx.auth.getUserIdentity()` (the verified JWT) → none ⇒ signed out.
  2. `session` row with `_id == identity.sessionId` **and** `expiresAt > now` → none ⇒ signed out.
     (So sign-out takes effect on the next query even though the JWT lives 15 min.)
  3. `user` row with `_id == identity.subject` → none ⇒ signed out.
  4. Returns the full user doc `{_id, _creationTime, name, email, emailVerified, image?, createdAt,
     updatedAt, userId?}`.
- Actions propagate the caller's identity into `ctx.runQuery`/`ctx.runMutation`, so internal
  queries such as `nodesInternal.owned`, `logSinks.forNode`, `tracing.scope` apply the caller's
  access rules.

Go: no JWT. Resolve `Actor` directly from the session token (cookie `keel_session` or bearer) on
every request and on WebSocket connect, applying the same three checks (session exists, unexpired,
user exists).

### 4.4 Session lifetime

- `expiresIn` 7 days, `updateAge` 1 day: any session read (`get-session`, `convex/token`,
  `sessionMiddleware`) where `expiresAt − 7d + 1d ≤ now` pushes `expiresAt` to now + 7 d and
  re-sets the cookie. In query context (no mutation available) refresh is disabled.
- An expired session found on read is deleted and the cookie cleared.
- CLI sessions therefore last 7 days and renew while used (each run hits `/convex/token`).
- No cleanup cron; expired rows linger until touched.

### 4.5 Passwords (exact, for importing existing accounts)

- Length: min 8, max 128 characters (server); errors below.
- Hash format: `"<saltHex>:<keyHex>"`. `saltHex` = 16 random bytes as 32 lowercase hex chars. Key =
  `scrypt(P = NFKC(password) as UTF-8, S = saltHex **as its ASCII bytes (not hex-decoded)**,
  N = 16384, r = 16, p = 1, dkLen = 64)`, hex-encoded (128 chars). Verify by recomputing and
  comparing hex strings. Go: `scrypt.Key([]byte(norm.NFKC.String(pw)), []byte(saltHex), 16384, 16,
  1, 64)`; compare with `subtle.ConstantTimeCompare`.

> **Go now:** no scrypt and no import. `internal/adapters/password` hashes NFKC passwords with argon2id into a PHC string (`$argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>`, 16-byte salt, 32-byte key), verified in constant time; the length check counts runes (`utf8.RuneCountInString`), not UTF-16 units.

### 4.6 Origin / CSRF / CORS / rate limits

- **CORS on `/api/auth/*`** (convex-helpers `corsRouter`): allowed origins = `[SITE_URL]`;
  `Access-Control-Allow-Credentials: true`; allowed headers `Content-Type, Better-Auth-Cookie,
  Authorization`; exposed `Set-Better-Auth-Cookie`; `Vary: Origin`; preflight `204` with
  `Access-Control-Allow-Methods` (`GET` or `POST` per prefix) and `Access-Control-Max-Age: 86400`.
  Disallowed origins are **not** blocked (`enforceAllowOrigins: false`), they just get no
  `Access-Control-Allow-Origin`.
- **Origin check** (Better Auth, every non-GET/HEAD/OPTIONS request): if the request carries a
  `Cookie` (including one synthesized from `Better-Auth-Cookie` or, depending on hook order, a
  bearer), `Origin` (or `Referer`) must be present and match `SITE_URL`: else `403`
  `{"code":"MISSING_OR_NULL_ORIGIN","message":"Missing or null Origin"}` or
  `{"code":"INVALID_ORIGIN","message":"Invalid origin"}`. Sign-in/sign-up also validate `Origin`
  when one is sent, and block `Sec-Fetch-Site: cross-site` + `Sec-Fetch-Mode: navigate`. Any
  `callbackURL`/`redirectTo`/`errorCallbackURL`/`newUserCallbackURL` in a body must be a trusted
  origin or relative path.
- **The CLI and CI always send `Origin: <dashboard URL>`** (scheme://host[:port] as the user typed
  it) on every Better Auth call, precisely to satisfy this. Quirk: if the user logs in through a
  hostname other than `SITE_URL`'s (e.g. MagicDNS name vs IP), cookie-bearing POSTs fail with
  "Invalid origin".
- **Rate limit** (only when Better Auth considers itself in production): in-memory, per IP and
  path, 100 requests / 10 s by default; `/sign-in*`, `/sign-up*` 3 / 10 s. Response 429 with
  `X-Retry-After`. Best-effort in Convex's isolates.

Go: same-origin dashboard + API, so no CORS and no cross-domain header dance. Keep CSRF protection
for cookie-authenticated unsafe methods (check `Origin` against `KEEL_SITE_URL` or use
SameSite=Lax + a custom header requirement). Do not require `Origin` on bearer requests (no ambient
credential), but accept it. Add a modest login rate limit (e.g. 5 / min / IP on sign-in, sign-up,
device code).

> **Go now:** in-memory limiters on `App` (`internal/app/auth_limit.go`): sign-in 10 per 5 min per IP and email, sign-in and sign-up together 20 per min per IP, device code 10 per min per IP, device poll 60 per min per IP. A refusal is `429 RATE_LIMITED` with `Retry-After` (no `X-Retry-After`).

### 4.7 Better Auth error body shape

Errors are JSON `{"message": "...", "code": "..."}` with the HTTP status of the `APIError`
(`code` may be absent for hand-thrown errors). Device endpoints instead answer RFC 8628 style
`{"error": "...", "error_description": "..."}`. The web shows `error.message` (or
`error_description` on `/device`); the CLI reads `message` or `error_description`.

---

## 5. Better Auth HTTP endpoints Keel uses

All under `${CONVEX_SITE_URL}/api/auth`. "Session" = cookie or bearer as in §4.2.

| # | Method, path | Caller | Auth | Request | 200 response |
| --- | --- | --- | --- | --- | --- |
| 1 | `POST /sign-up/email` | web sign-up form, CI | none | `{email, password, name, invitationId?}` (+ optional `image`, `callbackURL`, `rememberMe`) | `{token, user}` and session cookie (auto sign-in) |
| 2 | `POST /sign-in/email` | web sign-in form, CI | none | `{email, password}` (+ `callbackURL?`, `rememberMe?`) | `{redirect: false, token, url: undefined, user}` + cookie |
| 3 | `POST /sign-out` | web (several buttons), `keel logout` | session (optional) | `{}` | `{success: true}`; deletes the session row if one was presented |
| 4 | `GET /get-session` | web `useSession` (ConvexBetterAuthProvider) | session | — | `{session, user}` or `null` |
| 5 | `GET /convex/token` | web provider (`fetchAccessToken`), CLI `Connect` | session | — | `{token}` (JWT) |
| 6 | `GET /convex/jwks` | Convex itself | none | — | JWKS |
| 7 | `POST /organization/invite-member` | web invite dialog | session | `{email, role: "member", organizationId}` | invitation object |
| 8 | `POST /organization/accept-invitation` | web invite page (signed in) | session | `{invitationId}` | `{invitation, member}` |
| 9 | `POST /device/code` | CLI `StartLogin` | none | `{client_id: "keel-cli"}` | `{device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval}` |
| 10 | `POST /device/token` | CLI `PollLogin` | none | `{grant_type: "urn:ietf:params:oauth:grant-type:device_code", device_code, client_id: "keel-cli"}` | `{access_token, token_type: "Bearer", expires_in, scope: ""}` |
| 11 | `GET /device?user_code=X` | web `/device` page, CI | session optional (binds when present) | query `user_code` | `{user_code, status}` |
| 12 | `POST /device/approve` | web `/device`, CI | session required | `{userCode}` | `{success: true}` |
| 13 | `POST /device/deny` | web `/device` | session required | `{userCode}` | `{success: true}` |

`user` JSON (Better Auth output): `{id, name, email, emailVerified, image, createdAt, updatedAt}`
(dates as ISO strings). `session` JSON: `{id, token, userId, expiresAt, createdAt, updatedAt,
ipAddress, userAgent, activeOrganizationId}`.

### 5.1 `POST /sign-up/email`

Order of checks (Better Auth 1.6.17 + Keel hooks):

1. Body schema (zod): missing/invalid fields → 400 `VALIDATION_ERROR`.
2. Email syntax → 400 `{"code":"INVALID_EMAIL","message":"Invalid email"}`.
3. Password missing/non-string → 400 `INVALID_PASSWORD`; `< 8` → 400 `{"code":"PASSWORD_TOO_SHORT",
   "message":"Password too short"}`; `> 128` → 400 `PASSWORD_TOO_LONG` "Password too long".
4. Email lower-cased. Existing user with that email → **422**
   `{"code":"USER_ALREADY_EXISTS_USE_ANOTHER_EMAIL","message":"User already exists. Use another email."}`
   (checked **before** Keel's hook, so a taken email never reveals the invite rule).
5. Password hashed (§4.5).
6. `user.create.before` hook (§6.1): may throw **403** `{"message":"Sign-up is by invitation. Ask
   a member for an invite link."}` (or 403 "Sign-up needs a request" when called without an HTTP
   endpoint context; unreachable from HTTP).
7. User row inserted (`emailVerified: false`), then `user.create.after` hook (§6.1).
8. `account` row `{providerId: "credential", accountId: user.id, password: hash}`.
9. Session created (passing through `session.create.before`), cookie set, response
   `{token: session.token, user}`.

Note: Better Auth wraps this in `runWithTransaction`, but the Convex adapter runs each adapter
call as its own mutation, so steps 7–9 are **not atomic** today (a crash between them can leave a
user without account/member). Go: one DB transaction for user + account + member + invitation
update + session.

### 5.2 `POST /sign-in/email`

1. Email syntax → 400 "Invalid email".
2. User by lower-cased email; missing user, missing credential account, or missing/wrong password →
   **401** `{"code":"INVALID_EMAIL_OR_PASSWORD","message":"Invalid email or password"}` (a dummy
   hash is computed on the miss paths to equalize timing).
3. Session created (hook sets `activeOrganizationId`), cookie set, returns
   `{redirect, token, url, user}`. CI asserts `.token` is present.

No organization requirement: a user without membership can sign in.

### 5.3 Device authorization endpoints (plugin config: `verificationUri: ${SITE_URL}/device`,
`validateClient: id => id === "keel-cli"`, defaults `expiresIn 30m`, `interval 5s`,
`deviceCodeLength 40`, `userCodeLength 8`)

`POST /device/code`

- `client_id !== "keel-cli"` → 400 `{"error":"invalid_client","error_description":"Invalid client ID"}`.
- Inserts `deviceCode {deviceCode, userCode, userId: null, expiresAt: now+30m, status: "pending",
  pollingInterval: 5000, clientId, scope}`.
- Response (header `Cache-Control: no-store`): `device_code`, `user_code` (8 chars, no dash),
  `verification_uri: "<SITE_URL>/device"`, `verification_uri_complete:
  "<SITE_URL>/device?user_code=<user_code>"`, `expires_in: 1800`, `interval: 5`.

`POST /device/token` (all errors 400 unless noted, body `{error, error_description}`)

1. `client_id` invalid → `invalid_grant` "Invalid client ID".
2. No row for `device_code` → `invalid_grant` "Invalid device code".
3. Row `clientId` ≠ `client_id` → `invalid_grant` "Client ID mismatch".
4. `lastPolledAt` set and `now − lastPolledAt < pollingInterval (5000 ms)` → `slow_down`
   "Polling too frequently" (**`lastPolledAt` is not updated in this case**).
5. Set `lastPolledAt = now`.
6. Expired → delete row, `expired_token` "Device code has expired".
7. `status == "pending"` → `authorization_pending` "Authorization pending".
8. `status == "denied"` → delete row, `access_denied` "Access denied".
9. `status == "approved"` and `userId` set → atomically consume (find-and-delete the row where
   `deviceCode` matches and `status == "approved"`); lost race → `invalid_grant` "Invalid device
   code". Load user (500 `server_error` "User not found" if gone), create a session for it (500
   `server_error` "Failed to create session" on failure). Response 200, headers
   `Cache-Control: no-store`, `Pragma: no-cache`: `{access_token: session.token, token_type:
   "Bearer", expires_in: <seconds to session expiry>, scope: ""}`. **The token is handed out once.**
10. Anything else → 500 `server_error` "Invalid device code status".

> **Go now:** there is no step 3 (codes only exist for `keel-cli`, and step 1 already checked the client), no stored interval (step 4 uses the 5 s constant; `last_polled_at` 0 never slows down), and step 9 has no lost-race or user-not-found branch: the poll runs in one write transaction (`BEGIN IMMEDIATE`) that deletes the approved code it read and issues the session, and `user_id` cascades on delete. Step 10 cannot happen (`DecidePoll` covers every status).

`GET /device?user_code=X` (no Origin check, GET)

- `user_code` with `-` removed (case is **not** normalised server-side; the web upper-cases and
  strips spaces/dashes before calling).
- Unknown → 400 `invalid_request` "Invalid user code". Expired → 400 `expired_token` "User code
  has expired".
- If the caller has a session, the row has no `userId` and `status == "pending"`: conditional
  update (`id` match, `status == "pending"`, `userId IS NULL`) sets `userId = caller`. This
  **binds the code to the first signed-in user who looks at it**.
- Response `{user_code: <as given>, status: <row status>}`.

`POST /device/approve` / `POST /device/deny` (body `{userCode}`)

- No session → 401 `unauthorized` "Authentication required".
- Unknown → 400 `invalid_request` "Invalid user code"; expired → 400 `expired_token` "User code has
  expired"; `status != "pending"` → 400 `invalid_request` "Device code already processed";
  `userId` null → 400 `invalid_request` "Device code has not been claimed by a verifying session;
  call `GET /device` with the `user_code` while signed in before approving or denying"; bound to
  another user → **403** `access_denied` "You are not authorized to approve this device
  authorization" / "You are not authorized to deny this device authorization".
- Sets `status = "approved"` / `"denied"`, `userId = caller`. Returns `{success: true}`.

Note: approval does **not** require the approver to be in an organization. The CLI then gets a
session for that account, organization or not (founder gotcha, §6.4).

### 5.4 Exposed but unused Better Auth endpoints (do not port)

Core: `/ok` (GET `{ok:true}`), `/error`, `/update-session`, `/list-sessions`, `/revoke-session`,
`/revoke-sessions`, `/revoke-other-sessions`, `/update-user`, `/change-password`, `/change-email`,
`/delete-user`, `/delete-user/callback`, `/verify-password`, `/request-password-reset`,
`/reset-password`, `/reset-password/:token`, `/send-verification-email`, `/verify-email`,
`/sign-in/social`, `/callback/:id`, `/link-social`, `/unlink-account`, `/list-accounts`,
`/account-info`, `/get-access-token`, `/refresh-token`. Cross-domain:
`/cross-domain/one-time-token/verify` (OAuth only). Organization: `/organization/create` (always
403 "You are not allowed to create a new organization"), `check-slug`, `update`, `delete`,
`get-full-organization`, `set-active`, `list`, `remove-member`, `update-member-role`,
`get-active-member`, `get-active-member-role`, `leave`, `list-members`, `reject-invitation`,
`cancel-invitation`, `get-invitation`, `list-invitations`, `list-user-invitations`. (Teams and
dynamic roles are disabled.) Today an owner could remove members or cancel invitations only via
these raw endpoints; nothing in the UI or CLI does.

---

## 6. Sign-up, founding, joining

### 6.1 Database hooks (`convex/auth.ts`)

```ts
user.create.before(user, endpoint):
  adapter = endpoint?.context.adapter
  if !adapter: throw APIError("FORBIDDEN", "Sign-up needs a request")
  if no row in `user` (findMany limit 1): return            // first account: allowed
  id = endpoint.body.invitationId if non-empty string else null
  inv = id ? pendingInvitation(id) : null                     // see below
  if !inv || !sameEmail(inv.email, user.email):
     throw APIError("FORBIDDEN", "Sign-up is by invitation. Ask a member for an invite link.")

user.create.after(user, endpoint):
  id = endpoint.body.invitationId (non-empty string) ; if !adapter || !id: return
  inv = pendingInvitation(id); if !inv || !sameEmail(inv.email, user.email): return
  update invitation id=inv.id set status = "accepted"
  create member { organizationId: inv.organizationId, userId: user.id,
                  role: inv.role ?? "member", createdAt: now }

session.create.before(session, endpoint):
  member = first member where userId = session.userId
  return { data: { ...session, activeOrganizationId: member?.organizationId ?? null } }

pendingInvitation(id): row = invitation by id (malformed id → null);
  null unless row.status == "pending" and row.expiresAt >= now
sameEmail(a, b) = a.trim().toLowerCase() == b.trim().toLowerCase()
```

Consequences:

- The first account may send an `invitationId` (ignored by `before`); `after` still honours a
  valid one (only possible if an org and invitation already exist, e.g. after users were deleted).
- `after` does not check the org's membership limit or the inviter's current membership.
- The member row exists before the session is created, so an invited user's first session already
  has `activeOrganizationId`.
- The founder's sign-up session has `activeOrganizationId: null` forever (not updated on founding).

> **Go now:** one `SignUp` write does it all (`signUpRule`, then the user, then `foundOrganization` for the first account or `joinWithInvitation`, then the session). Emails are normalized once at the edge (`NormalizeUserEmail`) and compared as is, so there is no `sameEmail`; the invitation's role is never empty, so there is no `?? "member"`.

### 6.2 `auth.signUpOpen` (public query)

Args `{}`. Returns `true` iff the `user` table is empty. No auth. The web shows the sign-up form
(title "Lay the keel") only while this is `true` (or on an invite link).

### 6.3 Founding: `joinOrFound` (`projects.ts`, used by `projects.ensureDefault` and
`projects.create`)

```ts
user = requireUser(ctx)                      // ConvexError("Not authenticated")
m = currentMembership(ctx); if m: return m
if any organization row exists: throw ConvexError(NO_ORGANIZATION)
org = create organization { name: "Default", slug: "default", createdAt: now }
create member { organizationId: org._id, userId: user._id, role: "owner", createdAt: now }
return { user, organizationId: org._id, role: "owner" }
```

`NO_ORGANIZATION` = `"You're not in an organization yet. Ask a member for an invite link."`

Runs inside a Convex mutation (serializable, retried on conflict), so two concurrent founders
cannot create two orgs: the loser re-runs, sees the org, and gets `NO_ORGANIZATION`. Go: do the
"org exists?" check and both inserts in one write transaction (single writer / `BEGIN
IMMEDIATE`), or rely on a unique "one org" constraint.

**`projects.ensureDefault`** (public mutation, args `{}`, returns the project slug string). Called
by the web's `/` route (`Bootstrap`) on every visit while signed in; on success navigates to
`/p/<slug>`; on error shows the message and a "Sign out" button.

1. `membership = joinOrFound()`.
2. Legacy adoption (installs from before organizations): `legacy` = projects with
   `organizationId` unset; `own` = projects of `membership.organizationId`. `taken` = slugs of
   `own`. Sort `legacy` so rows with `ownerId == user._id` come first. For each: `slug =
   uniqueName(p.slug, taken)` (base, else `base-2`, `base-3`, …), add to `taken`, patch
   `{organizationId, ownerId: undefined, slug, name: slug == p.slug ? p.name : slug}`.
3. `existing = legacy.find(ownerId == user._id) ?? own[0] ?? legacy[0]`; if any → return its
   (possibly renamed) slug.
4. Else insert project `{name: "acme-support", slug: "acme-support", organizationId}` plus
   environment `{projectId, name: "production", isProduction: true}`; return `"acme-support"`.

So any member (not only the founder) who opens `/` in an org with zero projects creates the
default project.

**`projects.create`** founds too (so `keel project create` works on a fresh install), then
validates the name (details belong to the projects spec): trim; `""` or `> 60` →
`"Project name: 1–60 characters"`; slug = NFKD, strip combining marks, lower-case, runs of
`[^a-z0-9]` → `-`, cut to 40, trim `-`; empty → `"Project name needs a letter or digit (a-z,
0-9)"`; taken in org → `` `Project "${slug}" already exists` ``.

**`projects.list`** special case: no membership → `requireUser` (signed out ⇒ `"Not
authenticated"`); if an org exists ⇒ throw `NO_ORGANIZATION`; else (nothing founded yet) return
`[]`.

> **Go now:** the first account founds the organization when it signs up (§6.1), so there is no `joinOrFound`, no legacy adoption of org-less projects and no `projects.list` special case: every project use case requires a membership (`RequireMember`: signed out `Not authenticated`, else `NO_ORGANIZATION`). `EnsureDefaultProject` only makes `acme-support` when the organization has no project.

### 6.4 The "founder without org" gotcha

On a fresh install the first account is often created from a `keel login` link: the CLI prints
`<SITE_URL>/device?user_code=…`, the `_auth` layout shows sign-up (since `signUpOpen`), the user
signs up and stays on `/device`, approves, and never visits `/`. `ensureDefault` never runs, so
**no organization exists** although an account does. Then:

- `organizations.current` → `null`; `keel login` / `keel whoami` warn "this account isn't in the
  install's organization yet; projects stay empty until a member invites it".
- `projects.list` → `[]` (not an error, because no org exists yet); `keel project create` founds
  the org (`joinOrFound`) and the CLI proceeds. CI covers exactly this
  (`keel project list` → `[]`, then `project create` succeeds).
- Every other org-scoped read returns `null`/`[]`, mutations throw "Environment not found" /
  "Node not found" / `NO_ORGANIZATION` as per §9.
- Meanwhile nobody else can join: sign-up needs an invitation and there is no org to invite into.

Go must keep both founding entry points (dashboard bootstrap and project create) or found the org
eagerly at first sign-up (simpler, removes the gotcha; if chosen, `GET /api/me` and
`listProjects` behave as if the founder always has a membership, and the CLI warning path becomes
unreachable but must still be supported for legacy accounts without membership).

> **Go now:** eager founding at the first sign-up, so the gotcha is gone. There are no legacy accounts to support.

### 6.5 Accounts without a membership after the org exists

Possible for: legacy users from before organizations (the first to sign in founded the org), and
the loser of a first-sign-up race. They can sign in; `/` shows `NO_ORGANIZATION` with a "Sign
out" button; the CLI maps it to `NO_ORGANIZATION`. Their way in is an invite link while signed in
(§7.3), which is currently broken (§7.4).

---

## 7. Organizations and invitations

### 7.1 Plugin config

`organization({ allowUserToCreateOrganization: false, invitationExpiresIn: 604800,
cancelPendingInvitationsOnReInvite: true, sendInvitationEmail: async () => {} })` with default
roles:

| Role | Better Auth permissions (relevant) |
| --- | --- |
| `owner` | `invitation: [create, cancel]`, `member: [create, update, delete]`, `organization: [update, delete]` |
| `admin` | `invitation: [create, cancel]`, `member: [create, update, delete]`, `organization: [update]` |
| `member` | none (`ac: [read]` only) |

`creatorRole` = `owner`: only a member whose role includes `owner` may invite someone **as**
`owner`. Defaults: `invitationLimit` 100 pending unexpired invitations per org,
`membershipLimit` 100 (checked on accept only).

### 7.2 `POST /organization/invite-member` (web: Account menu → "Invite people…")

Request from the web: `{ email: email.trim(), role: "member", organizationId: <organizations.current.id> }`.

1. Session required (401 otherwise). `organizationId = body.organizationId ||
   session.activeOrganizationId`; none → 400 "Organization not found".
2. `email` lower-cased; invalid → 400 "Invalid email".
3. Caller's member row in that org; none → 400 `{"code":"MEMBER_NOT_FOUND","message":"Member not found"}`.
4. Permission `invitation:create` on the caller's role; `member` role fails → **403**
   "You are not allowed to invite users to this organization". (The web shows the menu item to
   every member; members get this toast.)
5. Role must be a known role → 400 "Role not found: <roles>". Inviting as `owner` without being
   owner → 403 "You are not allowed to invite a user with this role".
6. A user with that email already a member of the org → 400 "User is already a member of this
   organization".
7. Pending unexpired invitation for (email, org) exists → since `cancelPendingInvitationsOnReInvite`
   it is set `status: "canceled"` (no "already invited" error).
8. Pending unexpired invitations in the org `>= 100` → 403 "Invitation limit reached".
9. Insert `{organizationId, email, role, status: "pending", expiresAt: now + 7 d, createdAt: now,
   inviterId: caller}`. `sendInvitationEmail` is a no-op (nothing is mailed).
10. Response: the invitation `{id, organizationId, email, role, status, expiresAt, createdAt,
    inviterId, …}`. The web builds the link `${window.location.origin}/invite/${data.id}` (the
    browser's origin, not `SITE_URL`) and shows it with a Copy button; dialog copy: "Send this link
    to them. It works once, for that email, for a week."

### 7.3 Invite link page `/invite/$invitationId` (public route)

- Reads `organizations.invitation({ id })`. `null` → "Invite not found" / "This invite link is
  unknown, already used or expired. Ask a member for a new one."
- Signed out → sign-up form with email prefilled and disabled, title `Join <org name>`, submit
  sends `invitationId` in the sign-up body (§5.1/§6.1). On success → `/` (Bootstrap → project).
- Signed in, same email (case-insensitive) → "Join" button → `POST
  /organization/accept-invitation {invitationId}` → on success `/`; on error toast
  `error.message ?? "Could not accept the invitation"`.
- Signed in, other email → "This invite is for X, but you are signed in as Y. Sign out to create
  that account." + Sign out.

### 7.4 `POST /organization/accept-invitation` (current behaviour, including a bug)

1. Session required. Invitation by id; missing, expired, or `status != "pending"` → 400
   "Invitation not found".
2. Invitation email ≠ session user email (lower-cased) → 403 "You are not the recipient of the
   invitation".
3. **Email verification gate.** Better Auth requires `user.emailVerified` for invitation-id actions
   unless invitation ids are generated by its built-in opaque generator. Keel sets
   `advanced.database.generateId: false`, so the gate is on, and `emailVerified` is always `false`
   in Keel. **Result: this endpoint always fails today with 403 "Email verification required
   before accepting or rejecting invitation".** The "Join" path for existing accounts does not
   work; only sign-up-with-invitation works.
4. (Would continue:) member count ≥ 100 → 403 "Organization membership limit reached"; set
   invitation `accepted` (conditional on `pending`); create member `{organizationId, userId, role:
   invitation.role}`; set the session's `activeOrganizationId`; on failure revert the invitation to
   `pending`. Response `{invitation, member}`.

Go: implement the intended behaviour (steps 1, 2, 4 without step 3): accept when the invitation is
pending, unexpired, and its email matches the signed-in user's; refuse with the same messages; also
refuse if the user already has a membership (one-membership invariant) with a clear message, e.g.
"You're already in an organization". The unguessable invitation id is the proof of possession.

### 7.5 `organizations.current` (public query)

Args `{}`. `membership = currentMembership()`; none → `null`. Load organization by
`membership.organizationId`; missing → `null`. Returns `{ id: org._id, name, slug, role:
membership.role }`. Used by the account menu (shows `"<name> · <role>"`, gates "Invite people…"),
Settings ("every project in <name>"), the CLI (`whoami`, login warning; JSON `{id, name, slug,
role}` printed as the CLI's `organization`). Also called by `logSinks.connectAxiom` and
`logSinks.provision` via `ctx.runQuery(api.organizations.current)` as a membership gate.

### 7.6 `organizations.invitation` (public query, no auth)

Args `{ id: string }`. Invitation by `_id` (malformed id → `null`); `null` unless `status ==
"pending"` and `expiresAt >= now`; organization by id, missing → `null`. Returns `{ email:
invitation.email, organization: org.name }`. Public on purpose: "the id is the secret in the link".

### 7.7 `auth.getCurrentUser` (public query)

Args `{}`. Returns `safeGetAuthUser()` (§4.3) or `null`. Shape: the raw user doc
`{_id, _creationTime, name, email, emailVerified, image?, createdAt, updatedAt, userId?}`. Web uses
`name`, `email`; the CLI reads `_id`, `email`, `name` and prints `{id, email, name}`; a `null` makes
the CLI say "Session expired or signed out" (`NOT_AUTHENTICATED`).

### 7.8 Template leftovers

- `privateData.get` (public query, args `{}`): `{message: "Not authenticated"}` when signed out,
  else `{message: "This is private"}`. No caller. Do not port.
- `healthCheck.get` (public query, no args): returns `"OK"`. No caller in web/CLI/install. Go:
  a plain `GET /api/health` → `200 "OK"` if wanted.

---

## 8. CLI authentication (current, `apps/cli`)

### 8.1 Discovery

`keel login <dashboard-url>`: URL normalised to `scheme://host[:port]` (http/https only; else usage
error `"<raw>" is not a dashboard URL (http:// or https:// and a host)`). `GET <url>/config.js`;
take the text between the first `{` and the last `}` as JSON `{convexUrl, convexSiteUrl}`. Non-200,
no braces or bad JSON → `DISCOVERY_FAILED` "`<url>` doesn't look like a Keel dashboard (no
/config.js)"; empty values → `DISCOVERY_FAILED` "`<url>`/config.js has no Convex URLs (a dev
server?)". `--convex-url` / `--convex-site-url` (or `KEEL_CONVEX_URL` / `KEEL_CONVEX_SITE_URL` with
`KEEL_URL`) skip discovery. `config.js` is written by the web container:
`window.__KEEL__ = {"convexUrl":"<url>","convexSiteUrl":"<url>"};` Keep the file and its shape
(CLAUDE.md); the Go architecture adds `GET /api/meta` for new CLIs.

> **Go now:** discovery is `GET <url>/api/meta` only (anything else is `DISCOVERY_FAILED`). There is no `/config.js`, no `--convex-url` / `--convex-site-url` and no `KEEL_CONVEX_*`.

### 8.2 Login state machine

Config file `~/.config/keel/config.json` (`$KEEL_CONFIG_DIR`, else `$XDG_CONFIG_HOME/keel`; 0600 in a
0700 dir, atomic rename). Per instance: `url`, `convexUrl`, `convexSiteUrl`, `email`, `token`
(raw session token), `pending {deviceCode, userCode, url, expiresAt, interval}`.

```
keel login:
  if inst.token: Connect(); NOT_AUTHENTICATED → drop token; ok → print loggedIn, done
  if inst.pending and not expired: PollLogin once
       NOT_AUTHENTICATED (expired/denied/used) → drop pending; got token → use it
  if no token and (no pending or expired): StartLogin → POST /api/auth/device/code {client_id}
       pending = {device_code, user_code, verification_uri_complete,
                  expiresAt = now + expires_in (UTC, truncated to s), interval = max(interval,1)}
  save config
  if no token:
     no TTY or --json or --no-wait (and not --wait): print pending result and exit 0
        {"ok":true,"status":"pending","instance","url","approvalUrl","code":"ABCD-EFGH","expiresAt","next"}
     else: poll every `interval` s (+5 s per slow_down) until token / error / Ctrl-C (CANCELLED)
  save token BEFORE anything else can fail (token is single-use), clear pending
  Connect() → whoami → save email → print {"status":"loggedIn","instance","url","user","organization"}
  organization == null → warning (§6.4)
```

`PollLogin` maps `/device/token` replies: 200 with `access_token` → token; `authorization_pending`
→ wait; `slow_down` → wait longer; `expired_token` → `NOT_AUTHENTICATED` "The login link expired
before anyone approved it"; `access_denied` → `NOT_AUTHENTICATED` "The login was denied in the
dashboard"; `invalid_grant` → `NOT_AUTHENTICATED` "The login link is used up or unknown"; other
`error_description` → `SERVER_ERROR` "/api/auth/device/token: <desc>"; else `SERVER_ERROR` "HTTP
<n>".

Every other command runs `finishLogin` first when the instance has a pending login and no token:
expired → `NOT_AUTHENTICATED` (no poll); poll once; on `slow_down` wait `interval` s and poll once
more; still pending → `AUTHORIZATION_PENDING` (exit 4) with fix "Open <url> and approve (agents:
send it to your human), then retry; or keel login --wait"; on error re-read the config file: if a
concurrent run saved a token, use it; if the code is gone (denied/expired/used) and still the same
`deviceCode`, drop `pending` from the file. Success → save token, progress line "Login approved;
saved for <name>".

### 8.3 Authenticated calls

1. `Connect`: no token → `NOT_AUTHENTICATED` "Not logged in". `GET
   <convexSiteUrl>/api/auth/convex/token` with `Authorization: Bearer <token>` and `Origin: <url>`
   → `{token: JWT}`. 401 → `NOT_AUTHENTICATED` "Session expired or signed out". JWTs are never
   cached (15-min life).
2. Each function call: `POST <convexUrl>/api/{query|mutation|action}` body
   `{"path":"module:function","args":{…},"format":"json"}` (nil args → `{}`), `Authorization:
   Bearer <JWT>`. Response `{"status":"success","value":…}` or `{"status":"error","errorMessage",
   "errorData"}`. Non-JSON + HTTP 401 → unauthenticated. `errorMessage` is cleaned (drop
   `[Request ID: …] `, `Server Error\n`, `Uncaught `, stack after `\n    at `).
3. Function paths the CLI calls: `auth:getCurrentUser`, `organizations:current`, `projects:list`,
   `projects:create`, `environments:summary`, `nodes:list`, `nodes:create`, `nodes:remove`,
   `variables:list`, `variables:set`, `variables:remove`, `deployments:start`,
   `deployments:latest`, `deployments:get`, `deployments:listForNode`, `logs:tail`,
   `traces:overview`, `tracing:forNode`, `tracing:enable`, `tracing:localEnv`, `tracing:prompt`.
4. `keel logout`: `POST /api/auth/sign-out` with the bearer (failure only warns), then forget the
   instance and its directory links. Refused when `KEEL_URL` is set.
5. `keel token` prints the raw session token (after `finishLogin`) for `KEEL_TOKEN`.
6. Env: `KEEL_URL` (+ `KEEL_TOKEN`, `KEEL_CONVEX_URL`, `KEEL_CONVEX_SITE_URL`) targets an install
   without the config file; `KEEL_TOKEN` overrides a stored token; `KEEL_INSTANCE` / `--instance`
   pick a saved one.

### 8.4 Error translation (CLI `translate`, keyed on `ConvexError` data strings)

| ConvexError message (exact) | CLI code | CLI message / fix |
| --- | --- | --- |
| `Not authenticated` | `NOT_AUTHENTICATED` (exit 4) | "Session expired or signed out" |
| starts with `You're not in an organization` | `NO_ORGANIZATION` | "This account isn't in the install's organization"; fix "Ask a member for an invite link (account menu → Invite people), then keel login <url>" |
| `A deployment is already running` | `DEPLOYMENT_RUNNING` | |
| `Nothing to ship` | `NOTHING_TO_SHIP` | |
| `Node not found` | `SERVICE_NOT_FOUND` | "Service not found" |
| `Connect Axiom to see traces` or `Sign in with Axiom again to turn on traces` | `TRACES_OFF` | message kept |
| `Environment not found` | `PROJECT_NOT_FOUND` | |
| `Project "<slug>" already exists` | `NAME_TAKEN` | fix "Pick another name, or use it: keel link <slug>" |
| ends with `" is already taken` | `NAME_TAKEN` | |
| any other non-empty string | `INVALID_INPUT` | message kept |
| no data (non-ConvexError throw) | `SERVER_ERROR` | |
| HTTP 401 from Convex | `NOT_AUTHENTICATED` | |

Exit codes: 0 ok, 1 error, 2 usage, 4 `NOT_AUTHENTICATED`/`AUTHORIZATION_PENDING`, 130 cancelled.
Go: the server returns the code directly (problem+json `code`), so the CLI no longer parses
messages, but **messages stay byte-identical** (ARCHITECTURE: `domain.Error.Message` = Convex
message) because the dashboard displays them.

---

## 9. Access control (`convex/access.ts`)

### 9.1 Helpers (exact rules)

| Helper | Rule | Returns / throws |
| --- | --- | --- |
| `requireUser(ctx)` | `safeGetAuthUser` (§4.3) | user doc, else throws `ConvexError("Not authenticated")` |
| `currentMembership(ctx)` | user (silent) → first `member` row with `userId == user._id` | `{user, organizationId, role}` or `null` (signed out **or** no membership; never throws) |
| `ownedProject(ctx, projectId)` | membership exists **and** project exists **and** `project.organizationId === membership.organizationId` | project or `null` (projects with unset `organizationId` are never owned) |
| `ownedEnvironment(ctx, envId)` | environment exists, then `ownedProject(environment.projectId)` | `{environment, project}` or `null` |
| `ownedNode(ctx, nodeId)` | node exists, then `ownedEnvironment(node.environmentId)` | `{node, environment, project}` or `null` |
| `requireEnvironment(ctx, envId)` | `ownedEnvironment` | scope, else throws `ConvexError("Environment not found")` |
| `requireNode(ctx, nodeId)` | `ownedNode` | scope, else throws `ConvexError("Node not found")` |
| `logSinks.requireOrganization(ctx)` (local) | `currentMembership` | `organizationId`, else throws `NO_ORGANIZATION` (also when signed out) |

Key property: **signed out, not a member, missing, and foreign are indistinguishable** for every
resource lookup ("not found"/`null`/`[]`). Only `requireUser`-based paths say "Not authenticated".
Go: keep this (no 403 that leaks existence; use 404 codes from §8.4's mapping: `PROJECT_NOT_FOUND`
for environments, `SERVICE_NOT_FOUND` for nodes), but signed-out requests to authenticated API
routes may short-circuit with 401 `NOT_AUTHENTICATED` in middleware, since today's web never calls
them signed out and the CLI maps both to re-login. Exception to preserve: endpoints that are
public today (§9.3).

Convex argument validators (`v.id("nodes")` etc.) reject malformed ids or ids of another table
before the handler with a non-`ConvexError` ("ArgumentValidationError"), which the CLI reports as
`SERVER_ERROR`. Go: treat an unknown/malformed id exactly like a foreign one.

### 9.2 Validators in `access.ts` (exact messages)

| Function | Rule | Error |
| --- | --- | --- |
| `validName(name)` | `/^[a-z0-9-]{1,40}$/` | `Name: 1–40 chars, a-z 0-9 and - only` |
| `validEnvKey(key)` | `/^[A-Z_][A-Z0-9_]{0,63}$/` | `Key: UPPER_SNAKE_CASE only` |
| `validImage(image)` | `/^[a-z0-9][a-z0-9._\-/:@]{0,199}$/` | `Image must look like repo/name:tag` |
| `validPort(port?)` | undefined, or integer 1–65535 | `Port must be 1–65535` |
| `validReplicas(n?)` | undefined, or integer 0–20 | `Replicas must be 0–20` |

(The `–` in these messages is U+2013 EN DASH.)

### 9.3 Guard of every public function

"Denied" covers signed out, no membership, missing and foreign alike unless stated.

| Function | Kind | Guard | When denied |
| --- | --- | --- | --- |
| `auth.getCurrentUser` | query | none | returns `null` |
| `auth.signUpOpen` | query | **public** | — |
| `organizations.current` | query | `currentMembership` | `null` |
| `organizations.invitation` | query | **public** (id is the secret) | `null` |
| `privateData.get`, `healthCheck.get` | query | none | (leftovers) |
| `projects.ensureDefault` | mutation | `joinOrFound` | `Not authenticated` / `NO_ORGANIZATION` |
| `projects.create` | mutation | `joinOrFound` | `Not authenticated` / `NO_ORGANIZATION` |
| `projects.getBySlug` | query | `currentMembership`, slug looked up inside the caller's org | `null` |
| `projects.list` | query | `currentMembership` | signed out: `Not authenticated`; org exists: `NO_ORGANIZATION`; no org yet: `[]` |
| `environments.summary` | query | `ownedEnvironment` | `null` |
| `nodes.list` | query | `ownedEnvironment` | `[]` |
| `nodes.create` | mutation | `requireEnvironment` | `Environment not found` |
| `nodes.move`, `rename`, `setDesired`, `stop`, `start`, `expose`, `unexpose`, `duplicate` | mutation | `requireNode` | `Node not found` |
| `nodes.remove` | mutation | `ownedNode` | silent no-op (idempotent multi-delete) |
| `nodes.publicAddress` | query | `requireUser` only (no org needed) | `Not authenticated` |
| `variables.list`, `variables.referenceable` | query | `ownedNode` | `[]` |
| `variables.set`, `variables.remove` | mutation | `requireNode` | `Node not found` |
| `deployments.start` | mutation | `requireEnvironment` | `Environment not found` |
| `deployments.latest` | query | `ownedEnvironment` | `null` |
| `deployments.get` | query | deployment's `ownedEnvironment` (arg is a free string, normalised) | `null` |
| `deployments.listForNode` | query | `ownedNode` | `[]` |
| `logs.tail` | action | `internal.logSinks.forNode` → `ownedNode` | `Node not found` |
| `logs.recent`, `logs.around` | action | `internal.logSinks.forEnvironment` → `ownedEnvironment` | `Environment not found` (then `Connect Axiom to search all logs` without a sink) |
| `traces.overview`, `traces.get`, `traces.around` | action | `forEnvironment` → `ownedEnvironment` | `Environment not found` (then sink errors); `overview` with a `nodeId` outside the env → `Node not found` |
| `tracing.forNode` | query | `ownedNode` (+ must be a service with `desired`) | `null` |
| `tracing.enable`, `tracing.localEnv` | action | `internal.tracing.scope` → `requireNode` | `Node not found`; non-service → `Only services can be traced` |
| `tracing.prompt` | query | **none required**: uses `ownedNode` / `ownedEnvironment` only to fill names | generic prompt without names |
| `logSinks.get`, `logSinks.pendingOrgs` | query | `currentMembership` | `null` |
| `logSinks.connectAxiom` | action | `organizations.current` non-null | `NO_ORGANIZATION` |
| `logSinks.disconnect` | mutation | `requireOrganization` | `NO_ORGANIZATION` |
| `logSinks.beginAxiomSignIn` | action | redirect URI validated, Axiom client registered (DCR) **before** any auth check; then `startSignIn` → `requireOrganization` | `NO_ORGANIZATION` (after a possible `axiomClients` insert: Go should check membership first) |
| `logSinks.signInAxiom` | action | `takeSignIn` returns null unless the `state` row's org = caller's org | `Axiom sign-in expired, try again` |
| `logSinks.chooseAxiomOrg` | action | `takePending` (caller's org) | `Sign-in expired, sign in with Axiom again` |
| `logSinks.cancelAxiomSignIn` | mutation | `currentMembership` | silent no-op |

Role is never checked by any Keel function: `owner` and `member` have identical Keel powers
(including Disconnect Axiom and deleting services). Only Better Auth's invite endpoint looks at
roles.

---

## 10. HTTP router (`convex/http.ts`)

All routes on `CONVEX_SITE_URL` (port 3211). Matching is exact path + method; unmatched → Convex
404.

| Method, path | Auth | Request | Responses |
| --- | --- | --- | --- |
| `GET`,`POST` `/api/auth/*`, `OPTIONS /api/auth/*` | per endpoint (§5) | per endpoint | per endpoint; CORS per §4.6 |
| `GET /.well-known/openid-configuration` | none | — | `302` → `${CONVEX_SITE_URL}/api/auth/convex/.well-known/openid-configuration` |
| `POST /worker/events` | worker bearer | Docker events: one JSON object, a JSON array (body starts with `[` after trim), or NDJSON (blank lines skipped). Header `X-Keel-Resync: 1` on the first batch after the worker (re)starts. | `401 "unauthorized"`; `413 "too large"` if body length (JS string length, i.e. UTF-16 units) `> 262144`; `400 "bad json"` if any line/array fails to parse or any element is not an object with `Type` and `Action`; else runs `internal.events.ingest({events, resync})` → `200 "ok"`. Plain-text bodies. |
| `GET /worker/config` | worker bearer | — | `401 "unauthorized"`; `200` JSON of `internal.worker.config` with `content-type: application/json`, `cache-control: no-store`. |
| `POST /proxy/events` | worker bearer | JSON `{event: "cert_obtained"\|"cert_failed", name: string, error?: string}` | `401`; `413 "too large"` (> 262144); `400 "bad json"`; `400 "bad report"` if `event` not one of the two or `name` not a string; else `internal.proxyInternal.certReport({event, name, error: string or undefined})` → `200 "ok"`. |
| `POST /otlp/v1/traces` | `Authorization: Bearer keel_otlp_<…>` (environment ingest key) | OTLP/HTTP, `application/x-protobuf` or `application/json` | `401 "unauthorized"` (no `Bearer `, key without prefix `keel_otlp_`, unknown key, or project without org); `415 "OTLP over HTTP: application/x-protobuf or application/json"`; `413 "too large"` (`Content-Length` or body > 4 MiB); no traces dataset → `200` empty (`""` protobuf / `{}` JSON); else relays to Axiom (details in the traces spec). |

**Worker bearer** (`authorized(req)`):

```ts
expected = env.KEEL_WORKER_TOKEN
header = req.headers.authorization ?? ""
if (!expected || !header.startsWith("Bearer ")) return false   // case-sensitive "Bearer "
return timingSafeEqual(header.slice(7).trim(), expected)       // byte-wise XOR over max length,
                                                               // length difference folded in
```

**Event trimming** (`trim(e)`) keeps only:
`{ type: String(e.Type), action: String(e.Action), name: e.Actor?.Attributes?.name if string,
serviceName: e.Actor?.Attributes["com.docker.swarm.service.name"] if string, time: e.time if
number }`. Everything else is dropped before `events.ingest`.

Callers: the worker (`apps/worker/src/controlPlane.ts`) sends `authorization: Bearer <token>`,
`content-type: application/json`, `x-keel-resync: 1|0`, 10 s timeout, retries 5xx/network forever
with backoff, skips 4xx. keel-proxy's `keel` event handler posts `/proxy/events` with
`Authorization: Bearer <token>`, up to 4 attempts, accepts any `< 300`. `install.sh` verifies the
token with `GET /worker/config` (expects 2xx).

Go (ARCHITECTURE): the same four raw routes keep their paths, auth, size limits, status codes
and plain-text bodies so deployed workers and proxies keep working across the switch. `/api/auth/*`
is replaced by the Go auth API (§13); `/.well-known/openid-configuration` and `/api/auth/convex/*`
are dropped.

> **Go now:** the paths and the plain-text bodies stay, but `POST /worker/events` takes only a JSON array (256 KiB counted in bytes; each element needs `Type`, not `Action`; trimmed to `{type, name, serviceName}`), the agent sends `X-Keel-Resync: 1` only when it wants a sweep, `/proxy/events` answers `400 bad report` for any body it cannot use (malformed JSON included), and the worker bearer is compared as SHA-256 digests in constant time. The agent and the proxy ship in the same image as `keel serve`, so nothing deployed needs the old shapes.

---

## 11. Web auth usage (what the UI depends on)

| Place | Calls | Behaviour |
| --- | --- | --- |
| `lib/config.ts` | `window.__KEEL__` from `/config.js`, else Vite env | Throws "`<name>` is not configured (window.__KEEL__ or .env)" when missing. |
| `main.tsx` | `ConvexBetterAuthProvider` | `isAuthenticated` = session present or cached JWT; JWT fetched via `/convex/token`; handles `?ott=` (OAuth, unused). |
| `routes/index.tsx` `/` | `<Authenticated>` → `projects.ensureDefault` → navigate `/p/<slug>`; error → message + "Sign out"; `<Unauthenticated>` → `AuthForms` | Bootstrap/founding entry point. |
| `routes/_auth/route.tsx` | gate for `/p/*`, `/device`, `/axiom/callback` | Signed out: `AuthForms` in place, URL kept (so `/device?user_code=` survives sign-in). |
| `components/auth-forms.tsx` | `auth.signUpOpen` | Sign-up only while open; default view sign-up when open, else sign-in; sign-in offers "Need an account? Sign up" only while open, else "Need an account? Ask a member for an invite link." |
| `sign-in-form.tsx` | `signIn.email({email, password})` | Client validation: email format "Invalid email address", password ≥ 8 "Password must be at least 8 characters". Toasts `error.message || statusText`; success toast "Sign in successful". |
| `sign-up-form.tsx` | `signUp.email({email, password, name, invitationId?})` | Client validation: name ≥ 2 "Name must be at least 2 characters", email, password ≥ 8. Success toast "Sign up successful". Titles "Lay the keel" / "Join <org>". |
| `account-menu.tsx` | `auth.getCurrentUser`, `organizations.current`, `signOut()` | Shows name, email, "<org> · <role>"; "Invite people…" when an org exists. |
| `invite-dialog.tsx` | `organization.inviteMember({email, role: "member", organizationId})` | Link = `${location.origin}/invite/${id}`. Errors toast `error.message ?? "Could not create the invite"`. |
| `routes/invite.$invitationId.tsx` | `organizations.invitation`, `getCurrentUser`, `organization.acceptInvitation` | §7.3. |
| `routes/_auth/device.tsx` | `getCurrentUser`, `GET /device`, `POST /device/approve|deny`, `signOut()` | Code input normalised: remove spaces and `-`, upper-case. On mount `GET /device?user_code=` (binds); error → "Link no longer valid". Shows "Sign in the keel CLI", the code as `ABCD-EFGH`, Approve/Deny; result screens "CLI signed in" / "Sign-in denied". Errors toast `error_description ?? message ?? statusText ?? "Something went wrong"`. |

---

## 12. Realtime (subscriptions in this area)

Reactive queries the web subscribes to (Convex `useQuery`) here, and the writes that must refresh
them. Coarse keys proposed for the Go WebSocket (`docs/go/ARCHITECTURE.md` publishes path-prefix
topics on a server-chosen channel; add a per-user channel because a signed-in user may have no org).

| Query (Go endpoint suggestion) | Read set | Invalidate on | Channel / topic |
| --- | --- | --- | --- |
| `auth.getCurrentUser` (`GET /api/me`) | session row (exists, unexpired), user row | sign-out / session deleted or expired → push and **close the socket** (client falls back to signed out); user updated | `session:<id>`, `user:<id>`; topic `/api/me` |
| `organizations.current` (`GET /api/me` or `/api/organization`) | caller's member row, organization row | member inserted for the user (founding, sign-up-with-invite, accept), member deleted/role changed, org renamed | `user:<userId>` and `org:<orgId>`; topic `/api/me`, `/api/organization` |
| `auth.signUpOpen` (`GET /api/auth/sign-up-open`, public) | existence of any user | first user inserted | no socket when signed out: refetch on window focus and after a 403 sign-up; optional public channel `install` |
| `organizations.invitation` (`GET /api/invitations/{id}`, public) | invitation row, organization row | invitation status/expiry change | refetch on focus; `invitation:<id>` if a socket exists |
| everything org-scoped (projects, nodes, sinks, …) | via membership | membership created → the user's whole view changes | on member insert publish "invalidate all" to `user:<userId>` and move the connection onto `org:<orgId>` (server-side subscription) |

Convex queries that use `Date.now()` (expiry checks) are not re-run as time passes; Go need not
push on expiry either (except closing sockets of expired sessions lazily on next use).

---

## 13. Go port: auth API recommendation

Everything below is a recommendation consistent with `docs/go/ARCHITECTURE.md` (Huma, `/api`,
problem+json with `code`, cookie `keel_session` HttpOnly SameSite=Lax Secure-on-https, or
`Authorization: Bearer <session token>`). Paths and names are the API spec owner's call; behaviour
must match §5–§9.

| Operation | Suggested route | Behaviour to preserve |
| --- | --- | --- |
| `getSignUpOpen` | `GET /api/auth/sign-up-open` → `{open}` | §6.2, public |
| `signUp` | `POST /api/auth/sign-up {email, password, name, invitationId?}` | §5.1 order and messages, §6.1 hooks, one transaction; sets cookie; returns `{user, token}` |
| `signIn` | `POST /api/auth/sign-in {email, password}` | §5.2; 401 "Invalid email or password"; sets cookie; returns `{user, token}` |
| `signOut` | `POST /api/auth/sign-out` | delete presented session; always 200 `{success: true}`; clear cookie; close its sockets |
| `getSession` | `GET /api/auth/session` | `{user, session}` or 401 |
| `getMe` | `GET /api/me` → `{user: {id, email, name}, organization: {id, name, slug, role} \| null}` | merges §7.7 and §7.5 (CLI `whoami` shape) |
| `getInvitation` | `GET /api/invitations/{id}` (public) → `{email, organization}` | §7.6; 404 instead of null |
| `createInvitation` | `POST /api/organization/invitations {email, role?}` | §7.2 rules and messages (owner/admin only, cancel previous pending, limit 100, 7-day TTL); return `{id, email, role, expiresAt}`; the web builds the link |
| `acceptInvitation` | `POST /api/invitations/{id}/accept` | §7.4 intended behaviour (no email-verified gate) |
| `ensureDefaultProject` | `POST /api/projects/default` → `{slug}` | §6.3 (founding + legacy adoption + default project) |
| Device flow | `POST /api/auth/device/code`, `POST /api/auth/device/token`, `GET /api/auth/device?user_code=`, `POST /api/auth/device/approve`, `POST /api/auth/device/deny` | §5.3 exactly, RFC 8628 bodies (`error`/`error_description`, `slow_down` rule, single-use token, binding on GET, approve/deny only by the bound user). `verification_uri` = `KEEL_SITE_URL + "/device"`. `client_id` must be `keel-cli`. |

Session tokens: keep 32-char `[a-zA-Z0-9]` tokens (or longer), 7-day expiry with 1-day rolling
refresh, lookup by token. Accept `Bearer <token>` with or without the legacy `.<signature>`
suffix (strip it) so tokens printed by `keel token` before the switch keep working if sessions are
imported. Password hashes import unchanged (§4.5). Member/organization/invitation rows import
unchanged (ids are opaque strings). Device codes and JWKS need no import.

> **Go now:** nothing is imported. A session token is `NewSecret(32)` (52 base32 chars); only its SHA-256 is stored (`sessions.token_hash`), and a bearer is used as given, with no `.<signature>` stripping.

CLI (Go, same binary): drop `/convex/token` and the Convex HTTP API; call `/api/*` with
`Authorization: Bearer <session token>`; keep the device flow, config file format, `KEEL_URL` /
`KEEL_TOKEN`, pending-login semantics (§8.2), output codes and exit codes unchanged. Map
server `code` directly; `Message` strings stay identical to the Convex ones listed in this file.

Known current defects the port should fix rather than copy (each listed above):
1. `accept-invitation` always 403s (email-verification gate) — §7.4.
2. Members see "Invite people…" but cannot invite (403) — §7.2 step 4. Either hide it for
   `member` or allow members to invite (product call; today's server rule is owner/admin only).
3. Sign-up user/account/member/session writes are not atomic — §5.1.
4. `beginAxiomSignIn` registers an Axiom OAuth client before checking membership — §9.3.
5. Founding is lazy and can be skipped entirely by the device-login path — §6.4.
