# Web data layer: porting spec

Scope: everything in `apps/web` that talks to the backend. Today that is Convex (`convex/react`
hooks over the self-hosted Convex deployment) plus Better Auth (through `@convex-dev/better-auth`).
Target (`docs/go/ARCHITECTURE.md`): the same React app calling a same-origin HTTP JSON API under
`/api` through Kubb-generated TanStack Query hooks, and one WebSocket (`/api/ws`) that pushes
`{"type":"invalidate","topics":[…]}` where a topic is a URL path prefix of query keys.

Read at worktree commit `36c2ded`: all of `apps/web/src` (83 files), `apps/web/{package.json,
vite.config.ts,index.html,public/config.js,docker-entrypoint.sh,nginx.conf,Dockerfile,.env.schema,
tsconfig.json}`, and the backend functions the web calls (to pin exact return shapes and error
strings). Backend semantics behind each function are owned by the sibling specs
(`projects.md` for projects/nodes/variables/deployments/realtime write list, `observability.md`
for logs/traces/sinks); this file pins what the web consumes and how to replace each call.

Endpoint paths and operationIds below are proposals that follow ARCHITECTURE.md conventions
(camelCase verb+noun, query key = resolved path). If an area spec fixes other names, those win;
the key/topic logic in section 10 stays the same.

---

## 1. Stack today

| Piece | Version / setting | Role in the data layer |
| --- | --- | --- |
| `convex` | catalog `^1.45.0` | `ConvexReactClient`, `useQuery`, `useMutation`, `useAction`, `<Authenticated>/<Unauthenticated>/<AuthLoading>`, `ConvexError` |
| `@convex-dev/better-auth` | catalog `^0.12.5` | `ConvexBetterAuthProvider` (Convex JWT from the Better Auth session), client plugins `convexClient()`, `crossDomainClient()` |
| `better-auth` | catalog `1.6.17` | `createAuthClient` + `organizationClient()`, `deviceAuthorizationClient()` |
| `@my-better-t-app/backend` | `workspace:*` | `api` object + `Id<>` + module types imported straight from `packages/backend/convex` |
| `@tanstack/react-router` | `^1.170.32`, file routes, `autoCodeSplitting` | Routing, search-param state (`?deployment`, `?view`, `?trace`, `?around`) |
| `@tanstack/react-form` + `zod` 4 | | Sign-in / sign-up forms (client validation only) |
| `@xyflow/react` | `^12.11.6` | Canvas; local node state fed from the `nodes.list` subscription |
| `sonner` | | Every failed mutation/action becomes `toast.error(message)` |
| `anser` | | ANSI parsing in a Web Worker (`lib/ansi.worker.ts`), no network |
| `varlock` + `@varlock/vite-integration` | `.env.schema` → generated `src/env.ts` (gitignored, produced by root `postinstall`) | `ENV.VITE_CONVEX_URL`, `ENV.VITE_CONVEX_SITE_URL` for dev |

Not present: no `@tanstack/react-query`, no `usePaginatedQuery`, no `useConvex()`, no `fetch`,
no `WebSocket`/`EventSource`, no router loaders or `beforeLoad`, no `React.StrictMode` (matters
for run-once effects, section 7). `localStorage` is used only indirectly (Better Auth
cross-domain plugin); `sessionStorage` only for the Axiom return slug.

Vite (`apps/web/vite.config.ts`): dev server `127.0.0.1:3001`, `allowedHosts: [".ts.net"]`
(reached through `tailscale serve`, Host header preserved), plugins in order: varlock
(`ssrInjectMode: "auto-load"`), tailwind, TanStack router (`target: "react", autoCodeSplitting:
true`), react. `resolve.tsconfigPaths: true`; `@/*` → `src/*`. No `server.proxy` today.

### 1.1 Routes

| URL | File | Auth gate | Search params (validated) | Backend calls |
| --- | --- | --- | --- | --- |
| `/` | `routes/index.tsx` | inline `<Authenticated>`/`<Unauthenticated>`/`<AuthLoading>` | none | `projects.ensureDefault` on mount, then `navigate("/p/$slug", replace)` |
| `/p/$projectId` (param is the **slug**) | `routes/_auth/p/$projectId.tsx` | `_auth` layout | `deployment?: string`; `view?: "observability" \| "settings"` (`"logs"` and `"traces"` are accepted and mapped to `"observability"`); `trace?: string`; `around?: number` (finite only; the router parses digits to a number) | `projects.getBySlug`, then the whole canvas |
| `/device` | `routes/_auth/device.tsx` | `_auth` layout | `user_code?: string` (normalized: strip spaces and `-`, uppercase) | Better Auth device endpoints + `auth.getCurrentUser` |
| `/axiom/callback` | `routes/_auth/axiom/callback.tsx` | `_auth` layout | `code?`, `state?`, `error?`, `error_description?` (strings only) | `logSinks.signInAxiom` |
| `/invite/$invitationId` | `routes/invite.$invitationId.tsx` | inline gates | none | `organizations.invitation`, `auth.getCurrentUser`, Better Auth sign-up / accept |

`_auth` (`routes/_auth/route.tsx`) is a pathless layout: authenticated → `<Outlet/>`, signed out →
`<AuthShell><AuthForms/></AuthShell>` (URL kept, so `/device?user_code=…` survives sign-in),
loading → full-screen `<Loader/>`.

Router (`main.tsx`): `createRouter({ routeTree, defaultPreload: "intent", scrollRestoration: true,
defaultPendingComponent: Loader, context: {}, Wrap })`, where `Wrap` puts
`<ConvexBetterAuthProvider client={convex} authClient={authClient}>` around the whole router.
`RouterAppContext` is empty: nothing auth-related flows through router context.

---

## 2. Bootstrap, `/config.js`, `lib/config.ts`

### 2.1 Today

1. `index.html` loads `<script src="/config.js"></script>` synchronously in `<head>`, before the
   module script `/src/main.tsx`.
2. `/config.js` defines `window.__KEEL__`:
   - Dev: `apps/web/public/config.js` = `window.__KEEL__ = {};` (Vite serves `public/` as-is).
   - Production image (`apps/web/Dockerfile`: static build on `nginx:1.29-alpine`):
     `docker-entrypoint.sh`, installed as `/docker-entrypoint.d/40-keel-config.sh`, runs before
     nginx starts. It requires `KEEL_CONVEX_URL` (Convex API origin, e.g. `http://100.64.0.1:3210`)
     and `KEEL_CONVEX_SITE_URL` (Convex HTTP actions origin, e.g. `http://100.64.0.1:3211`), exits 1
     with `40-keel-config: not an http(s) URL: <v>` unless each starts with `http://` or `https://`,
     and with `40-keel-config: invalid character in URL: <v>` if it contains `"`, `\`, `<`, `>` or
     a space. Then it writes exactly:
     `window.__KEEL__ = {"convexUrl":"<KEEL_CONVEX_URL>","convexSiteUrl":"<KEEL_CONVEX_SITE_URL>"};\n`
     to `/usr/share/nginx/html/config.js`.
   - `nginx.conf`: `location = /config.js { add_header Cache-Control "no-store"; }`,
     `/assets/` → `Cache-Control: public, max-age=31536000, immutable`, everything else
     `try_files $uri $uri/ /index.html` (SPA fallback), gzip on for text/css/json/js/svg.
3. `src/lib/config.ts` (module-load time):
   ```ts
   export const config = {
     convexUrl: required("convexUrl", window.__KEEL__?.convexUrl || ENV.VITE_CONVEX_URL),
     convexSiteUrl: required("convexSiteUrl", window.__KEEL__?.convexSiteUrl || ENV.VITE_CONVEX_SITE_URL),
   };
   // required() throws `${name} is not configured (window.__KEEL__ or .env)` → blank page.
   ```
   `ENV` comes from varlock (`.env.schema`: `VITE_CONVEX_URL` and `VITE_CONVEX_SITE_URL` are
   `@public @optional @type=url(matches="^(?!https?://example[.]convex[.])")`).
4. Consumers: `main.tsx:14` `new ConvexReactClient(config.convexUrl)`; `lib/auth-client.ts:8`
   `createAuthClient({ baseURL: config.convexSiteUrl, … })`. Nothing else reads it.
5. The CLI also reads `/config.js` (`apps/cli/internal/keel/auth.go` `Discover`): GETs
   `<dashboard>/config.js`, takes the bytes between the first `{` and the last `}`, JSON-decodes
   `{convexUrl, convexSiteUrl}`; non-200 or unparsable → `DISCOVERY_FAILED` "`<url>` doesn't look
   like a Keel dashboard (no /config.js)"; either field empty → "`<url>`/config.js has no Convex
   URLs (a dev server?)". CLAUDE.md: "keep that file's shape".

### 2.2 Target

- Dashboard, API and WebSocket share one origin (`keel serve`). The web needs **no** runtime URL:
  the generated client uses relative `/api/...`; the WebSocket URL is derived from
  `window.location` (`ws(s)://<host>/api/ws`). Delete `lib/config.ts`, `public/config.js`,
  `docker-entrypoint.sh`, `nginx.conf`, the nginx `Dockerfile` stage, and the two `VITE_CONVEX_*`
  entries in `.env.schema` (keep `NODE_ENV`). Remove the `<script src="/config.js">` tag from
  `index.html` once no web code reads `window.__KEEL__`.
- `keel serve` still answers `GET /config.js` with `Content-Type: application/javascript` and
  `Cache-Control: no-store` (ARCHITECTURE: "`GET /config.js` stays"). Its payload is a CLI
  contract decision (owned by the CLI spec): either keep the object shape with new fields (e.g.
  `window.__KEEL__ = {"apiUrl":"<KEEL_SITE_URL>"};`, which old CLIs reject with the clear "has no
  Convex URLs" error) or stop serving it once the new CLI discovers via `GET /api/meta`.
- Static assets: `keel serve` embeds `apps/web/dist` (`apps/web/embed.go`) and must reproduce the
  nginx rules: `/assets/*` immutable for a year, `index.html` and `/config.js` never cached, SPA
  fallback to `index.html` for any non-`/api`, non-file path.
- Dev: add to `vite.config.ts`
  `server.proxy = { "/api": { target: "http://127.0.0.1:8080", ws: true }, "/worker": …, "/otlp": …, "/config.js": … }`
  (ARCHITECTURE "Dev"). `ws: true` is required for `/api/ws`. With `public/config.js` deleted
  there is no shadowing question.

> **Go now:** `keel serve` serves no `/config.js` (the CLI discovers through `GET /api/meta`), and Vite proxies `/api`, `/worker`, `/otlp` and `/proxy` only.

---

## 3. How auth state reaches components

### 3.1 Today

1. **Client** (`lib/auth-client.ts`): `createAuthClient({ baseURL: config.convexSiteUrl, plugins:
   [convexClient(), crossDomainClient(), organizationClient(), deviceAuthorizationClient()] })`.
   Better Auth's routes live on the Convex HTTP-actions origin under `/api/auth/*`
   (`http.ts`: `authComponent.registerRoutes(http, createAuth, { cors: true })`).
2. **Cross-domain session storage** (`crossDomainClient`, because the dashboard origin differs
   from the Convex site origin): every auth fetch is sent with `credentials: "omit"` and header
   `Better-Auth-Cookie: <name=value; …>` built from `localStorage["better-auth_cookie"]` (a JSON
   map `{cookieName: {value, expires}}`, expired entries dropped). Responses carrying
   `Set-Better-Auth-Cookie` update that map; a changed `*.session_token` notifies the session
   signal. `GET /api/auth/get-session` responses are cached in
   `localStorage["better-auth_session_data"]`. `sign-out` clears both keys and sets the session
   atom to null before the request.
3. **Convex auth** (`ConvexBetterAuthProvider` → `ConvexProviderWithAuth`): `useAuth` is built from
   `authClient.useSession()` (`GET /api/auth/get-session`) and `fetchAccessToken`, which calls
   `authClient.convex.token()` (`GET /api/auth/convex/token` → `{ token }`, a Convex JWT the CLI
   README says lasts 15 minutes) and caches it. `isLoading = sessionPending && !cachedToken`,
   `isAuthenticated = !!session.session || cachedToken !== null`. Convex calls
   `fetchAccessToken({ forceRefreshToken: true })` before expiry. The provider also consumes a
   `?ott=` one-time token (cross-domain OAuth redirects); Keel has no social login, so this path
   is dead.
4. **Gates**: `<Authenticated>`, `<Unauthenticated>`, `<AuthLoading>` from `convex/react` reflect
   the Convex client's auth state (the backend accepted the JWT), used at
   `routes/_auth/route.tsx:15-27`, `routes/index.tsx:43-53`, `routes/invite.$invitationId.tsx:92-100`.
5. **Identity**: components never read the Better Auth session object. They call
   `useQuery(api.auth.getCurrentUser)` (better-auth user row via `authComponent.safeGetAuthUser`;
   fields used: `name`, `email`) in `account-menu.tsx:19`, `device.tsx:67`,
   `invite.$invitationId.tsx:29`.
6. **Active organization**: the web never reads `session.activeOrganizationId`
   (`useActiveOrganization` is unused). It calls `useQuery(api.organizations.current)` →
   `{ id, name, slug, role } | null` (`account-menu.tsx:20`, `settings.tsx:43`). The server
   resolves the organization from the caller's single `member` row (`access.currentMembership`;
   one organization per install). `InviteDialog` passes `organization.id` as `organizationId` to
   `authClient.organization.inviteMember`. Every other query/mutation is scoped server-side by that
   membership; the web passes only `environmentId` / `nodeId` / slug.
7. **Transitions**: sign-in success → toast "Sign in successful"; the session signal refetches
   `get-session`, Convex gets a token, `<Authenticated>` swaps the form for the page (same URL).
   Sign-up success → toast "Sign up successful" (Better Auth auto-signs in) and, from an invite,
   `navigate("/", replace)`. Sign-out (`authClient.signOut()`, four call sites) never navigates:
   storage is cleared, Convex drops auth, every gate flips to the sign-in form in place.
   On `/`, `Bootstrap` runs `projects.ensureDefault` (founds the org for the first account,
   errors with the NO_ORGANIZATION text for an account without membership).

### 3.2 Target

- Session = HttpOnly cookie `keel_session` set by the Go auth endpoints (same origin, no
  `localStorage`, no JWT, no `Better-Auth-Cookie` header). Delete `lib/auth-client.ts` and the
  `ConvexBetterAuthProvider` wrap.
- One query replaces `auth.getCurrentUser` + `organizations.current` + the Convex auth state:
  `GET /api/session` (`getSession`) →
  `{ "user": { "id", "name", "email" } | null, "organization": { "id", "name", "slug", "role" } | null }`.
  Always 200 (signed out = `user: null`), so it can drive the gate.
- Gate component (replaces the three `convex/react` components): `isPending` → `<AuthLoading>`
  UI; `data.user === null` → signed-out UI; else children. Provide `useSession()` returning the
  same data to `AccountMenu`, `ObservabilitySettings`, `Approve`, `Accept`.
- After sign-in, sign-up, sign-out, invitation accept: `await queryClient.resetQueries()` (drop
  every cached org-scoped answer) and reconnect the WebSocket (the server picks the org channel at
  connect time; a changed session or a new membership needs a new connection).
- Any API response with status 401 / code `NOT_AUTHENTICATED` (expired session) →
  `queryClient.setQueryData(["/api/session"], { user: null, organization: null })`, close the
  WebSocket. This mirrors Convex dropping auth and flipping the gates.
- Signed-out pages (`AuthForms`, `/invite/$id` sign-up) still need `GET /api/auth/sign-up-open`
  and `GET /api/invitations/{id}` without a session (public endpoints, no WebSocket).

---

## 4. Call-site inventory

Legend. **Live**: does the component rely on the value changing while mounted (Convex
subscription semantics)? `crit` = core UX breaks without it, `yes` = visibly stale otherwise,
`low` = changes rarely, `no` = one-shot. **Key** = target TanStack query key (first element = the
resolved request path). Return shapes are spelled out in section 5, error strings in section 6.

### 4.1 Reactive queries (`useQuery`)

Identical `(function, args)` pairs are one Convex subscription however many components use them
(`nodes.list` ×4 sites, `logSinks.get` ×2, `logSinks.pendingOrgs` ×2, `organizations.current`
×2, `auth.getCurrentUser` ×3). TanStack dedupes the same way by key.

| # | file:line | Convex function | Args | Result fields used | Live | Replacement (hook ← operationId, request, key) |
| --- | --- | --- | --- | --- | --- | --- |
| Q1 | `components/auth-forms.tsx:14` | `api.auth.signUpOpen` | none | the boolean: `undefined` → Loader; `true` → sign-up form first, plus "Need an account? Sign up" on sign-in; `false` → sign-in only | low | `useGetSignUpOpen` ← `GET /api/auth/sign-up-open` → `{open: boolean}`; key `["/api/auth/sign-up-open"]`; public, no topic; refetch on window focus |
| Q2 | `components/canvas/account-menu.tsx:19` | `api.auth.getCurrentUser` | none | `name` (fallback "…"), `email` | low | `useSession()` (`getSession`) `.user` |
| Q3 | `components/canvas/account-menu.tsx:20` | `api.organizations.current` | none | truthiness (shows org line, "Invite people…", mounts `InviteDialog`), `name`, `role` ("{name} · {role}"), `id` (→ `InviteDialog organizationId`) | low | `useSession().organization` |
| Q4 | `components/canvas/settings.tsx:42` | `api.logSinks.get` | `{}` | `undefined` → spinner; `kind === "axiom"`; `org` (optional label); `dataset`; `traces` (`null` → "None: this connection predates traces." + Sign in button) | yes | `useGetLogSink` ← `GET /api/organization/log-sink` → `{sink: LogSinkView \| null}`; key `["/api/organization/log-sink"]`; topic `/api/organization` |
| Q5 | `components/canvas/settings.tsx:43` | `api.organizations.current` | none | `name` (copy "every project in {name}") | low | `useSession().organization` |
| Q6 | `components/canvas/observability/page.tsx:17` | `api.logSinks.get` | `{}` | `undefined` → Loader; `kind !== "axiom"` → `AxiomGate`; else passes `{domain, dataset, traces, org}` as `Sink` to `Explorer` (which only reads `traces` truthiness) | yes (the gate swaps to the Explorer by itself when an org pick completes) | same hook as Q4 |
| Q7 | `components/canvas/observability/axiom-gate.tsx:37` (`TracesBanner`) | `api.logSinks.pendingOrgs` | `{}` | non-empty → renders the org picker card instead of the banner | yes | `useListPendingAxiomOrgs` ← `GET /api/organization/axiom/pending-orgs` → `{orgs: [{id, name}]}` (empty when no sign-in waits); key `["/api/organization/axiom/pending-orgs"]`; topic `/api/organization` |
| Q8 | `components/canvas/observability/axiom-gate.tsx:171` (`AxiomSignIn`) | `api.logSinks.pendingOrgs` | `{}` | non-empty `[{id, name}]` → picker buttons; `[]` → title/copy + Sign in button | yes | same as Q7 |
| Q9 | `components/canvas/observability/chrome.tsx:59` (`useServices`) | `api.nodes.list` | `{environmentId}` | array **order** (index `i` picks the tone `SERVICE_TONES[i % 6]`), `id`, `name` | yes | `useListNodes` ← `GET /api/environments/{environmentId}/nodes` → `NodeView[]`; key `["/api/environments/{id}/nodes"]`; topic `/api/environments/{id}` |
| Q10 | `components/canvas/topbar.tsx:25` (`ShipButton`) | `api.nodes.list` | `{environmentId}` | `id` only (set of alive node ids for Retry) | yes | same as Q9 |
| Q11 | `components/canvas/use-synced-graph.ts:14` | `api.nodes.list` | `{environmentId}` | everything `toCanvasNodes` maps (section 8) | crit | same as Q9 |
| Q12 | `components/canvas/use-data.ts:12` (`useSummary`, used by `topbar.tsx` ShipButton and `status-bar.tsx`) | `api.environments.summary` | `{environmentId}` | `pendingChanges` ("Ship · N change(s)"), `servers` ("N server(s)"); truthiness gates the servers label; `counts` unused | yes | `useGetEnvironmentSummary` ← `GET /api/environments/{id}/summary` → `{summary: {pendingChanges, counts, servers} \| null}`; key `["/api/environments/{id}/summary"]`; topics `/api/environments/{id}` and `/api/environments` (cluster) |
| Q13 | `components/canvas/use-data.ts:18` (`useLatestDeployment`, used by ShipButton) | `api.deployments.latest` | `{environmentId}` | via `toDeployment`: `status` (`running` → "Shipping… done/total" + elapsed; `failed` → Retry), `steps[].status`, `steps[].nodeId`, `steps.length`, `startedAt` | crit | `useGetLatestDeployment` ← `GET /api/environments/{id}/deployments/latest` → `{deployment: Deployment \| null}`; key `["/api/environments/{id}/deployments/latest"]`; topic `/api/environments/{id}` |
| Q14 | `components/canvas/use-deployment-link.ts:34` (`useLinkedDeployment`, used by `canvas.tsx` `Panel`) | `api.deployments.get` | `{id}` from `?deployment=`, or `"skip"` when absent | via `toDeployment`: `steps[].nodeId`; `undefined` while loading, `null` when missing / not owned / malformed id | low (the effect acts once per id) | `useGetDeployment` ← `GET /api/deployments/{id}` → `{deployment: Deployment \| null}` (malformed id → `null`, not 422); key `["/api/deployments/{id}"]`; `enabled: !!id`; topic `/api/deployments/{id}` |
| Q15 | `components/canvas/bottom-panel/tabs/deployments.tsx:226` | `api.deployments.listForNode` | `{nodeId}` | via `toDeployment` (each row): `id`, `message`, `status`, `startedAt`, `finishedAt`, `steps[]` (`nodeId`, `label`, `status`, `startedAt`, `finishedAt`), `log[]` (`at`, `nodeId`, `text`); first row = "current" card | crit (deploy log streams line by line, step icons move) | `useListNodeDeployments` ← `GET /api/nodes/{nodeId}/deployments` → `Deployment[]`; key `["/api/nodes/{id}/deployments"]`; topic `/api/nodes/{id}` |
| Q16 | `components/canvas/bottom-panel/tabs/networking.tsx:32` | `api.nodes.publicAddress` | none | the string or `null` (copy: "Served by the control plane (ip)…", "Point … at ip") | no (process env) | `useGetControlPlane` ← `GET /api/control-plane` → `{publicIp: string \| null}`; key `["/api/control-plane"]`; `staleTime: Infinity`, `meta.realtime = false` |
| Q17 | `components/canvas/bottom-panel/tabs/tracing.tsx:18` | `api.tracing.forNode` | `{nodeId}` | `undefined` → spinner; `null` → renders nothing; `enabled`; `traces` (`"off"`/`"old"`/`"on"` → note text, switch disabled when `traces !== "on" && !enabled`); `env[]` (`key`, `value`, `secret`, `overridden`) shown when enabled | yes (after the `enable` action, and on org sink changes) | `useGetNodeTracing` ← `GET /api/nodes/{id}/tracing` → `{tracing: TracingView \| null}`; key `["/api/nodes/{id}/tracing"]`; topic `/api/nodes/{id}` (+ `/api/nodes` on sink change) |
| Q18 | `components/canvas/bottom-panel/tabs/variables.tsx:322` | `api.variables.list` | `{nodeId}` | `undefined` vs `[]` (empty-state copy only when loaded and empty); per row `key`, `value`, `resolved`, `secret`, `resolvedSecret`, `parts[]` (`{text}` or `{ref: {node?, nodeId?, key, missing}}`) | yes | `useListVariables` ← `GET /api/nodes/{id}/variables` → `VariableView[]`; key `["/api/nodes/{id}/variables"]`; topic `/api/nodes/{id}` |
| Q19 | `components/canvas/bottom-panel/tabs/variables.tsx:323` | `api.variables.referenceable` | `{nodeId}` | `?? []`; per source `nodeId`, `name`, `type`, `image`, `keys[]` (`key`, `secret`, `provided`) and their **order** (first provided non-HOST/PORT key = the "Connect" suggestion) | yes | `useListReferenceableVariables` ← `GET /api/nodes/{id}/variables/referenceable` → `ReferenceSource[]`; key `["/api/nodes/{id}/variables/referenceable"]`; topic `/api/nodes/{id}` |
| Q20 | `components/canvas/copy-prompt.tsx:23` | `api.tracing.prompt` | `{nodeId?, environmentId?}` (the component gets one or the other) | the string (copied to clipboard); falsy → button disabled | no | `useGetTracingPrompt` ← `GET /api/tracing/prompt?nodeId=&environmentId=` → `{prompt: string}`; key `["/api/tracing/prompt", {nodeId, environmentId}]`; `meta.realtime = false`, refetch on mount |
| Q21 | `components/canvas/project-switcher.tsx:27` | `api.projects.list` | none | `undefined` → placeholder "Loading projects…"; per project `id`, `name`, `slug` (sorted client-side by `name.localeCompare`); `environments` unused | yes (projects created elsewhere appear) | `useListProjects` ← `GET /api/projects` → `ProjectSummary[]`; key `["/api/projects"]`; topic `/api/projects` |
| Q22 | `routes/_auth/device.tsx:67` | `api.auth.getCurrentUser` | none | `email`; `undefined` → Loader | low | `useSession().user` |
| Q23 | `routes/_auth/p/$projectId.tsx:45` | `api.projects.getBySlug` | `{slug}` | `undefined` → Loader; `null` → "Project “{slug}” not found." + link to `/`; `id`, `name`, `environment.id` (also the `Canvas` React `key`), `environment.name` | low | `useGetProjectBySlug` ← `GET /api/projects/by-slug/{slug}` → `{project: {...} \| null}`; key `["/api/projects/by-slug/{slug}"]`; topic `/api/projects` |
| Q24 | `routes/invite.$invitationId.tsx:29` (`Accept`) | `api.auth.getCurrentUser` | none | `email` (case-insensitive compare to `invitation.email`); `undefined` → Loader | low | `useSession().user` |
| Q25 | `routes/invite.$invitationId.tsx:71` | `api.organizations.invitation` | `{id}` | `undefined` → Loader; `null` → "Invite not found"; `email`, `organization` | no | `useGetInvitation` ← `GET /api/invitations/{id}` (public) → `{invitation: {email, organization} \| null}`; key `["/api/invitations/{id}"]`; no topic |

### 4.2 Mutations (`useMutation`)

Every call goes through `attempt()` (`components/canvas/errors.ts`) unless noted: on throw,
`toast.error(errorMessage(err))` and resolve `undefined`; on success resolve the return value.
Convex returns `null` (never `undefined`) for handlers that return nothing, which several call
sites rely on (`!== undefined` means success, see 9.4).

| # | file:line (hook / call) | Convex function | Args sent | Return used | On success | Replacement |
| --- | --- | --- | --- | --- | --- | --- |
| M1 | `routes/index.tsx:21` / `:25` | `api.projects.ensureDefault` | `{}` | slug `string` | `navigate({to: "/p/$projectId", params: {projectId: slug}, replace: true})`; on error (not via `attempt`) the page shows `errorMessage(err)` + a Sign out button | `useEnsureDefaultProject` ← `POST /api/projects/ensure-default` → `{slug}` |
| M2 | `components/canvas/project-switcher.tsx:28` / `:44` | `api.projects.create` | `{name}` (palette text, untrimmed; server trims) | `.slug` | `navigate("/p/$slug")` (keeps no search) | `useCreateProject` ← `POST /api/projects` `{name}` → `{id, name, slug, environments}` |
| M3 | `components/canvas/actions.tsx:65` / `:102` | `api.nodes.create` | `{environmentId, type, position: {x, y}, image?, engine?, deploy?}`; from the Add palette: service `{image?: text, deploy: true}`, nginx placeholder `{deploy: true}`, database/cache `{engine, deploy: true}`, volume `{}`; position = viewport centre at 1/3 height minus (110, 40), **rounded** (`toolbar.tsx:32-36`) | `{id, deploymentId?}` | `selectOnArrival.add(id)`; `deploymentId` → `link.open(id)` (sets `?deployment=`) | `useCreateNode` ← `POST /api/environments/{id}/nodes` → `{id, deploymentId: string \| null}` |
| M4 | `actions.tsx:66` / `:123` | `api.nodes.rename` | `{id, name}`; only sent when `name` matches `^[a-z0-9-]{1,40}$` and differs (`node-shell.tsx:69-88`) | none | (canvas updates by subscription) | `useRenameNode` ← `PATCH /api/nodes/{id}` `{name}` |
| M5 | `actions.tsx:67` / `:140` | `api.nodes.duplicate` | `{id}` | new node id `string` | `selectOnArrival.add(copyId)` | `useDuplicateNode` ← `POST /api/nodes/{id}/duplicate` → `{id}` |
| M6 | `actions.tsx:68` / `:117` (`redeploy`) | `api.deployments.start` | `{environmentId, only: [id], refresh}`; node toolbar: Redeploy `refresh: true`, Restart / Run again `refresh: false` | deployment id | `link.open(id)` | `useShipEnvironment` ← `POST /api/environments/{id}/deployments` `{only?, refresh?}` → `{id}` |
| M7 | `actions.tsx:68` / `:148` (`ship`) | `api.deployments.start` | `{environmentId, only?: ids, refresh: only !== undefined}`; Ship = no `only`; Retry = failed step node ids still alive (`topbar.tsx:34-40`) | deployment id | `link.open(id)` | same as M6 |
| M8 | `actions.tsx:69` / `:108` | `api.nodes.start` | `{id}` (Deploy for `pending`, Start for `stopped`) | deployment id | `link.open(id)` | `useStartNode` ← `POST /api/nodes/{id}/start` → `{deploymentId}` |
| M9 | `actions.tsx:70` / `:112` | `api.nodes.stop` | `{id}` | deployment id, or `null` when already at 0 replicas | `if (did) link.open(did)` | `useStopNode` ← `POST /api/nodes/{id}/stop` → `{deploymentId: string \| null}` |
| M10 | `actions.tsx:71` / `:80-86` / `:121` | `api.nodes.move` **with optimistic update** | `{id, position}`; fired once per dragged node on drag end (`canvas.tsx:57-62`); positions are **fractional** | none | errors toast | `useMoveNode` ← `PUT /api/nodes/{id}/position` `{x, y}` + optimistic recipe (section 7) |
| M11 | `actions.tsx:72` / `:90-96` / `:144` | `api.nodes.remove` **with optimistic update** | `{id}`; one call per id, fired in parallel for a multi-select delete (Backspace/Delete, or ⋯ → Delete via `flow.deleteElements`) | none | server silently ignores a missing / foreign id | `useDeleteNode` ← `DELETE /api/nodes/{id}` (204 also when already gone) + optimistic recipe |
| M12 | `actions.tsx:73` / `:126` | `api.nodes.expose` | `{id}` (toolbar Expose: defaults) or `{id, protocol, port?, domain?}` (http) / `{id, protocol, port?, publicPort?}` (tcp/udp) from `AddEndpoint`; empty inputs → omitted; numbers via `Number(text)` | only "did it succeed" (`!== undefined`) → close the add row | | `useExposeNode` ← `POST /api/nodes/{id}/endpoints` → `EndpointView` |
| M13 | `actions.tsx:74` / `:129-136` | `api.nodes.unexpose` | `{id}` (Make private = close all) or `{id, protocol, domain, publicPort}` copied from an `Endpoint` (the irrelevant one is `undefined`, dropped on the wire) | none | | `useUnexposeNode` ← `DELETE /api/nodes/{id}/endpoints?protocol=&domain=&publicPort=` |
| M14 | `components/canvas/bottom-panel/tabs/variables.tsx:324` / `:335` | `api.variables.set` | `{nodeId, key, value, secret, previousKey?}`; composer row: `previousKey` undefined; row editor: `previousKey = old key`; "Connect" chip (`:347`): `{key: defaultKey(name, KEY), value: "${{ name.KEY }}", secret: false}` | success flag only (`!== undefined`; Convex resolves `null`) | composer resets + refocuses key; row editor closes | `useSetVariable` ← `PUT /api/nodes/{id}/variables/{key}` `{value, secret, previousKey?}` |
| M15 | `variables.tsx:325` / `:383` | `api.variables.remove` | `{nodeId, key}` | none | | `useDeleteVariable` ← `DELETE /api/nodes/{id}/variables/{key}` |
| M16 | `components/canvas/settings.tsx:120` / `:124` | `api.logSinks.disconnect` | `{}` | success flag (`.then(() => true)`) | close dialog, `toast("Axiom disconnected for every project")` | `useDisconnectLogSink` ← `DELETE /api/organization/log-sink` |
| M17 | `components/canvas/observability/axiom-gate.tsx:173` / `:211` | `api.logSinks.cancelAxiomSignIn` | `{}` | none | picker disappears via Q7/Q8 | `useCancelAxiomSignIn` ← `DELETE /api/organization/axiom/pending` |

### 4.3 Actions (`useAction`): polled or one-shot, never subscribed

| # | file:line (hook / call) | Convex function | Args | Cadence | Result fields used | Replacement |
| --- | --- | --- | --- | --- | --- | --- |
| A1 | `components/canvas/bottom-panel/tabs/logs.tsx:19` / `:27` | `api.logs.tail` | `{nodeId, tail: 300}` | `setInterval` every **3000 ms** while the Logs tab is mounted and `node.type !== "volume" && status !== "pending"`; first call immediately; calls can overlap; cancelled flag on unmount | `source` (`"axiom"` → " · via Axiom"), `lines[]` (`time`, `text`, `stream`, `task`), `replicas[]` (`task`, `slot`) → `r<slot>` tags, one tone per distinct slot; tags shown when `replicas.length > 1` or any line has a non-empty `task` | `useTailNodeLogs` ← `GET /api/nodes/{id}/logs?tail=300` → `LogTail`; `refetchInterval: 3000`, `enabled` as today, `meta.realtime = false`; error → inline red text (replaces body); success clears it |
| A2 | `tabs/tracing.tsx:19` / `:43` | `api.tracing.enable` | `{nodeId, on: !enabled}` | click | none (Q17 refreshes) | `useSetNodeTracing` ← `PUT /api/nodes/{id}/tracing` `{on}` |
| A3 | `observability/axiom-gate.tsx:138` / `:142` | `api.logSinks.beginAxiomSignIn` | `{redirectUri: location.origin + "/axiom/callback"}` | click | `url` → `sessionStorage["keel.axiom.return"] = JSON.stringify({slug})`, `location.assign(url)`; failure → `busy=false` | `useBeginAxiomSignIn` ← `POST /api/organization/axiom/sign-in` `{redirectUri}` → `{url}` |
| A4 | `observability/axiom-gate.tsx:172` / `:178` | `api.logSinks.chooseAxiomOrg` | `{orgId}` | click | `org`, `dataset` → `toast.success("Every project's logs and traces now go to Axiom · {org} · {dataset}")` | `useChooseAxiomOrg` ← `POST /api/organization/axiom/choose` `{orgId}` → `{dataset, org}` |
| A5 | `observability/explorer.tsx:187` / `:198` | `api.logs.recent` | `{environmentId, range, search, tail: 300}` (`search` = trimmed field debounced 300 ms) | poll: next call 10 s after **both** A5 and A6 settle (setTimeout chain), only while the stream is visible (`active = !hidden`) | `lines[]` (`time`, `text`, `stream`, `task`, `serviceId`) | `useListEnvironmentLogs` ← `GET /api/environments/{id}/logs?range=&search=&tail=300` → `{source, lines}`; `refetchInterval: 10000`, `enabled: active`, `placeholderData: keepPreviousData`, `meta.realtime = false` |
| A6 | `observability/explorer.tsx:188` / `:199` | `api.traces.overview` | `{environmentId, range, search}`; skipped (resolves `null`) when `!sink.traces` | same chain as A5 | `from`, `to` (rate = requests per minute over `to - from`), `bucketMs`, `stats` (`requests`, `errors`, `p50`, `p95`, `p99`), `buckets[]` (same + `time`), `traces[]` (TraceSummary) | `useGetTraceOverview` ← `GET /api/environments/{id}/traces/overview?range=&search=` → `TraceOverview`; same options, `enabled: active && !!sink.traces` |
| A7 | `observability/log-context.tsx:38` / `:45` | `api.logs.around` | `{environmentId, at}` | once per `(environmentId, at)` (`Promise.all` with A8; either failing = error) | `ProjectLine[]` oldest first | `useListLogsAround` ← `GET /api/environments/{id}/logs/around?at=` → `ProjectLine[]`; `staleTime: Infinity`, `meta.realtime = false` |
| A8 | `observability/log-context.tsx:39` / `:45` | `api.traces.around` | `{environmentId, at}` | with A7 | `TraceSummary[]` (`traceId`, `start`, `name`, `duration`, `error`, `errors`); sorted by `start` client-side | `useListTracesAround` ← `GET /api/environments/{id}/traces/around?at=` → `TraceSummary[]`; same options |
| A9 | `observability/trace.tsx:100` / `:107` | `api.traces.get` | `{environmentId, traceId, at?}` (`at` = root start or the clicked line's time; absent when opened from a link) | once per `(environmentId, traceId, at)` | `traceId`, `spans[]` (all Span fields), `logs[]` (ProjectLine) | `useGetTrace` ← `GET /api/environments/{id}/traces/{traceId}?at=` → `Trace`; `staleTime: Infinity`, `meta.realtime = false` |
| A10 | `routes/_auth/axiom/callback.tsx:29` / `:51` | `api.logSinks.signInAxiom` | `{state, code}` from Axiom's redirect | **exactly once** (`useRef` guard: `state` is single-use server-side) | `choose`; when `false`: `org`, `dataset` → success toast; always `finally` navigate to `/p/<slug>?view=observability` (slug from `sessionStorage`, removed on read) or `/`; Axiom `error` / missing code or state → `toast.error("Axiom: " + (error_description \|\| error \|\| "no code returned"))` without calling | `useCompleteAxiomSignIn` ← `POST /api/organization/axiom/callback` `{state, code}` → `{choose: true} \| {choose: false, dataset, org}`; keep the ref guard |

### 4.4 Better Auth client calls

| # | file:line | Call | HTTP today (on `convexSiteUrl`) | Fields used / behaviour | Replacement |
| --- | --- | --- | --- | --- | --- |
| B1 | `main.tsx:24` (implicit, `ConvexBetterAuthProvider`) | `authClient.useSession()`, `authClient.convex.token()` | `GET /api/auth/get-session`, `GET /api/auth/convex/token` | drive the Convex auth state | `useSession()` (`GET /api/session`) |
| B2 | `components/sign-in-form.tsx:18` | `authClient.signIn.email({email, password}, {onSuccess, onError})` | `POST /api/auth/sign-in/email` | client validation first (zod: `z.email("Invalid email address")`, password `min(8, "Password must be at least 8 characters")`); success → `toast.success("Sign in successful")`; error → `toast.error(error.error.message \|\| error.error.statusText)` | `useSignIn` ← `POST /api/auth/sign-in` `{email, password}` (sets cookie) |
| B3 | `components/sign-up-form.tsx:37` | `authClient.signUp.email({email, password, name, invitationId?})` | `POST /api/auth/sign-up/email` | `invitationId` rides in the body (not a user field; the server's user-create hook reads it); zod adds `name min(2, "Name must be at least 2 characters")`; invite → email input disabled and prefilled; success → `toast.success("Sign up successful")` + `onSuccess()` (invite page: `navigate("/", replace)`); server refusal text: `Sign-up is by invitation. Ask a member for an invite link.` | `useSignUp` ← `POST /api/auth/sign-up` `{email, password, name, invitationId?}` |
| B4 | `account-menu.tsx:44`, `routes/index.tsx:33`, `routes/_auth/device.tsx:146`, `routes/invite.$invitationId.tsx:60` | `authClient.signOut()` | `POST /api/auth/sign-out` | fire-and-forget; no navigation | `useSignOut` ← `POST /api/auth/sign-out`, then `resetQueries` + WS close |
| B5 | `components/invite-dialog.tsx:36` | `authClient.organization.inviteMember({email: email.trim(), role: "member", organizationId})` | `POST /api/auth/organization/invite-member` | `data.id` → shows link `${location.origin}/invite/${id}`; `error \|\| !data` → `toast.error(error?.message ?? "Could not create the invite")` | `useCreateInvitation` ← `POST /api/invitations` `{email, role: "member"}` → `{id}` (org from session; drop `organizationId`) |
| B6 | `routes/invite.$invitationId.tsx:35` | `authClient.organization.acceptInvitation({invitationId})` | `POST /api/auth/organization/accept-invitation` | error → `toast.error(error.message ?? "Could not accept the invitation")`; success → `navigate("/", replace)` | `useAcceptInvitation` ← `POST /api/invitations/{id}/accept`; then reset queries + reconnect WS |
| B7 | `routes/_auth/device.tsx:74` | `authClient.device({query: {user_code}})` | `GET /api/auth/device?user_code=…` | `{user_code, status: "pending" \| "approved" \| "denied"}`; any error → "Link no longer valid". **Side effect**: while signed in, an unclaimed pending code is bound to this user (only that user can approve) | `useClaimDeviceCode` ← `POST /api/device/claim` `{userCode}` → `{userCode, status}` (a POST because it writes); call once per code (effect with cancelled flag) |
| B8 | `routes/_auth/device.tsx:87` | `authClient.device.approve({userCode})` | `POST /api/auth/device/approve` | error shown as `error_description ?? message ?? statusText ?? "Something went wrong"` | `useApproveDevice` ← `POST /api/device/approve` `{userCode}` |
| B9 | `routes/_auth/device.tsx:88` | `authClient.device.deny({userCode})` | `POST /api/auth/device/deny` | same | `useDenyDevice` ← `POST /api/device/deny` `{userCode}` |

Device error texts the page may show (better-auth 1.6.17): `Invalid user code`, `User code has
expired`, `Device code already processed`, `Device code has not been claimed by a verifying
session; call `GET /device` with the `user_code` while signed in before approving or denying`,
`Authentication required`, `You are not authorized to approve this device authorization`.

### 4.5 Auth gates

| # | file:line | Component | Replacement |
| --- | --- | --- | --- |
| G1 | `routes/_auth/route.tsx:15-27` | `<Authenticated><Outlet/>` / `<Unauthenticated>` forms / `<AuthLoading>` loader | `<SessionGate signedOut={<AuthShell><AuthForms/></AuthShell>}>` over `useSession()` |
| G2 | `routes/index.tsx:43-53` | same, signed-in child = `Bootstrap` | same |
| G3 | `routes/invite.$invitationId.tsx:92-100` | signed-in → `Accept`, signed-out → `SignUpForm` with invitation | same |

---

## 5. Return shapes the web consumes (JSON contract)

Field names are the wire names the Go API must return (camelCase). Times are epoch ms numbers.
"opt" = may be absent (Convex drops `undefined` fields; Go may send `null` only where noted,
because several call sites test `=== undefined` or spread the object). IDs are opaque strings.

### 5.1 `NodeView` (`nodes.list` item; `nodeHelpers.view`)

Order: **creation ascending** (Convex `by_environment` index ties break on `_creationTime`).
`chrome.tsx` assigns service tones by index, React Flow stacks later nodes on top, and
`toCanvasNodes` moves groups first while keeping relative order.

| Field | Type | Meaning / derivation | Used by web |
| --- | --- | --- | --- |
| `id` | string | node id | yes |
| `type` | `"service" \| "database" \| "cache" \| "volume" \| "group"` | | yes |
| `name` | string | unique per environment | yes |
| `parentId` | string opt | group id; position is then relative to the group | yes (`parentId` + `extent: "parent"`) |
| `position` | `{x: number, y: number}` | floats allowed | yes |
| `config` | `{sizeGb?, width?, height?}` | volume size; group box | yes (`sizeGb ?? 0`, `width ?? 300`, `height ?? 180`) |
| `dirty` | boolean | `n.dirty ?? false` | no (CLI) |
| `status` | NodeStatus | `deriveStatus` (below) | yes |
| `image` | string opt | `desired.image` | yes |
| `port` | number opt | `desired.port` | yes |
| `replicas` | number | `desired.replicas ?? 0` | yes |
| `running` | number | `observed.running ?? 0` | yes |
| `revision` | number | `desired.revision ?? 0` | no |
| `deployedRevision` | number opt | | no |
| `public` | boolean | `endpoints.length > 0` | yes (Expose vs Make private) |
| `publicUrl` | string opt | first http endpoint address | no (CLI) |
| `endpoints` | `EndpointView[]` | `[]` when none | yes |
| `error` | string opt | `applyError ?? (status === "error" ? observed.error : undefined)` | yes |
| `deploy` | `{step, startedAt}` opt | only when `status === "deploying"` and `shippedAt` set; `step` = `"pulling image"` if `!observed \|\| observed.revision < desired.revision`, else `"rolling out"` if `observed.state === "updating"`, else `"starting"`; `startedAt = shippedAt` | yes ("Deploying · {step} {elapsed}") |
| `stoppedAt` | number opt | `shippedAt` when status is `stopped` or `stopping` | yes |
| `finishedAt` | number opt | `observed.finishedAt` when status is `done` | yes |

```ts
// status.ts — NodeStatus = healthy | done | deploying | stopping | error | stopped | pending
if (!desired || desired.revision === 0) return "pending";
if (node.applyError) return "error";
if (!observed) return "deploying";
if (desired.replicas === 0) {
  if (observed.running > 0) return "stopping";
  if (observed.revision === 0 || observed.revision >= desired.revision) return "stopped";
  return "stopping";
}
if (observed.revision < desired.revision) return "deploying";
if (observed.state === "crashloop" || observed.state === "failed") return "error";
if (converged(node)) return observed.state === "completed" ? "done" : "healthy";
return "deploying";
```

`EndpointView`: `{protocol: "http" | "tcp" | "udp", port: number, domain?: string (http),
publicPort?: number (tcp/udp), address: string, state: "starting" | "live" | "failed",
error?: string}`. `address` = `https://<domain>` for http, else `<KEEL_PUBLIC_IP or "<public IP>">:<publicPort>`.
The web uses every field (`EndpointAddress`, networking rows, `bestHttp` ranks live < starting <
failed, failed message in the meta strip with `https://` stripped).

### 5.2 Summary (`environments.summary`)

`{pendingChanges: number, counts: {[NodeStatus]?: number}, servers: number} | null` (null when the
environment is not the caller's). `pendingChanges` = non-group nodes with `dirty` and a deployable
type; `counts` = derived status per non-group node (volumes count as `pending`); `servers` =
install-wide `cluster.servers ?? 0`. Web uses `pendingChanges`, `servers`.

### 5.3 `Deployment` (`deployments.latest`, `.get`, `.listForNode`)

Today these return the raw Convex document, so the web reads **`_id`** (`mapping.ts:110`
`id: d._id`). Target: `id`.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string | was `_id` |
| `environmentId` | string | unused by web |
| `sha` | string opt | unused by web (mapped, never shown) |
| `message` | string | `"<verb> <name>, <name>"`; verb = explicit (`stop`, `start`, `deploy` for never-shipped) or `redeploy` / `deploy` (with `only`, `refresh` true / false) or `ship` |
| `status` | `"running" \| "success" \| "failed"` | |
| `startedAt` | number | |
| `finishedAt` | number opt | |
| `steps` | `[{nodeId?: string, label: string, status: "pending" \| "running" \| "done" \| "failed", startedAt?: number, appliedAt?: number, finishedAt?: number}]` | one per node (label = node name at ship time) then a final `{label: "health checks"}` with **no** `nodeId` |
| `log` | `[{at: number, nodeId?: string, text: string}]` | last 500 entries, oldest first |

Ordering: `latest` = newest; `listForNode` = newest first, scans the environment's 50 newest and
keeps those with a step for the node, at most 20.

Web mapping (`mapping.ts` `toDeployment`): `steps[].nodeId ?? ""`; each log line becomes
`` `${formatClock(at - startedAt)}  ${who}${text}` `` where `who = "<step label>  "` only when more
than one step has a `nodeId` (else `""`; unknown node → `"?"`). The Deployments tab colours a log
line red when it matches `/error:|crash loop|timed out|node deleted/` and the last line ink, so the
server's log texts (`error: …`, `<name>: crash loop · …`, `<name>: timed out waiting for replicas…`,
`<name>: node deleted`) must keep those words (backend spec owns the exact texts).

### 5.4 Variables

`VariableView` (`variables.list`, rows in creation order of the variable rows; a rename via
`previousKey` keeps the row's place):
`{key: string, value: string (as typed), resolved: string (references expanded), secret: boolean
(row flag), resolvedSecret: boolean (a referenced value is secret), parts: Part[]}` where
`Part = {text: string} | {ref: {node?: string, nodeId?: string, key: string, missing: boolean}}`
(`node` absent = own node; `nodeId` absent when the name resolves to nothing). The web
discriminates with `"ref" in p`, so a part must carry exactly one of `text` / `ref`.

`ReferenceSource` (`variables.referenceable`): `{nodeId: string, name: string, type: NodeType,
image?: string, keys: [{key: string, secret: boolean, provided: boolean}]}`. Sources = every other
deployable node of the environment, in creation order. `keys` = provided keys first, in the
order `DATABASE_URL` | `REDIS_URL` | `URL`, then `HOST`, then `PORT` (minus any the node overrides
with its own row), then the node's own rows in creation order.

### 5.5 Tracing

`TracingView` (`tracing.forNode`) or `null` (not a service, no `desired`, not owned):
`{enabled: boolean, traces: "off" | "old" | "on", env: [{key: string, value: string, secret:
boolean, overridden: boolean}]}`; `env` order is the `tracingEnv` order (`OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS` (secret; value shows the masked key
`<prefix>…<last 4>`), `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_EXPORTER`,
`OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER`).

`tracing.prompt`: a markdown string (`tracingPrompt.ts agentPrompt`), naming the service and/or
project slug when visible to the caller.

### 5.6 Projects, organization, auth

| Function | Shape |
| --- | --- |
| `projects.list` | `[{id, name, slug, environments: [{id, name, isProduction}]}]` (projects in creation order, production environment first) |
| `projects.getBySlug` | `{id, name, slug, environment: {id, name}} \| null` (production environment, else the first) |
| `projects.create` | `{id, name, slug, environments: [{id, name: "production", isProduction: true}]}` |
| `projects.ensureDefault` | slug string |
| `organizations.current` | `{id, name, slug, role} \| null` |
| `organizations.invitation` | `{email, organization: <org name>} \| null` (unknown, spent, expired, or malformed id → `null`) |
| `auth.getCurrentUser` | Better Auth user row `{_id, name, email, emailVerified, image?, createdAt, updatedAt}` or null; web reads `name`, `email` |
| `auth.signUpOpen` | boolean: no user row exists yet |
| `nodes.publicAddress` | `KEEL_PUBLIC_IP` or `null`; throws `Not authenticated` when signed out |

### 5.7 Log sink (`logSinks.*`)

| Function | Shape |
| --- | --- |
| `logSinks.get` | `{kind: "axiom", domain, dataset, traces: string \| null, org: string \| null, tokenHint: "…<last4>"} \| null`; the web's `Sink` type (`chrome.tsx:14`) is `{domain, dataset, traces, org}` |
| `logSinks.pendingOrgs` | `[{id, name}]` (empty when none) |
| `logSinks.beginAxiomSignIn` | `{url}` |
| `logSinks.signInAxiom` | `{choose: true} \| {choose: false, dataset, org}` |
| `logSinks.chooseAxiomOrg` | `{dataset, org}` |

### 5.8 Logs and traces (`logProviders/types.ts`, `traceProviders/types.ts`)

```ts
type LogLine = { time: number; text: string; stream: "stdout" | "stderr"; task: string /* "" when unknown */ };
type Replica = { task: string; slot: number; state: string };
type Tail = { source: "docker" | "axiom"; lines: LogLine[]; replicas: Replica[] };       // logs.tail
type ProjectLine = LogLine & { serviceId: string };                                       // node id
type ProjectTail = { source: "docker" | "axiom"; lines: ProjectLine[] };                  // logs.recent
type Attribute = { key: string; value: string };                                          // sorted by key
type SpanEvent = { time: number; name: string; attributes: Attribute[] };
type Span = { spanId: string; parentId: string /* "" for root */; name: string; service: string;
  kind: string; start: number; duration: number; status: "ok" | "error" | "unset";
  statusMessage: string; scope: string; attributes: Attribute[]; resource: Attribute[]; events: SpanEvent[] };
type TraceSummary = { traceId: string; name: string; service: string; kind: string; start: number;
  duration: number; httpStatus: number | null; spans: number; errors: number; error: boolean; local: boolean };
type TraceBucket = { time: number; requests: number; errors: number; p50: number | null; p95: number | null; p99: number | null };
type TraceStats = Omit<TraceBucket, "time">;
type TraceOverview = { source: "axiom"; from: number; to: number; bucketMs: number; stats: TraceStats;
  buckets: TraceBucket[] /* oldest first, empty ones included */; traces: TraceSummary[] /* newest first */ };
type Trace = { source: "axiom"; traceId: string; spans: Span[]; logs: ProjectLine[] };
type TimeRange = "15m" | "1h" | "24h" | "7d";
```

Times and durations are **fractional** ms (sub-ms spans); keep them `float64` on the wire.
`Attribute` is a `{ key, value }` object, not a `[key, value]` tuple: OpenAPI can only describe
a tuple as `string[][]`, and an object keeps the generated type true with no cast.

Explorer constants the API must honour: `LINES = 300` lines per poll and `REQUESTS = 100` traces
per overview (`traceProviders/axiom LIST`); `mergeEvents` treats a list of that full length as
truncated and shows "Showing the latest events, back to …".

---

## 6. Error strings the web displays

`errorMessage(err)` (`components/canvas/errors.ts`): `ConvexError` → `String(err.data)` (the
message); other `Error` → first line of `message`; else `String(err)`. Shown by `attempt()` as a
toast, or inline where noted. The Go API's problem `detail` must carry these sentences verbatim
(ARCHITECTURE: `domain.Error.Message` kept identical).

| Function | Messages the user can see |
| --- | --- |
| `projects.ensureDefault` (inline on `/`) | `Not authenticated`; `You're not in an organization yet. Ask a member for an invite link.` |
| `projects.create` | the two above; `Project name: 1–60 characters`; `Project name needs a letter or digit (a-z, 0-9)`; `Project "<slug>" already exists` |
| `projects.list` (query that **throws**) | `Not authenticated`; the NO_ORGANIZATION sentence (Convex rethrows inside `useQuery` during render; unreachable in practice because the switcher only mounts on an owned project) |
| `nodes.create` | `Environment not found`; `Only services take a custom image`; `This node type has no runtime settings`; `<engine> is not a <type>`; `Image must look like repo/name:tag`; `"<name>" is already taken`; `Name: 1–40 chars, a-z 0-9 and - only`; `Replicas must be 0–20`; `Port must be 1–65535`. A ship refused inside `create {deploy}` is swallowed (`deploymentId` absent) |
| `nodes.move` | `Node not found` |
| `nodes.rename` | `Node not found`; `Name: 1–40 chars, a-z 0-9 and - only`; `"<name>" is already taken` |
| `nodes.start` / `nodes.stop` | `Node not found`; `This node type cannot be started` / `This node type cannot be stopped`; `A deployment is already running`; `Nothing to ship` |
| `deployments.start` | `Environment not found`; `A deployment is already running`; `Nothing to ship` |
| `nodes.expose` | `Node not found`; `Only services, databases and caches can be exposed`; `Ship this Redis first: its password takes effect on the next Ship`; `Set the service's port first`; `Port must be 1–65535`; `Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP`; `HTTP is always served on 80 and 443`; `Domain must look like app.example.com`; `<domain> is already used by <node name>`; `Only HTTP endpoints have a domain`; `80 and 443 serve HTTP; pick another public port`; `Port <n>/<protocol> is already used by <node name>`; `At most 10 endpoints per node`; `No free public port left` |
| `nodes.unexpose` | `Node not found`; `Name the endpoint: protocol and domain (http) or public port`; `Domain must look like app.example.com` |
| `nodes.duplicate` | `Node not found`; `Groups cannot be duplicated` |
| `nodes.remove` | none (missing / foreign id is a silent no-op) |
| `variables.set` | `Node not found`; `Key: UPPER_SNAKE_CASE only`; `Value too long`; `<KEY> already exists` |
| `variables.remove` | `Node not found` |
| `tracing.enable` | `Node not found`; `Only services can be traced`; `Connect Axiom to see traces`; `Sign in with Axiom again to turn on traces` |
| `logSinks.disconnect` | NO_ORGANIZATION sentence |
| `logSinks.beginAxiomSignIn` | `Bad redirect URI`; NO_ORGANIZATION; Axiom client-registration error text passed through |
| `logSinks.signInAxiom` (toast on callback) | `Axiom sign-in expired, try again`; `This Axiom account has no organization`; NO_ORGANIZATION; `Querying <dataset>: <axiom message>`; Axiom exchange / provisioning error text passed through |
| `logSinks.chooseAxiomOrg` | `Sign-in expired, sign in with Axiom again`; `Organization not found`; provisioning errors as above |
| `logs.tail` (inline, red, Logs tab) | `Node not found`; provider error text (Docker / Axiom) |
| `logs.recent`, `logs.around`, `traces.overview`, `traces.around`, `traces.get` (inline: lamp error page, red line above data, or the detail view) | `Environment not found`; `Connect Axiom to search all logs` (logs); `Connect Axiom to see traces`; `Sign in with Axiom again to turn on traces` (overview); `Node not found` (overview with nodeId); `Not a trace id` (get: id must match `^[0-9a-f]{16,32}$` case-insensitive); Axiom error text passed through |

Non-`ConvexError` failures in Convex reach the browser as a redacted "Server Error" first line; in
Go that is `SERVER_ERROR` with a generic message (ARCHITECTURE "Errors").

Client-only texts (no server involvement): name editor rejects anything not matching
`^[a-z0-9-]{1,40}$` (typed text is lowercased); variable key input upper-cases and replaces
`[^A-Z0-9_]` with `_`, accepts `^[A-Z_][A-Z0-9_]{0,63}$` and flags `<KEY> already exists` against
the loaded rows; pasting `KEY=value` (optionally `export `, quotes stripped) splits into the two
inputs. These mirror `access.ts` `NAME_RE` / `ENV_KEY_RE` and `variables.ts` `REF_RE` by hand.

---

## 7. Optimistic updates

Exactly two, both in `CanvasActionsProvider` (`components/canvas/actions.tsx:76-97`), both on the
`nodes.list` query for the current `environmentId`:

```ts
moveRaw.withOptimisticUpdate((store, { id, position }) => {
  const nodes = store.getQuery(api.nodes.list, { environmentId });
  if (!nodes) return;
  store.setQuery(api.nodes.list, { environmentId }, nodes.map((n) => (n.id === id ? { ...n, position } : n)));
});
removeNodeRaw.withOptimisticUpdate((store, { id }) => {
  const nodes = store.getQuery(api.nodes.list, { environmentId });
  if (!nodes) return;
  store.setQuery(api.nodes.list, { environmentId }, nodes.filter((n) => n.id !== id));
});
```

Why (comment at `actions.tsx:76`): React Flow already shows the new position / the removal
locally; without the overlay, any server push that lands before the mutation commits (another
node's status change, a deploy log line) would snap the node back or resurrect it. Convex
re-applies a pending optimistic update on top of every incoming server value until the mutation's
own write is reflected, then drops it; a failed mutation drops it too (the node snaps back, toast
shows the error).

No other optimistic updates exist. Everything else (create, rename, duplicate, variables,
expose, tracing, sink) waits for the server and is shown by the subscription.

**Target recipe** (TanStack has no layered overlay, so build one):

- Keep a `pending` overlay in `CanvasActionsProvider`: `moves: Map<nodeId, {x, y, seq}>`,
  `removals: Set<nodeId>`. `useSyncedGraph` applies it on top of the fetched `NodeView[]` (position
  replaced, removed ids filtered) before merging into React Flow state.
- On mutate: add to the overlay, `queryClient.cancelQueries({queryKey: canvasKey})`.
- On settle (success or error): remove the entry only if `seq` is still the latest for that node
  (two quick drags of one node must not let the first response clear the second), then invalidate
  `canvasKey`. On error, the overlay entry goes and the next fetch restores the server position;
  `attempt` still toasts.
- Optionally also `setQueryData` the canvas cache in `onMutate` so other readers of the key (Q9,
  Q10) see the removal immediately; the overlay is what protects against refetch races.
- Multi-node drag sends one request per node today (`canvas.tsx:59`); a batch endpoint
  (`PUT /api/environments/{id}/positions` with `[{id, x, y}]`) is optional and would cut N
  invalidations to one. Multi-delete likewise fires N parallel deletes; the server must stay
  idempotent (204 when already gone, never "Node not found").

---

## 8. How the canvas syncs (positions, selection, ordering)

Files: `use-synced-graph.ts`, `mapping.ts`, `canvas.tsx`, `actions.tsx`, `toolbar.tsx`,
`store.tsx`, `use-deployment-link.ts`.

1. **Source of truth**: the `nodes.list` subscription for the environment (Q11). Positions,
   names, status, endpoints all come from it. There are no edges (no edge query; `nodesConnectable
   = false`).
2. **Mapping** (`toCanvasNodes`): groups first, then the rest, each through `toCanvasNode`:
   `{id, position, parentId?, extent: "parent" if parentId, type, data}` with `data` per type:
   service `{...runtime, http: bestHttp(endpoints)}`; database `{...runtime, engine:
   engineLabel(image, "Postgres")}`; cache `{...runtime, engine: engineLabel(image, "Redis")}`;
   volume `{name, status, sizeGb: config.sizeGb ?? 0}`; group `style {width: config.width ?? 300,
   height: config.height ?? 180}`, `data {label: name}`. `runtime` = `{name, status, image, port,
   replicas, running, deploy, error, stoppedAt, finishedAt, public, endpoints}`. `engineLabel`:
   `"postgres:16"` → `"Postgres 16"` (`postgres|mysql|mongo|redis` → `Postgres|MySQL|MongoDB|Redis`,
   other repos capitalised, tag `latest` dropped, digest stripped).
3. **Merge into React Flow state** (`useNodesState`), on every new query value:
   ```ts
   setNodes((prev) => {
     const prevById = new Map(prev.map((n) => [n.id, n]));
     return toCanvasNodes(nodeDocs).map((next) => {
       const old = prevById.get(next.id);
       if (!old) return select.has(next.id) ? { ...next, selected: true } : next;
       return { ...next,
         selected: select.size > 0 ? false : old.selected,
         dragging: old.dragging, measured: old.measured,
         position: old.dragging ? old.position : next.position };
     });
   });
   ```
   Local-only state: `selected`, `dragging`, `measured`, viewport (never persisted; `fitView`
   with padding 0.2, maxZoom 1 on mount; zoom 0.25–2). A node being dragged keeps its local
   position against server pushes.
4. **Writes**: positions only on drag end (`onNodeDragStop`), one `move` per dragged node with
   the node's React Flow `position` (relative to its parent when it has one; fractional). Deletes
   via `onDelete` (keys Backspace/Delete, or ⋯ → Delete). Section 7 covers the optimistic overlay.
   A group delete reparents children server-side (absolute positions); the web just re-renders.
5. **Select on arrival**: `create` and `duplicate` put the new id in `selectOnArrival` (a ref
   `Set`); when the query value first contains it, the merge selects it and deselects everything
   else, then removes it from the set. React Flow's selection change then dispatches
   `select` → the bottom panel opens on the new node (`store.tsx` reducer).
   Today this works because the Convex mutation promise resolves around the time the
   subscription delivers the new row, and the `.then` runs before the effect. **Target hazard**:
   if the mutation resolves only after the canvas refetch completed (recommended read-your-writes,
   section 9.3), the effect has already run without the id. Fix: after a create/duplicate
   resolves, select directly when the node is already in React Flow state
   (`flow.setNodes(ns => ns.map(n => ({...n, selected: n.id === id})))`), otherwise queue it in the
   set as today.
6. **Deployment link**: actions that start a deployment call `link.open(id)` → `?deployment=<id>`
   (push). `Panel` (`canvas.tsx:115-135`) waits for `deployments.get` (Q14), then opens the
   Deployments tab of the step whose `nodeId === panelNodeId`, else the first step with a
   `nodeId`; none (stale, foreign, node-less) → `clear()` (replace). A `handled` ref makes it run
   once per id. Selecting another node with a deployment linked clears the link.
   The Deployments tab highlights `rows.find(r => r.id === deploymentId) ?? current`.
7. **Canvas identity**: `<Canvas key={project.environment.id}>` remounts on project switch;
   every query below it is keyed by `environmentId` from `EnvironmentProvider`
   (`{projectId, environmentId, environmentName, projectName}`).
8. **UI state** (`store.tsx`): reducer `{panelNodeId, panelTab: "deployments" | "variables" |
   "logs" | "settings", panelCollapsed}` and a separate `renamingId` context. Pure client state;
   not part of the data layer.

---

## 9. Replacement design (target)

### 9.1 Pieces

| Piece | Path | Notes |
| --- | --- | --- |
| Generated API | `apps/web/src/gen/api/` from `kubb.config.ts` reading `../../openapi.json` | plugins: `@kubb/plugin-oas`, `@kubb/plugin-ts`, `@kubb/plugin-zod` (optional), `@kubb/plugin-client` (custom client, below), `@kubb/plugin-react-query` (hooks named from operationId: `useListNodes`, `useMoveNode`, …) |
| Fetch client | `apps/web/src/api/client.ts` (Kubb `importPath` for the client) | relative URLs, `credentials: "same-origin"`, JSON in/out; non-2xx → throw `ApiError {status, code, message, problem}` parsed from `application/problem+json`; reads the `Keel-Invalidate` header (9.3) |
| Query client | `apps/web/src/lib/query.tsx` | `QueryClientProvider` in `main.tsx` replacing the Convex providers; global `QueryCache`/`MutationCache` `onError` handles 401 (section 3.2) |
| Realtime | `apps/web/src/lib/realtime.tsx` | `centrifuge` client on `/api/ws`; on publication `{type: "invalidate", topics}` → batch 50 ms → invalidate matching keys; on (re)connect → invalidate everything live |
| Session | `apps/web/src/lib/session.tsx` | `useSession()`, `<SessionGate>`, sign-in/out helpers that reset the cache and reconnect the socket |

Defaults: `staleTime: Infinity` and `refetchOnWindowFocus: false` for realtime-covered queries
(the socket keeps them fresh, as a Convex subscription would), `retry` off for 4xx, `gcTime`
default. Polled and one-shot queries set `meta: { realtime: false }` and their own
`refetchInterval` / `staleTime`.

### 9.2 Invalidation matching

```ts
// realtime.tsx
const matches = (q: Query, topics: string[]) =>
  q.meta?.realtime !== false &&
  typeof q.queryKey[0] === "string" &&
  topics.some((t) => (q.queryKey[0] as string).startsWith(t));
queryClient.invalidateQueries({ predicate: (q) => matches(q, topics), refetchType: "active" });
```

Prefix matching must respect segment boundaries: topic `/api/nodes/abc` must not match
`/api/nodes/abcd/...`. IDs are fixed-length (20 base32 chars, ARCHITECTURE), so plain prefix is
safe for IDs; for safety compare `key === t || key.startsWith(t + "/")`.

`meta.realtime = false` exists because polled endpoints sit under invalidated prefixes
(`/api/nodes/{id}/logs`, `/api/environments/{id}/logs`, `/traces/overview`): without the opt-out,
every observe write during a deploy would re-run `docker service logs` or an Axiom query.

### 9.3 Read-your-writes after a mutation

Convex guarantees that when a mutation's promise resolves, every subscribed query already
reflects it. Call sites depend on that: the variables composer resets and the new row is there;
the tracing switch flips without flicker when `busy` clears; Make private hides the Expose state;
the Ship button reflects a new deployment. A WebSocket invalidation alone can arrive after the
HTTP response.

Recommendation: every non-GET response carries `Keel-Invalidate: <topic>,<topic>` (the same
`Changes` the write publishes). The custom fetch client, on such a response, does
`await queryClient.invalidateQueries({predicate: matches(topics), refetchType: "active"})` before
returning the body, so `mutateAsync` resolves after the active affected queries refetched. The
later socket message for the same topics refetches once more (harmless; TanStack dedupes an
in-flight fetch). This keeps components free of query-key knowledge.

### 9.4 Semantics to carry over

| Convex behaviour | Target equivalent |
| --- | --- |
| `useQuery` returns `undefined` while loading, the value (possibly `null`) after | `data` is `undefined` while pending. Nullable reads return 200 with an envelope whose field may be `null` (`{project: …\|null}`, `{deployment: …\|null}`, `{sink: …\|null}`, `{tracing: …\|null}`, `{summary: …\|null}`, `{invitation: …\|null}`). Never a 404 for "not found or not yours" on these: a 404 would become an error with retries. Components read `data?.project` and keep the `undefined` / `null` distinction. |
| `useQuery(fn, "skip")` | `enabled: false` (Q14: `enabled: !!deploymentId`) |
| A query that throws rethrows during render (no error boundary in the app) | TanStack exposes `error`; do not use `throwOnError`. Only Q21 and Q16 could throw today. |
| Query result objects are new on every update | TanStack structural sharing keeps references stable when content is equal. `ReferencePalette` keys its pages on `JSON.stringify(sources)` to survive identical re-queries; that stays correct and becomes cheaper. `useSyncedGraph` re-merges only on real changes. |
| `attempt(promise)` resolves `undefined` on failure; Convex mutations without a return resolve `null` | A 204 resolves `undefined` in most fetch clients, which `attempt` would read as failure (M12 `expose` returns a body, but M14 `variables.set`, M16 `disconnect` return nothing). Change `attempt` to return `{ok: true, data} \| {ok: false}` and update the four sites that test `!== undefined` / `=== true` (`actions.tsx:126`, `variables.tsx:335`, `settings.tsx:124`, plus `expose`'s caller `networking.tsx:126`). |
| `errorMessage(err)` reads `ConvexError.data` | Read `ApiError.message` (= problem `detail`, the sentence from section 6); fall back to `title`, then the first line of `Error.message`. |
| `Id<"nodes">` brands, `asNodeId` casts (20 sites) | Plain `string`. Delete `asNodeId` and the casts. |
| Mutations scoped by `environmentId` in args | Path parameter. The web keeps getting it from `useEnvironment()`. |
| One Convex WebSocket carries queries and mutations | Queries/mutations over HTTP; the socket carries only invalidations. |

### 9.5 Polling, re-expressed

- **A1 Logs tab**: `useTailNodeLogs({id}, {tail: 300}, { query: { enabled: deployable && status
  !== "pending", refetchInterval: 3000, meta: {realtime: false}, staleTime: 0 } })`. TanStack
  never overlaps fetches for one key (today's `setInterval` can, and an older answer can land last).
  Keep the error precedence: if the latest fetch failed, show its message (today: `error` state set
  on failure, cleared on the next success; previous lines are not shown while erroring).
- **A5 + A6 Explorer**: two queries keyed by `(environmentId, range, search)`, both
  `placeholderData: keepPreviousData`, `refetchInterval: 10_000`, `enabled: active` (A6 also
  `!!sink.traces`). `stale` (dim the old data) = `isPlaceholderData` of either. Today's chain
  waits for both before scheduling the next poll and keeps each half's last success when the other
  fails for the same key; independent queries give the same visible result. On a new key that
  fails, today the half becomes `[]` / `null`; with TanStack it is `undefined`, treat as empty.
  Render rules (keep): first load → Loader; `sink.traces && error && no lines && no numbers` →
  `StreamError` ("Trying again every 10 seconds."); `sink.traces && !error && search === "" &&
  no lines && stats.requests === 0` → `StreamEmpty` (moth); otherwise the stream with any error as
  a red line above it.
- **A7 + A8, A9**: plain queries with `staleTime: Infinity`, keyed by their args, `meta.realtime =
  false`; A7/A8 render an error if either fails (today `Promise.all`).

### 9.6 Per-file migration checklist

| File | Change |
| --- | --- |
| `main.tsx` | Drop `ConvexReactClient`, `ConvexBetterAuthProvider`, `config` import; `Wrap` = `QueryClientProvider` + `RealtimeProvider` + `SessionProvider` |
| `lib/config.ts`, `lib/auth-client.ts`, `public/config.js` | Delete |
| `lib/axiom-sign-in.ts` | Unchanged (sessionStorage key `keel.axiom.return`, redirect `${origin}/axiom/callback`) |
| `routes/_auth/route.tsx`, `routes/index.tsx`, `routes/invite.$invitationId.tsx` | `<SessionGate>` instead of `convex/react` gates; hooks per section 4 |
| `routes/_auth/p/$projectId.tsx` | `useGetProjectBySlug`; read `data?.project` |
| `routes/_auth/device.tsx` | `useSession`, `useClaimDeviceCode` (once per code), approve/deny mutations; error display reads the problem `detail` |
| `routes/_auth/axiom/callback.tsx` | `useCompleteAxiomSignIn` inside the same run-once ref guard |
| `components/auth-forms.tsx`, `sign-in-form.tsx`, `sign-up-form.tsx`, `invite-dialog.tsx` | generated hooks; keep the toasts and zod validation; invite drops `organizationId` |
| `components/canvas/actions.tsx` | generated mutations; optimistic overlay (section 7); select-on-arrival fix (8.5) |
| `components/canvas/use-synced-graph.ts` | `useListNodes`; apply the overlay before the merge |
| `components/canvas/use-data.ts`, `use-deployment-link.ts`, `topbar.tsx`, `status-bar.tsx` | generated queries; `deployment.id` instead of `_id` |
| `components/canvas/mapping.ts` | Types from `api/gen` (`NodeView`, `Deployment`); `id: d.id`; delete `asNodeId` |
| `components/canvas/environment.tsx` | `projectId: string; environmentId: string` |
| `components/canvas/errors.ts` | `ApiError` handling; `attempt` result object |
| `components/canvas/bottom-panel/tabs/*.tsx`, `reference-palette.tsx`, `copy-prompt.tsx` | generated hooks and types (`VariableView`, `VariablePart`, `ReferenceSource`, `TracingView`, `LogTail`) |
| `components/canvas/observability/*.tsx`, `correlate.ts`, `charts.tsx` | generated hooks; types `ProjectLine`, `TraceSummary`, `TraceOverview`, `Trace`, `Span`, `Attribute`, `TraceBucket`, `TraceStats`, `TimeRange` from `api/gen` |
| `components/canvas/project-switcher.tsx`, `settings.tsx`, `account-menu.tsx` | generated hooks; `useSession` |
| `package.json` | remove `convex`, `@convex-dev/better-auth`, `better-auth`, `@my-better-t-app/backend`; add `@tanstack/react-query`, `centrifuge`; dev: `@kubb/cli`, `@kubb/core` and the plugins; script `"api:generate": "kubb generate"` |
| `vite.config.ts` | `server.proxy` (section 2.2) |
| `.env.schema` | remove `VITE_CONVEX_URL`, `VITE_CONVEX_SITE_URL` |
| `Dockerfile`, `nginx.conf`, `docker-entrypoint.sh` | delete (the binary embeds `dist/`) |

---

## 10. Realtime: keys, topics, and which writes publish them

### 10.1 Reactive reads → key → covering topic

| Read (today) | Target key (`queryKey[0]`) | Topic that invalidates it |
| --- | --- | --- |
| `auth.getCurrentUser`, `organizations.current` | `/api/session` | `/api/session` (published by `ch.Organization(org)`; plus client-side reset on auth events) |
| `auth.signUpOpen` | `/api/auth/sign-up-open` | none (public; signed-out tabs have no socket) |
| `organizations.invitation` | `/api/invitations/{id}` | none |
| `projects.list` | `/api/projects` | `/api/projects` (`ch.Projects(org)`) |
| `projects.getBySlug` | `/api/projects/by-slug/{slug}` | `/api/projects` |
| `nodes.list` | `/api/environments/{env}/nodes` | `/api/environments/{env}` |
| `environments.summary` | `/api/environments/{env}/summary` | `/api/environments/{env}`; `/api/environments` (cluster) |
| `deployments.latest` | `/api/environments/{env}/deployments/latest` | `/api/environments/{env}` |
| `deployments.get` | `/api/deployments/{id}` | `/api/deployments/{id}` |
| `deployments.listForNode` | `/api/nodes/{node}/deployments` | `/api/nodes/{node}` |
| `variables.list`, `variables.referenceable` | `/api/nodes/{node}/variables[...]` | `/api/nodes/{node}` |
| `tracing.forNode` | `/api/nodes/{node}/tracing` | `/api/nodes/{node}`; `/api/nodes` (org sink change) |
| `logSinks.get` | `/api/organization/log-sink` | `/api/organization` |
| `logSinks.pendingOrgs` | `/api/organization/axiom/pending-orgs` | `/api/organization` |
| `nodes.publicAddress`, `tracing.prompt` | `/api/control-plane`, `/api/tracing/prompt` | none (`meta.realtime = false`) |

Channel: the server subscribes each connection to `org:<organizationId>` (ARCHITECTURE), so
`/api/organization` and `/api/nodes` are implicitly the caller's organization.

### 10.2 Coarse `Changes` helpers (server)

Named after the keys above; a coarse key per scope, as in `project:<id>` / `org:<id>` /
`node:<id>` thinking:

| Helper | Coarse key | Topics published |
| --- | --- | --- |
| `ch.Projects(org)` | `org:<org>/projects` | `/api/projects` |
| `ch.Organization(org)` | `org:<org>` | `/api/organization`, `/api/session` |
| `ch.Sink(org)` | `org:<org>/sink` | `/api/organization`, `/api/nodes` (tracing views read the sink's traces state) |
| `ch.Environment(org, env)` | `env:<env>` | `/api/environments/<env>` **and** `/api/nodes/<n>` for every node of `env` (variables resolve references across nodes; per-node deployment lists derive from the environment's deployments; tracing views read node variables) |
| `ch.Canvas(org, env)` | `env:<env>/nodes` | `/api/environments/<env>/nodes` only (for `move`, which changes nothing else) |
| `ch.Deployment(org, env, d)` | `deployment:<d>` | `/api/environments/<env>`, `/api/deployments/<d>`, `/api/nodes/<n>` for each step node |
| `ch.Cluster()` | `cluster` | `/api/environments` to **every** org channel |

> **Go now:** the helpers are `ch.Projects`, `ch.Environment`, `ch.Node`, `ch.Deployment` and `ch.Organization` (`internal/app/changes.go`), plus `ch.Add` for the one-off topics: `/api/environments/<env>/nodes` on a move, `/api/organization` and `/api/nodes` on a sink change, `/api/environments` to every organization on a server-count change, `/api/me` on a membership change. There is no `Sink`, `Canvas` or `Cluster` helper, and the session topic is `/api/me`.

### 10.3 Writes → helpers

| Write (Convex function or job) | Helpers |
| --- | --- |
| `projects.create`, `projects.ensureDefault` | `Projects(org)`; when the org was founded also `Organization(org)` (the caller's session gains an org; the socket must reconnect to join the new channel, so the client resets after `ensureDefault` if `session.organization` was null) |
| `nodes.create` | `Environment(env)`; with `deploy` also `Deployment(env, d)` |
| `nodes.move` | `Canvas(env)` |
| `nodes.rename`, `nodes.setDesired`, `nodes.duplicate`, `nodes.remove`, `nodes.expose`, `nodes.unexpose` | `Environment(env)` |
| `nodes.start`, `nodes.stop`, `deployments.start` (any `beginDeployment`) | `Environment(env)`, `Deployment(env, d)` |
| `deployments.stepRunning/stepLog/stepApplied/stepFailed` | `Deployment(env, d)` |
| `reconcile.run` | `Deployment(env, d)` per deployment **whose steps, log or status changed** (today it patches every running deployment on every run) |
| `reconcile.timeoutDeployment` | `Deployment(env, d)`, `Environment(env)` (nodes get `applyError`) |
| `nodesInternal.setObserved` | `Environment(env)` **only if `view(node)` changed** (`observed.at` changes on every scan and is not in any view) |
| `nodesInternal.setApplyError`, `nodesInternal.followPort` | `Environment(env)` |
| `nodesInternal.scheduleObserve`, `clearObserveScheduled`, `events.ingest` | none |
| `proxyInternal.setStatuses`, `proxyInternal.certReport` | `Environment(env)` of each node whose endpoint status changed |
| `migrations.run` | `Environment(env)` of each touched node (runs at start; clients refetch on reconnect anyway) |
| `environments.setServers` | `Cluster()` **only when `servers` changed** (today `at` is patched on every observe) |
| `variables.set`, `variables.remove` | `Environment(env)` |
| `tracing.setEnabled` | `Environment(env)` |
| `otlp.saveKey` (first `enable` in an environment) | `Environment(env)` (masked key in tracing views) |
| `logSinks.save` (via `connectAxiom`, `signInAxiom`, `chooseAxiomOrg`), `logSinks.disconnect` | `Sink(org)` |
| `logSinks.stashPending`, `takePending`, `dropPending` (10-min expiry job), `cancelAxiomSignIn` | `Organization(org)` |
| `logSinks.startSignIn`, `takeSignIn`, `dropSignIn`, `saveClient` | none (no visible read) |
| Sign-up with invitation / accept invitation (member row created) | `Organization(org)` |

### 10.4 Delivery rules

- Publish after commit (ARCHITECTURE `a.write`), never inside the transaction.
- Coalesce per channel for about 100 ms and dedupe topics: a deploy writes a step/log patch per
  progress line and an observe per Docker event burst; the client batches another 50 ms. Use
  TanStack's default `cancelRefetch: true` on invalidation so the newest state wins.
- Reconnect (and first connect after a reconnect) → invalidate every active query that is not
  `meta.realtime === false`. A lost publication only delays freshness until the next one or the
  next reconnect.
- Signed-out tabs open no socket.

---

## 11. Types imported from the backend package (all to replace)

Every import of `@my-better-t-app/backend` and of Convex types, with the target source:

| Imported symbol | From | Where (file:line) | Target |
| --- | --- | --- | --- |
| `api` (value) | `convex/_generated/api` | `routes/index.tsx:1`, `routes/_auth/p/$projectId.tsx:1`, `routes/_auth/device.tsx:1`, `routes/_auth/axiom/callback.tsx:1`, `routes/invite.$invitationId.tsx:1`, `components/auth-forms.tsx:1`, `canvas/account-menu.tsx:1`, `canvas/actions.tsx:1`, `canvas/copy-prompt.tsx:1`, `canvas/project-switcher.tsx:1`, `canvas/settings.tsx:1`, `canvas/topbar.tsx:1`, `canvas/use-data.ts:1`, `canvas/use-deployment-link.ts:1`, `canvas/use-synced-graph.ts:1`, `bottom-panel/tabs/{deployments,logs,networking,tracing,variables}.tsx:1`, `observability/{axiom-gate,chrome,explorer,log-context,page,trace}.tsx:1` | generated hooks |
| `api` (type only, for `FunctionReturnType`) | same | `canvas/mapping.ts:1`, `bottom-panel/reference-palette.tsx:1` | generated types |
| `FunctionReturnType` | `convex/server` | `mapping.ts:3` (`NodeDoc = FunctionReturnType<typeof api.nodes.list>[number]`, `DeploymentDoc = NonNullable<FunctionReturnType<typeof api.deployments.latest>>`), `tabs/variables.tsx:4` (`Variable = FunctionReturnType<typeof api.variables.list>[number]`, `Part = Variable["parts"][number]`), `reference-palette.tsx:2` (`ReferenceSource = FunctionReturnType<typeof api.variables.referenceable>[number]`) | `NodeView`, `Deployment`, `VariableView`, `VariablePart`, `ReferenceSource` |
| `Id<"projects">`, `Id<"environments">` | `convex/_generated/dataModel` | `canvas/environment.tsx:1` (`EnvironmentScope.projectId`, `.environmentId`) | `string` |
| `Id<"nodes">` | same | `canvas/mapping.ts:2` (`asNodeId`), `canvas/copy-prompt.tsx:2` (prop `nodeId`), `bottom-panel/tabs/tracing.tsx:2` (prop `nodeId`) | `string` |
| `Id<"environments">` | same | `canvas/copy-prompt.tsx:2` (prop), `observability/explorer.tsx:2` (`useStream` arg, `NoRequests` prop), `observability/lamp.tsx:1` (`StreamEmpty` prop) | `string` |
| `Doc<…>` | | not imported anywhere in `apps/web` | |
| `LogLine`, `Replica`, `Tail` | `convex/logs` (re-exported from `logProviders/types`) | `bottom-panel/tabs/logs.tsx:2` | `LogLine`, `Replica`, `LogTail` |
| `ProjectLine` | `convex/logs` | `observability/explorer.tsx:3`, `log-context.tsx:2`, `stream.tsx:1`, `trace.tsx:2` | `ProjectLine` (consider `EnvironmentLogLine`; keep fields) |
| `TimeRange`, `TraceOverview` | `convex/traces` | `observability/explorer.tsx:4` | `TimeRange` (enum `15m \| 1h \| 24h \| 7d`), `TraceOverview` |
| `TraceSummary` | `convex/traces` | `log-context.tsx:3`, `stream.tsx:2` | `TraceSummary` |
| `Attribute`, `Span`, `Trace` | `convex/traces` | `trace.tsx:3` (`Attribute` also `correlate.ts:1`) | `Attribute` (`{ key, value }`, see 5.8), `Span`, `Trace` |
| `TraceBucket`, `TraceStats` | `convex/traces` | `observability/charts.tsx:1` | `TraceBucket`, `TraceStats` |
| `ConvexError` | `convex/values` | `canvas/errors.ts:1` | `ApiError` from `api/client.ts` |

Hand-written mirrors of backend constants (not imports; keep them in sync with the Go domain
rules): `canvas/types.ts:3` `NodeStatus` (status.ts), `types.ts:36` `Endpoint` (endpointView),
`chrome.tsx:14` `Sink` (logSinks.get minus `kind`/`tokenHint`), `tabs/variables.tsx:21` `KEY_RE`
(`^[A-Z_][A-Z0-9_]{0,63}$`), `nodes/node-shell.tsx:70` `NAME_RE` (`^[a-z0-9-]{1,40}$`),
`reference-palette.tsx:13` `refText` (`${{ node.KEY }}`, REF_RE), `add-dialog.tsx` engine list
(`postgres:16`, `mysql:8`, `mongo:7`, `redis:7`) and `types.ts` `DeployStep` / `Deployment` (UI
shapes, produced by `toDeployment`). With generated types, `NodeStatus` and `Endpoint` can import
from `api/gen` instead of mirroring.

---

## 12. Behaviours that must survive the port (checklist)

1. `nodes.list` order = creation ascending (tones, stacking); `listForNode` newest first, ≤20 of
   the last 50; `variables.list` in row creation order with in-place rename; `referenceable` key
   order (provided first). `projects.list` order is irrelevant (sorted client-side by name).
2. Nullable reads distinguish loading (`undefined`) from not-found (`null`); never 404 for them.
   `deployments.get` takes an arbitrary string from the URL and answers `null` when malformed.
3. Positions are floats; create rounds client-side, drag does not.
4. Move/remove optimistic overlay survives concurrent refetches; multi-delete is idempotent.
5. Select-on-arrival works when the mutation resolves after the refetch (8.5).
6. `nodes.stop` may return no deployment (`null`); the web must not open a link then.
7. Retry = failed step node ids still present in `nodes.list`, shipped with `refresh: true`;
   when none is left the button is a plain Ship of every dirty node.
8. `nodes.create {deploy: true}` returns `deploymentId` only when the ship started; a refused ship
   is silent and the node stays dirty.
9. Axiom callback calls the completion exactly once per page load; return slug comes from
   `sessionStorage["keel.axiom.return"]` (removed on read, JSON `{slug}`); always navigates away.
10. Device page claims the code with the first lookup while signed in; approve/deny show the
    server's `error_description`.
11. Invite link = `${location.origin}/invite/<invitation id>`; signed-in email must match
    case-insensitively to show Join; sign-up with invitation sends `invitationId` in the body.
12. Every mutation error is a toast with the server's sentence; observability errors are inline.
13. Logs tab polls every 3 s, Explorer every 10 s while visible, detail views once; none of them
    is invalidated by the socket.
14. Deploy log lines are rendered verbatim with `mm:ss` offsets from `startedAt` and node labels
    when more than one node shipped; error words drive the red tone.
15. Signing out never navigates; every gate flips to the sign-in form in place and the URL is
    kept for after sign-in.

---

## Addendum (critic)

### W1. One API, conflicting route and envelope proposals across specs

The dashboard (this spec §4), the CLI (cli-install.md B2), auth (auth-orgs.md §13) and ingress
(proxy-ingress.md §10) each proposed Go routes for the **same** Convex function, and some
disagree on path, response envelope, or "null vs 404". `keel serve` exposes one OpenAPI, so each
row needs one answer before coding. Rows where every spec agrees are omitted.

| Convex function(s) | This spec (§4) | cli-install.md B2 | auth-orgs.md §13 / proxy-ingress.md §10 | Recommendation |
| --- | --- | --- | --- | --- |
| `auth.getCurrentUser` + `organizations.current` | `GET /api/session` (`useSession().user` / `.organization`; topic `/api/session`) | `GET /api/me` → `{id,email,name}` and `GET /api/organization` → `{id,name,slug,role} \| null` | `GET /api/auth/session` → `{user, session}` or 401; `GET /api/me` → `{user:{id,email,name}, organization:{id,name,slug,role} \| null}` | One `GET /api/me` with auth-orgs' merged shape (401 when signed out); it backs `useSession()` and `keel whoami`; publish topic `/api/me` where §10 says `/api/session`. Drop `/api/auth/session`. |
| sign-up / sign-in | `POST /api/auth/sign-up`, `POST /api/auth/sign-in` | CI (A17) calls today's `POST /api/auth/sign-up/email`, `/sign-in/email` | same as this spec | Take the new paths and change `ci.yml` in the same PR (or keep the `/email` paths as aliases for one release). |
| device link (claim / approve / deny) | `POST /api/device/claim\|approve\|deny {userCode}` | keeps `GET /api/auth/device?user_code=` and `POST /api/auth/device/approve {"userCode"}` (CI approves this way) | keep `/api/auth/device/*` **exactly** (RFC 8628 bodies, binding on GET) | auth-orgs: keep the `/api/auth/device/*` paths; the web's `useClaimDeviceCode` calls `GET /api/auth/device?user_code=` (binding is its side effect). A `POST /api/device/claim` alias is optional, not a replacement. |
| `organization/invite-member` | `POST /api/invitations {email, role: "member"}` → `{id}` | — | `POST /api/organization/invitations {email, role?}` → `{id, email, role, expiresAt}` | pick one path; auth-orgs' response is a superset (the web reads only `id`). |
| `organizations.invitation` | `GET /api/invitations/{id}` → `200 {invitation: {email, organization} \| null}` | — | same path, **404** instead of null | 200 + `null` (the page renders "Invite not found" from null, not from an error); a malformed id is also `null`. |
| `projects.ensureDefault` | `POST /api/projects/ensure-default` → `{slug}` | — | `POST /api/projects/default` → `{slug}` | pick one; behaviour identical (auth-orgs.md §6.3). |
| `environments.summary` | `200 {summary: {pendingChanges, counts, servers} \| null}` | `200 {pendingChanges,counts,servers}`; not owned → 404 `PROJECT_NOT_FOUND` | — | This spec's envelope (Huma cannot type a top-level `null`); the CLI already turns a `null` summary into `PROJECT_NOT_FOUND "Environment not found"` client-side (`api.go`), so it only has to unwrap `.summary`. |
| `deployments.latest` / `deployments.get` | `200 {deployment: Deployment \| null}`; malformed id → `null` (not 422) | `latest`: doc or `null`; `get`: 404 → `DEPLOYMENT_NOT_FOUND` | — | Envelope + `null` for both; the CLI maps a `null` `get` to its not-found error as today (`api.go` checks `d == nil`). `id` replaces `_id` in both specs (CLI B2 and §5.3 agree). |
| `tracing.forNode` | `200 {tracing: TracingView \| null}` | 404 → CLI `INVALID_INPUT "…only services can be traced"` | — | Envelope + `null`; the CLI already treats `t == nil` that way. |
| `projects.getBySlug` | `200 {project: … \| null}` | — | — | as proposed. |
| `traces.overview` | `GET /api/environments/{id}/traces/overview?range=&search=` | `GET /api/environments/{id}/traces?range=&search=&nodeId=` | — | One route with all three params (`nodeId` optional; `keel traces <service>` needs it). |
| `variables.set` / `variables.remove` | `PUT /api/nodes/{id}/variables/{key}` `{value, secret, previousKey?}`; `DELETE /api/nodes/{id}/variables/{key}` | `POST /api/nodes/{id}/variables {key,value,secret,previousKey?}`; `POST /api/nodes/{id}/variables/delete {key}` ("key in the body: user input may contain `/`") | — | Body form (CLI's): the key is validated **inside** the handler (`Key: UPPER_SNAKE_CASE only` must be the error a bad key gets, not a 404 from the router); `previousKey` and `key` are both user input. |
| `nodes.expose` / `nodes.unexpose` | `POST /api/nodes/{id}/endpoints` → `EndpointView`; `DELETE /api/nodes/{id}/endpoints?protocol=&domain=&publicPort=` | — | `POST /api/nodes/{id}/expose`, `POST /api/nodes/{id}/unexpose` (body) | pick one; keep the selector semantics of proxy-ingress.md §4.2 (no selector = close all) whichever transport carries it. |
| `nodes.publicAddress` | `GET /api/control-plane` → `{publicIp: string \| null}`, `meta.realtime = false` | — | `GET /api/public-address` → `string \| null`; key `install:public-ip` | This spec's object form (extensible, typed). Still requires a signed-in user (`Not authenticated` today). |
| `nodes.move` | `PUT /api/nodes/{id}/position {x, y}` (§4.2 M10; §8 also mentions a batch `PUT /api/environments/{id}/positions`) | — | — | One per-node route is enough (today the web fires one call per dragged node). |

### W2. Realtime key names used by the other specs, mapped onto §10

The other specs name invalidation keys in their own words. §10.2's `Changes` helpers are the
single vocabulary; read the other specs through this table:

| Key as written elsewhere | Where | Means, in §10.2 terms |
| --- | --- | --- |
| `env:<environmentId>` | swarm-worker.md §17, observability.md §18, proxy-ingress.md §10 | `ch.Environment(org, env)` (which also covers every `/api/nodes/<n>` of that environment) |
| `Environment(env)` | projects.md §11 | same |
| `deployment:<id>` | swarm-worker.md §17 | `ch.Deployment(org, env, d)` |
| `cluster` | swarm-worker.md §17 | `ch.Cluster()` (publish only when `servers` changed) |
| `node:<id>` | observability.md §18 | covered by `ch.Environment(org, node.environmentId)`; `ch.Node(org, id)` (ARCHITECTURE) is the narrower option for `tracing.setEnabled` |
| `org:<orgId>` | observability.md §18 (sink, pending orgs), auth-orgs.md §12 | `ch.Sink(org)` for `logSinks.save`/`disconnect`; `ch.Organization(org)` for `axiomPending` writes and membership |
| `project:<projectId>` | observability.md §18 (`tracing.prompt` reads the slug) | none needed: `tracing.prompt` is `meta.realtime = false` here, and project slugs never change today |
| `Projects(org)` | projects.md §11 | `ch.Projects(org)` |
| `install:public-ip` | proxy-ingress.md §10 | none (`/api/control-plane` is static per process; reconnect refetches) |
| `user:<userId>`, `session:<id>` | auth-orgs.md §12 | per-connection concerns, not topics: on sign-out close that session's sockets; on a new membership (founding, invite accept) the client resets queries and reconnects so the server subscribes it to `org:<orgId>` (§10.3 first row) |

Writes that only one spec lists and §10.3 already covers: `nodesInternal.followPort`,
`proxyInternal.setStatuses`/`certReport` (only patched nodes), `migrations.run` (per touched
node, or nothing since clients refetch on reconnect after the restart), `otlp.saveKey`.
