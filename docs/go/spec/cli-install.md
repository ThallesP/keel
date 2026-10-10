# Porting spec: the `keel` CLI, install, deploy, CI

Scope: everything outside the Convex functions and the web UI that a person or an agent touches to
get Keel running and to drive it from a terminal. Written so the Go rewrite (one binary: `keel
serve` control plane with embedded web, SQLite and Caddy; `keel agent` per node; the CLI) can
reproduce today's behaviour without reading the TypeScript, shell or YAML.

Sources read (at `36c2ded`, branch `go-single-binary`):

- `apps/cli/**` (Go module `github.com/ThallesP/keel/apps/cli`, every `.go` file and `README.md`)
- `install.sh`, `README.md` (Install, Install with an agent, Development)
- `deploy/compose.yml`, `deploy/functions-entrypoint.sh`, `deploy/functions.Dockerfile`
- `scripts/bootstrap-swarm.sh`, `scripts/deploy-worker.sh`, `scripts/dev-https.sh`
- `apps/web/{Dockerfile,docker-entrypoint.sh,nginx.conf,public/config.js,src/lib/config.ts,.env.schema}`,
  `apps/worker/Dockerfile`, `apps/proxy/{Dockerfile,caddy.json}`
- `.github/workflows/{ci.yml,images.yml,pullfrog.yml}`, root `package.json`, `turbo.json`, `bunfig.toml`, `.dockerignore`
- Server side of every call the CLI makes: `convex/{auth,access,organizations,projects,environments,nodes,nodeHelpers,status,variables,deployments,logs,traces,timeRange,tracing,tracingPrompt,http,worker,migrations}.ts`
- better-auth 1.6.17 `plugins/device-authorization/{index,routes,error-codes,schema}.mjs`, `@convex-dev/better-auth` 0.12.5 `plugins/convex`

Part A documents what exists. Part B says what each piece becomes in the single-binary world.
Where Part B proposes routes or names, other specs in `docs/go/spec/` that define the HTTP API are
authoritative for the route; the semantics listed here are what the CLI needs.

---

# Part A — what exists today

## A1. The CLI at a glance

| Item | Value |
| --- | --- |
| Module | `github.com/ThallesP/keel/apps/cli`, `go 1.27.1`, own `go.mod` outside bun/turbo |
| Deps | `github.com/spf13/cobra v1.10.2`, `golang.org/x/term v0.46.0` (indirect: pflag, mousetrap, x/sys) |
| Entry | `cmd/keel/main.go`: `keel.UserAgent = "keel-cli/" + version`; `ctx := signal.NotifyContext(Background, os.Interrupt, SIGTERM)`; `os.Exit(cli.Execute(ctx, version))` |
| Version | `var version = "dev"`, set with `-ldflags "-X main.version=1.2.3"`. Cobra's `--version` prints `keel version <v>` (plain text, not the envelope) |
| Build | `cd apps/cli && go build -o bin/keel ./cmd/keel` (`/bin/` gitignored). No release pipeline: there is no published CLI binary today |
| Packages | `internal/output` (contract), `internal/config` (config.json), `internal/convex` (Convex HTTP API client), `internal/keel` (discovery, sign-in, typed calls, `translate`), `internal/cli` (one file per noun) |
| Transport | Plain HTTP. No websockets, no subscriptions: everything that watches, polls |
| HTTP client | One shared `http.Client{Timeout: 60s}`; every request sets `User-Agent: keel-cli/<version>`; JSON bodies set `Content-Type: application/json` |

## A2. Output contract (`internal/output`) — must be preserved byte-for-byte

"Fields and codes are only ever added, never renamed or removed."

- **stdout = results only.** Human mode: text/tables. JSON mode (`--json`, or `KEEL_JSON=1` or
  `KEEL_JSON=true`): exactly one JSON object + `\n`.
  - `Result(v)`: encodes `v` (must encode to an object) with `json.Encoder`, `SetEscapeHTML(false)`
    (so `<`, `>`, `&` stay literal), then rewrites it so `"ok"` is the first key:
    `{"ok":true` + (`,` unless the object was empty) + rest. `struct{}{}` → `{"ok":true}`.
  - Streams (`logs --follow`): one JSON object per line via `Event(v)`, **without** `ok`.
  - `keel run` leaves stdout to the child process.
- **stderr** = progress (`Progress`: `format + "\n"`), warnings (`"warning: " + format + "\n"`),
  and errors.
- **Errors** (`Fail(e)`): always writes to stderr `error: <message>\n` and, if `Fix != ""`,
  `fix:   <fix>\n` (three spaces after `fix:`). In JSON mode additionally writes to stdout
  `{"ok":false,"code":…,"error":…,"fix":…}` plus every key of `e.Extra`. The body is a Go map, so
  after `ok` **all** keys are in sorted order, extras interleaved: e.g.
  `{"ok":false,"code":…,"deployment":…,"done":…,"error":…,"fix":…}`. `fix` is always present
  (possibly `""`).
- **Exit codes**: 0 ok; 1 any error code not listed; 2 `USAGE`; 4 `NOT_AUTHENTICATED` and
  `AUTHORIZATION_PENDING`; 130 `CANCELLED`. `keel run` exits with the child's code, `128+signal`
  when the child was killed by a signal. `logs -f` exits 0 on Ctrl-C.
- **No prompts without a terminal**: only `service delete` ever prompts, and only when
  `!JSON && isatty(stdin)`.
- **Times** print as RFC 3339 UTC with milliseconds: `keel.Time.MarshalJSON` uses
  `"2006-01-02T15:04:05.000Z07:00"` on `t.UTC()` → `2026-10-08T12:00:00.123Z`. Exception:
  `login`'s `expiresAt` is a plain `time.Time` truncated to seconds, UTC → `2026-10-08T12:30:00Z`.

### Error codes (stable, agents branch on them)

| Code | Exit | Raised when |
| --- | --- | --- |
| `USAGE` | 2 | Bad flags/args, cobra errors (unknown command, arg count), missing required input; `fix` = `<command path> --help` unless stated |
| `NOT_AUTHENTICATED` | 4 | No saved login, expired/revoked session, server says `Not authenticated`, HTTP 401, device code expired/denied/used up |
| `AUTHORIZATION_PENDING` | 4 | A pending `keel login` link not yet approved |
| `NO_ORGANIZATION` | 1 | Server message starts with `You're not in an organization` |
| `NO_PROJECTS` | 1 | `projects:list` returned `[]` |
| `PROJECT_REQUIRED` | 1 | Several projects, none picked |
| `PROJECT_NOT_FOUND` | 1 | Unknown slug/id, project without environment, `Environment not found`, `environments:summary` null |
| `SERVICE_NOT_FOUND` | 1 | Name/id not in `nodes:list`, server `Node not found`, created service vanished |
| `VARIABLE_NOT_FOUND` | 1 | `var delete` of a key the service lacks |
| `DEPLOYMENT_NOT_FOUND` | 1 | `deployments:get`/`latest` returned null |
| `DEPLOYMENT_RUNNING` | 1 | Server `A deployment is already running` |
| `DEPLOYMENT_FAILED` | 1 | Awaited deployment ended `failed`; adds `"deployment"` to the error object |
| `NOTHING_TO_SHIP` | 1 | Server `Nothing to ship` |
| `NAME_TAKEN` | 1 | Server `Project "<slug>" already exists` or `"<name>" is already taken` |
| `TRACES_OFF` | 1 | Server `Connect Axiom to see traces` or `Sign in with Axiom again to turn on traces` |
| `INVALID_INPUT` | 1 | Any other server `ConvexError` string; client-side "only services can be traced" |
| `DISCOVERY_FAILED` | 1 | `/config.js` missing, malformed or without URLs |
| `NETWORK_ERROR` | 1 | Transport error that is a `net.Error` but not a timeout |
| `SERVER_ERROR` | 1 | Non-ConvexError function failure, non-JSON responses, unexpected auth responses |
| `CONFIG_ERROR` | 1 | Can't read/parse/write `config.json` |
| `TIMEOUT` | 1 | `net.Error` timeout; `--timeout` elapsed while awaiting a deployment |
| `CANCELLED` | 130 | Ctrl-C/SIGTERM, `context.Canceled`, declined delete prompt |

### `Execute` (root.go) error funnel

1. `cmd.ExecuteContextC(ctx)`; nil → exit 0.
2. `*childExit` (from `run`) → its code, nothing printed.
3. If the printer was never created (failure before `PersistentPreRun`: bad flag, unknown
   command), create it with JSON mode = `--json`/`KEEL_JSON` **or** `jsonArg(os.Args[1:])`.
   `jsonArg` scans raw args up to `--`: `--json` → true, `--json=<v>` → `strconv.ParseBool(v)`
   (invalid → false); last one wins.
4. `*output.Error` → as is; else if `ctx.Err() != nil` → `CANCELLED "Cancelled"`; else (cobra's
   own errors) → `USAGE "<cobra message>"`, fix `<cmd path> --help`.
5. `Fail(e)` → exit code.

Flag parse errors go through `SetFlagErrorFunc` → `USAGE`. `args(min,max)` validators give
`USAGE "usage: <cmd.UseLine()>"`. `SilenceUsage`/`SilenceErrors` are on; the completion command
is hidden.

## A3. Global flags and environment

| Flag / env | Meaning |
| --- | --- |
| `--json` / `KEEL_JSON=1` (or `true`) | JSON mode (same switch install.sh reads) |
| `--instance <name>` / `KEEL_INSTANCE` | Which saved install |
| `-p, --project <slug-or-id>` / `KEEL_PROJECT` | Which project |
| `KEEL_URL` | Dashboard URL; use an install without a saved login (with `KEEL_TOKEN`) |
| `KEEL_TOKEN` | Session token; overrides the saved one |
| `KEEL_CONVEX_URL`, `KEEL_CONVEX_SITE_URL` | With `KEEL_URL`: skip discovery (dev servers whose `/config.js` is empty) |
| `KEEL_CONFIG_DIR` | Config directory (default below) |
| `XDG_CONFIG_HOME` | Fallback base for the config dir |

## A4. Config file (`internal/config`)

Location: `$KEEL_CONFIG_DIR`, else `$XDG_CONFIG_HOME/keel`, else `~/.config/keel` (all OSes).
File `config.json`. Missing file = empty config. Written atomically: `MkdirAll(dir, 0700)`,
`CreateTemp(dir, ".config-*.json")`, `chmod 0600`, write `MarshalIndent(c, "", "  ") + "\n"`,
close, `rename`. Parse error → `CONFIG_ERROR "Can't read the config file: <err>"`, fix
`Fix or delete the file, then keel login again`. Write error → `CONFIG_ERROR "Can't write the
config file: <err>"`, fix `Check the permissions of <path>`.

```json
{
  "current": "keel.example.ts.net",
  "instances": {
    "keel.example.ts.net": {
      "url": "https://keel.example.ts.net",
      "convexUrl": "http://100.64.0.1:3210",
      "convexSiteUrl": "http://100.64.0.1:3211",
      "email": "me@example.com",
      "token": "<better-auth session token>",
      "pending": {
        "deviceCode": "<40-char secret>",
        "userCode": "ABCDEFGH",
        "url": "https://keel.example.ts.net/device?user_code=ABCDEFGH",
        "expiresAt": "2026-10-08T12:30:00Z",
        "interval": 5
      }
    }
  },
  "links": { "/home/me/acme": { "instance": "keel.example.ts.net", "project": "acme-api" } }
}
```

All of `current`, `instances`, `links`, `email`, `token`, `pending` are `omitempty`. Instance
name defaults to the dashboard URL's host (`host[:port]`). `LinkFor(dir)` walks `dir` then each
parent until `/`; first hit wins. `RemoveInstance(name)` deletes the instance, every link whose
`instance == name`, and clears `current` if it pointed there. `PendingLogin.Expired()` is
`!now.Before(expiresAt)`.

## A5. Target resolution (`internal/cli/context.go`)

**Install** (`target`):

1. `KEEL_URL` set: `normalizeURL` (must parse, scheme `http`/`https`, non-empty host; result is
   `scheme://host`, path/query dropped, input trimmed) else `USAGE "KEEL_URL: <err>"` fix
   `KEEL_URL=https://<dashboard-host>`. Instance = `{URL, ConvexURL: $KEEL_CONVEX_URL,
   ConvexSiteURL: $KEEL_CONVEX_SITE_URL, Token: $KEEL_TOKEN}`. If a saved instance has the same
   URL and `ConvexURL` is still empty, copy **both** Convex URLs from it. If either is still empty
   → `Discover`. Name = host. Never written to the file.
2. Else name = `--instance` → `KEEL_INSTANCE` → link of cwd (its `instance`) → `current` → the
   only saved instance. Missing instance: if a name was chosen and some instances exist →
   `NOT_AUTHENTICATED "Not logged in to an instance named \"<n>\" (known: a, b)"` (sorted), fix
   `keel login <dashboard-url> --name <n>`; else `NOT_AUTHENTICATED "Not logged in"`, fix
   `keel login <dashboard-url>, or set KEEL_URL and KEEL_TOKEN`.
3. `KEEL_TOKEN` set → a copy of the instance with that token (the file is never touched).

`connect` = load config → `target` → `finishLogin` → `keel.Connect` (JWT exchange, A8).

**Project** (`projectSlug`): `--project` → `KEEL_PROJECT` → cwd link's project **only if**
`link.instance == session name` → `""`. `pickProject(projects, slug)`:

| Situation | Result |
| --- | --- |
| `projects == []` | `NO_PROJECTS "No projects yet"`, fix `keel project create <name> --link` |
| a project's `slug == s` or `id == s` | that project |
| `s == ""`, one project | it |
| `s == ""`, several | `PROJECT_REQUIRED "<n> projects and none picked"`, fix `Pass --project <slug> or run keel link <slug> (projects: a, b)` |
| no match | `PROJECT_NOT_FOUND "No project \"<s>\""`, fix `Projects: a, b` |

**Environment**: `project.environments[0]` (server sorts production first); none →
`PROJECT_NOT_FOUND "Project <slug> has no environment"`, fix `Open <url> and check the project`.
There is no `--environment` flag.

**Service** (`findService`): first node of `nodes:list` (groups removed) whose `name == x` or
`id == x`; else `SERVICE_NOT_FOUND "No service \"<x>\""`, fix `Services: a, b` (in list order)
or `keel service list` when there are none.

## A6. Discovery via `/config.js` (`keel.Discover`)

- `GET <dashboard>/config.js` (UA header only). Transport error → `translate` (network codes).
- Body: take the bytes from the **first `{`** to the **last `}`** inclusive and JSON-decode
  `{"convexUrl": string, "convexSiteUrl": string}` (other keys ignored).
- Failure (status ≠ 200, no braces, bad JSON) → `DISCOVERY_FAILED "<url> doesn't look like a Keel
  dashboard (no /config.js)"`. Either field empty → `DISCOVERY_FAILED "<url>/config.js has no
  Convex URLs (a dev server?)"`. Fix for both: `keel login <url> --convex-url <url>
  --convex-site-url <url>`.
- Values are returned with trailing `/` trimmed.

What the file looks like:

- Installed: written by `apps/web/docker-entrypoint.sh` at container start:
  `window.__KEEL__ = {"convexUrl":"http://100.64.0.1:3210","convexSiteUrl":"http://100.64.0.1:3211"};`
  (printf `'window.__KEEL__ = {"convexUrl":"%s","convexSiteUrl":"%s"};\n'`). nginx serves it with
  `Cache-Control: no-store`.
- Dev (`apps/web/public/config.js`): `window.__KEEL__ = {};` → discovery fails, by design.
- The web reads `window.__KEEL__?.convexUrl || VITE_CONVEX_URL` (same for site URL) and throws
  `"<name> is not configured (window.__KEEL__ or .env)"` when both are empty.

## A7. Convex HTTP API transport (`internal/convex`)

Request: `POST <convexUrl>/api/{query|mutation|action}`, body
`{"path":"module:function","args":{…},"format":"json"}`; `nil` args are sent as `{}`;
`Authorization: Bearer <JWT>` when a token is set.

Response handling:

- Decode `{"status","value","errorMessage","errorData"}`. If not JSON or `status == ""`: HTTP 401 →
  wraps `ErrUnauthenticated` (`"<kind> <path>: unauthenticated: <snippet>"`), else
  `"<kind> <path>: HTTP <code>: <snippet>"` (snippet = trimmed body, cut at 200 chars + `…`).
- `status != "success"` → `FunctionError{Message: clean(errorMessage), Data: errorData}`.
  `clean` strips `^\[Request ID: [^\]]*\] (Server Error\n)?`, a leading `Uncaught `, and
  everything from `"\n    at "` (stack), then trims. `DataString()` = `errorData` when it is a
  JSON string (every Keel `ConvexError`), else `""`.
- Success → decode `value` into the out param.

Numbers arrive as floats: `keel.Int` decodes any JSON number into an int; `keel.Time` decodes
epoch milliseconds (float) into `time.Time` UTC.

## A8. Authentication (CLI side + the server behaviour it depends on)

### Endpoints used (all on the Convex **site** URL, prefix `/api/auth`)

Every auth request carries `Origin: <dashboard URL>` (better-auth trusts exactly `SITE_URL`) and,
when given, `Authorization: Bearer <session token>`.

| Call | Request | Success | CLI handling |
| --- | --- | --- | --- |
| `StartLogin` | `POST /api/auth/device/code` `{"client_id":"keel-cli"}` | 200 `{device_code,user_code,verification_uri,verification_uri_complete,expires_in,interval}` | Pending = `{deviceCode, userCode, url: verification_uri_complete, expiresAt: now+expires_in (UTC, truncated to s), interval: max(interval,1)}` |
| `PollLogin` | `POST /api/auth/device/token` `{"grant_type":"urn:ietf:params:oauth:grant-type:device_code","device_code":…,"client_id":"keel-cli"}` | 200 `{access_token,token_type:"Bearer",expires_in,scope}` | see table below |
| `SignOut` | `POST /api/auth/sign-out` `{}` + bearer | 200 | failure → warning only |
| `Connect` | `GET /api/auth/convex/token` + bearer | 200 `{"token": "<JWT>"}` | JWT (15 min, audience `convex`) used as the Convex bearer for this run; never cached |

`PollLogin` outcome mapping (`json.Unmarshal` of `{access_token,error,error_description}`):

| Response | Result |
| --- | --- |
| 200 and `access_token != ""` | token |
| `error == "authorization_pending"` | no token, no error |
| `error == "slow_down"` | no token, `slowDown = true` |
| `expired_token` | `NOT_AUTHENTICATED "The login link expired before anyone approved it"` |
| `access_denied` | `NOT_AUTHENTICATED "The login was denied in the dashboard"` |
| `invalid_grant` | `NOT_AUTHENTICATED "The login link is used up or unknown"` |
| other with `error_description` | `SERVER_ERROR "/api/auth/device/token: <description>"` |
| otherwise | `SERVER_ERROR "/api/auth/device/token: HTTP <status>"` |

`authCall` (code, sign-out, convex/token): 200 → decode (bad JSON → `SERVER_ERROR "unexpected
response from <path>: <err>"`); 401 → `NOT_AUTHENTICATED "Session expired or signed out"`;
body `message` or `error_description` → `SERVER_ERROR "<path>: <msg>"`; else `SERVER_ERROR
"<path>: HTTP <status>"`. `notAuthenticated(url,msg)` fix = `keel login <url>` (or
`keel login <dashboard-url>, or set KEEL_URL and KEEL_TOKEN` with no URL).

### Server behaviour (better-auth `deviceAuthorization`, configured in `convex/auth.ts`)

Config: `verificationUri: SITE_URL + "/device"`, `validateClient: id === "keel-cli"`, defaults
`expiresIn 30m`, `interval 5s`, `deviceCodeLength 40` (chars `a-zA-Z0-9`), `userCodeLength 8`
(each char = `"ABCDEFGHJKLMNPQRSTUVWXYZ23456789"[randomByte % 32]`).

Table `deviceCode`: `id, deviceCode, userCode, userId?, expiresAt, status ("pending"|"approved"|"denied"), lastPolledAt?, pollingInterval? (ms), clientId?, scope?`.

`POST /device/code` — client ≠ `keel-cli` → 400 `{"error":"invalid_client","error_description":"Invalid client ID"}`. Inserts `{status:"pending", userId:null, expiresAt: now+30m, pollingInterval: 5000, clientId, scope}`. Returns `expires_in: 1800`, `interval: 5`, `verification_uri: <SITE_URL>/device`, `verification_uri_complete: <SITE_URL>/device?user_code=<CODE>`; header `Cache-Control: no-store`.

`POST /device/token` — evaluated **in this order** (all errors HTTP 400 `{"error","error_description"}` unless noted):

1. client ≠ `keel-cli` → `invalid_grant` / `Invalid client ID`
2. no row for `device_code` → `invalid_grant` / `Invalid device code`
3. row `clientId` set and ≠ → `invalid_grant` / `Client ID mismatch`
4. `lastPolledAt` and `now - lastPolledAt < pollingInterval` → `slow_down` / `Polling too frequently` (**`lastPolledAt` is not updated**)
5. `lastPolledAt = now`
6. `expiresAt < now` → delete row; `expired_token` / `Device code has expired`
7. `pending` → `authorization_pending` / `Authorization pending`
8. `denied` → delete row; `access_denied` / `Access denied`
9. `approved` with `userId` → atomically consume (delete where `deviceCode` and `status = approved`); nothing consumed → `invalid_grant` / `Invalid device code`; user missing → 500 `server_error` / `User not found`; create a session (session-create hook sets `activeOrganizationId` = the user's membership org or null); 200 `{access_token: session.token, token_type: "Bearer", expires_in: <seconds left>, scope: ""}` with `Cache-Control: no-store`, `Pragma: no-cache`. The token goes to **one** poll only.
10. anything else → 500 `server_error` / `Invalid device code status`

`GET /device?user_code=X` (dashboard `/device` page; CI uses it) — strips `-` only; no row → 400 `invalid_request` / `Invalid user code`; expired → 400 `expired_token` / `User code has expired`; if a session is present and the row is `pending` with no `userId`, binds `userId` (conditional update `where status=pending and userId is null`). Returns `{"user_code": <as given>, "status": <row status>}`.

`POST /device/approve {userCode}` and `POST /device/deny {userCode}` — no session → 401 `unauthorized` / `Authentication required`; strip `-`; no row → 400 `invalid_request` / `Invalid user code`; expired → 400 `expired_token` / `User code has expired`; status ≠ pending → 400 `invalid_request` / `Device code already processed`; `userId` null → 400 `invalid_request` / ``Device code has not been claimed by a verifying session; call `GET /device` with the `user_code` while signed in before approving or denying``; `userId` ≠ caller → 403 `access_denied` / `You are not authorized to approve this device authorization` (deny: `…to deny this…`). Sets `status` approved/denied. 200 `{"success":true}`.

Sessions: better-auth defaults — token is a random string stored in the `session` table, lifetime
7 days, renewed (expiry pushed out) when used more than 1 day after the last refresh. The bearer
plugin accepts `Authorization: Bearer <token>`. `POST /sign-out` deletes the session.

Also used by CI (not the CLI): `POST /api/auth/sign-up/email {email,password,name[,invitationId]}`
→ `{token, user:{id,…}}` (first account free; later ones need a pending invitation for that
email, else 403 `Sign-up is by invitation. Ask a member for an invite link.`), and
`POST /api/auth/sign-in/email {email,password}` → `{token, user, …}`.

### `keel login` algorithm (auth.go)

1. `--wait` and `--no-wait` together → `USAGE "--wait and --no-wait are exclusive"`.
2. `loginTarget`:
   - No URL argument: `KEEL_URL` set or no saved instances → `USAGE "pass the dashboard URL: keel login <dashboard-url>"`. Else name from `target` (only the name is used: the instance is the **saved** one, never the `KEEL_TOKEN` copy).
   - URL argument: `normalizeURL` (error → `USAGE "\"<raw>\" is not a dashboard URL (http:// or https:// and a host)"`). Name = `--name` or host. Reuse the saved instance only if same name **and** same URL; else a fresh `{URL}`.
   - Convex URLs: `--convex-url`/`--convex-site-url` override saved values; if either is empty → `Discover` fills the missing one(s); trailing `/` trimmed.
3. `SetInstance(name, inst)`, `current = name`.
4. Instance has a token → `Connect`: `NOT_AUTHENTICATED` → drop the token and continue; other error → fail; success → `loggedIn` (prints `status: "loggedIn"`, saves `email` and `current`; nothing else changes).
5. Pending link not expired → one `PollLogin`: `NOT_AUTHENTICATED` → drop pending; other error → fail; token → step 9. `slow_down` is ignored here.
6. No token and (no pending or expired) → `StartLogin` (new link).
7. Save config.
8. Still no token:
   - `!--wait && (--no-wait || JSON || stdin not a TTY)` → print pending result, exit 0:
     `{"ok":true,"status":"pending","instance":…,"url":…,"approvalUrl":<pending.url>,"code":"ABCD-EFGH","expiresAt":…,"next":"Send approvalUrl to a person to approve, then carry on: the next keel command finishes the login (or wait for it: keel login --wait)"}`.
     Human: `To log in to <name>, open this link and approve (if you are an agent, send it to your human):\n\n  <url>\n\nCode ABCD-EFGH, valid until HH:MM. The next keel command after the approval finishes the login; keel login --wait waits for it.`
   - Else stderr progress `Open this link and approve to log in to <name>:\n\n  <url>\n\nCode ABCD-EFGH. Waiting for the approval (Ctrl-C stops waiting; the link stays valid until HH:MM)…`, then `waitForApproval`: sleep `interval`, poll; error or token → return; `slow_down` → `interval += 5s`; ctx done → `CANCELLED "Stopped waiting"`, fix `keel login --wait (the link stays valid)`.
9. Save the token and clear pending **before** anything else can fail (the token is handed out once), then `Connect` and `loggedIn`.

`prettyCode`: 8-char codes print as `ABCD-EFGH`. `loggedIn` warns `this account isn't in the install's organization yet; projects stay empty until a member invites it` when `organizations:current` is null.

### `finishLogin` (runs at the start of every signed-in command and `token`)

- Nothing to do if the instance has a token (or `KEEL_TOKEN`) or no pending link.
- Pending expired → `NOT_AUTHENTICATED "The login link expired before anyone approved it"`, fix `keel login <url>` (no poll).
- `PollLogin`; on `slow_down` wait `pending.interval` seconds (ctx-cancellable → `CANCELLED "Cancelled"`) and poll once more.
- Poll error → reload the config from disk: if the fresh instance (same name, same URL) now has a token, another concurrent `keel` run took it → adopt it, success. Else if the error is `NOT_AUTHENTICATED` and the saved pending device code is still this one → delete `pending`, save (later runs say "Not logged in" instead of polling a dead code). Return the error.
- No token → `AUTHORIZATION_PENDING "Waiting for someone to approve the login"`, fix `Open <pending.url> and approve (agents: send it to your human), then retry; or keel login --wait`.
- Token → save it, clear pending, stderr `Login approved; saved for <name>`, continue the command.

## A9. `translate()` — Convex errors → CLI errors (`internal/keel/api.go`)

Applied to every Convex call and to transport errors from discovery/auth. Order matters.

| # | Input | Code | Message | Fix |
| --- | --- | --- | --- | --- |
| 1 | already `*output.Error` | as is | | |
| 2 | ConvexError `"Not authenticated"` | `NOT_AUTHENTICATED` | `Session expired or signed out` | `keel login <url>` |
| 3 | starts with `You're not in an organization` (full server text: `You're not in an organization yet. Ask a member for an invite link.`) | `NO_ORGANIZATION` | `This account isn't in the install's organization` | `Ask a member for an invite link (account menu → Invite people), then keel login <url>` |
| 4 | `"A deployment is already running"` | `DEPLOYMENT_RUNNING` | `A deployment is already running in this environment` | `keel deployment get --wait` |
| 5 | `"Nothing to ship"` | `NOTHING_TO_SHIP` | `Nothing to ship: no service has staged changes` | `Stage a change first (keel var set …), or redeploy: keel redeploy <service>` |
| 6 | `"Node not found"` | `SERVICE_NOT_FOUND` | `Service not found` | `keel service list` |
| 7 | `"Connect Axiom to see traces"` or `"Sign in with Axiom again to turn on traces"` | `TRACES_OFF` | the server message | `Open Observability in the dashboard (<url>) and Sign in with Axiom` |
| 8 | `"Environment not found"` | `PROJECT_NOT_FOUND` | `Environment not found` | `keel project list` |
| 9 | prefix `Project "` and suffix `" already exists` | `NAME_TAKEN` | the server message | `Pick another name, or use it: keel link <slug between the quotes>` |
| 10 | suffix `" is already taken` | `NAME_TAKEN` | the server message | `Pick another name; keel service list shows the taken ones` |
| 11 | any other non-empty ConvexError string | `INVALID_INPUT` | the server message | `""` |
| 12 | FunctionError without string data (plain `throw new Error`, validator errors) | `SERVER_ERROR` | cleaned `errorMessage` | `""` |
| 13 | `ErrUnauthenticated` (HTTP 401 without envelope) | `NOT_AUTHENTICATED` | `Session expired or signed out` | `keel login <url>` |
| 14 | `context.Canceled` | `CANCELLED` | `Cancelled` | `""` |
| 15 | `net.Error` with `Timeout()` | `TIMEOUT` | `Timed out talking to <host>` | `Retry; check that <host> is up` |
| 16 | other `net.Error` (every `*url.Error`) | `NETWORK_ERROR` | `Can't reach <host>: <innermost wrapped error>` | `Check the URL, and that this machine is on the install's tailnet` |
| 17 | anything else | `SERVER_ERROR` | `%v` | `""` |

`<url>` is the dashboard URL; `<host>` is its host (falls back to the whole URL).

ConvexError strings the CLI can receive from the functions it calls (every one not in rows 2–10
surfaces as `INVALID_INPUT` with the message verbatim, so the Go server must keep the exact
text):

- `Project name: 1–60 characters`, `Project name needs a letter or digit (a-z, 0-9)` (projects:create)
- `Only services take a custom image`, `This node type has no runtime settings`, `<engine> is not a <type>`, `Image must look like repo/name:tag`, `Name: 1–40 chars, a-z 0-9 and - only`, `Port must be 1–65535`, `Replicas must be 0–20` (nodes:create)
- `Key: UPPER_SNAKE_CASE only`, `Value too long`, `<KEY> already exists` (variables:set)
- `Only services can be traced` (tracing:enable, tracing:localEnv)
- Axiom failures in traces:overview are wrapped as `ConvexError(err.message)` (e.g. `Axiom 403: …`) → `INVALID_INPUT`. logs:tail provider failures are plain `Error`s → `SERVER_ERROR`.

## A10. Commands

Conventions: "connect" = A5 `connect`; "project" = resolve project + environment; "service" =
resolve via `nodes:list`. Shapes below are the JSON **after** `"ok":true`. `Service`, `Deployment`
etc. are defined in A11.

| Command | Args / flags | Pre-connect validation | Calls (in order) | JSON result |
| --- | --- | --- | --- | --- |
| `login [dashboard-url]` | `--wait`, `--no-wait`, `--name`, `--convex-url`, `--convex-site-url` | A8 | discovery, device code/token, convex/token, `auth:getCurrentUser`, `organizations:current` | pending or `{"status":"loggedIn","instance","url","user":User,"organization":Org|null}` |
| `logout` | — | `KEEL_URL` set → `USAGE "KEEL_URL is set; logout only removes saved logins (unset KEEL_URL and KEEL_TOKEN instead)"` | `target` (no finishLogin); if token: `POST /api/auth/sign-out` (failure → `warning: couldn't revoke the session on the server (<err>); forgetting it here anyway`) | `{"instance"}` after `RemoveInstance` + save |
| `whoami` | — | | connect, `auth:getCurrentUser`, `organizations:current` | `{"instance","url","user":User,"organization":Org|null}` |
| `token` | — | | `target`, `finishLogin` (no Connect); no token → `NOT_AUTHENTICATED "Not logged in"` fix `keel login <url>` | `{"token"}` (human: the token alone) |
| `project list` (`projects`, `ls`) | — | | connect, `projects:list` | `{"projects":[{id,name,slug,environments,current:bool}]}`; `current` = matches `projectSlug`, or the only project |
| `project create <name>` | `--link` | 1 arg | connect, `projects:create {name}`; with `--link` save link `cwd → {session name, slug}` (save failure → same CONFIG_ERROR with message prefixed `Created project <slug>, but can't link it: `) | `{"project":Project,"url":"<dashboard>/p/<slug>","linked":"<cwd>"?}` |
| `link [project]` | uses `--project` when no arg (not `KEEL_PROJECT`, not existing link) | 0–1 | connect, `projects:list`, `pickProject` | `{"dir","instance","project":{id,slug,name}}` |
| `unlink` | — | | config only (no network) | `{"unlinked":bool,"dir"?}` |
| `status` | — | | connect, `projects:list`, `environments:summary`, `nodes:list`, `deployments:latest` (its `log` dropped) | `{"instance","url","project":{id,slug,name},"environment":Environment,"pendingChanges","servers","services":[Service],"latestDeployment":Deployment|null}` |
| `service list` (`services`, `ls`) | — | | connect, project, `nodes:list` | `{"services":[Service]}` |
| `service create <name>` | `--image` (required), `--port N`, `--replicas N` (sent only when the flag was given) | `--image` missing → `USAGE "--image is required: the image to run, e.g. --image nginx:alpine"`; non-int → flag USAGE | connect, project, `nodes:create {environmentId,type:"service",name,image,port?,replicas?}`, then `nodes:list` to find the returned id (gone → `SERVICE_NOT_FOUND "Service <name> was deleted right after it was created"`, fix `keel service list`) | `{"project":{id,slug,name},"service":Service}` |
| `service delete <service>` (`rm`) | `-y/--yes` | no `--yes` and not interactive → `USAGE "Deleting <x> can't be undone: pass --yes to confirm"` | connect, project, service, prompt `Delete <name>? It stops now and its variables are dropped. [y/N] ` (only `y`/`yes`, case-insensitive; else `CANCELLED "Nothing deleted"`), `nodes:remove {id}` | `{"service","id","deleted":true}` |
| `logs <service>` | `-n/--lines` (default 100, 1–1000), `-f/--follow` | lines out of range → `USAGE "--lines must be 1–1000"` | connect, project, service, `logs:tail {nodeId, tail: n}` | `{"service","source","lines":[LogLine]}` |
| `traces [service]` | `--since 15m|1h|24h|7d` (default 15m), `--search s` | bad since → `USAGE "--since must be one of 15m, 1h, 24h, 7d"` | connect, project, service?, `traces:overview {environmentId, range, nodeId?, search?}` | `{"service"?,"since","stats":Stats,"traces":[TraceSummary]}` |
| `run <service> -- <cmd…>` | — | `ArgsLenAtDash() != 1` or `< 2` args → `USAGE "usage: keel run <service> -- <command> [args...] (the command goes after --)"` | connect, project, service, `variables:list`, `tracing:localEnv` | none: exec (A10.3) |
| `tracing prompt [service]` | — | 0–1 | connect, project (error tolerated without a service arg), service?, `tracing:prompt {nodeId?, environmentId?}` | `{"prompt"}` (human: the text verbatim, no extra newline) |
| `tracing status <service>` | — | 1 | connect, project, service, `tracing:forNode` (null → `INVALID_INPUT "<name> is a <type>; only services can be traced"`, fix `Pick a service: keel service list`) | `{"service","staged","enabled","store","env":[{key,value,secret,overridden}]}` |
| `tracing enable|disable <service>` | — | 1 | connect, project, service; client check `type != "service"` → same INVALID_INPUT; `tracing:enable {nodeId, on}` | `{"service","tracing":bool,"staged":true}` |
| `var list <service>` (`vars`,`variables`; `ls`) | `--show-secrets` | 1 | connect, project, service, `variables:list` | `{"service","variables":[{key,value:string|null,resolved:string|null,secret}]}` |
| `var set <service> KEY=VALUE…` | `--secret` | each pair `strings.Cut(arg,"=")`, empty key/no `=` → `USAGE "\"<arg>\" is not KEY=VALUE"` | connect, project, service, `variables:list` (for current secret flags), then `variables:set` per pair in order | `{"service","set":[keys],"staged":true}` |
| `var delete <service> KEY…` (`rm`,`unset`) | — | 2+ args | connect, project, service, `variables:list`; any key missing → `VARIABLE_NOT_FOUND "<svc> has no variable <KEY>; nothing was deleted"`, fix `keel var list <svc>` (checked for all before deleting any); then `variables:remove` per key | `{"service","deleted":[keys],"staged":true}` |
| `ship [service…]` | `-d/--detach`, `--timeout` (default 10m) | | connect, project, `nodes:list` (names → ids), `deployments:start {environmentId, refresh:false, only?}`, then detach or await | `{"deployment":Deployment}` |
| `redeploy <service…>` | same | 1+ args | same with `refresh:true`, `only` always set | same |
| `deployment list <service>` (`deployments`; `ls`) | — | 1 | connect, project, service, `deployments:listForNode` (logs dropped) | `{"service","deployments":[Deployment]}` |
| `deployment get [id]` | `--wait`, `--timeout` (10m) | 0–1 | connect; id → `deployments:get {id}`, else project + `deployments:latest`; null → `DEPLOYMENT_NOT_FOUND` (`Deployment <id> not found` / `This project was never deployed`), fix `keel deployment list <service>`; `--wait` and status ≠ `success` → resolve services (best effort) and await | `{"deployment":Deployment}` (with log) |

### A10.1 Details that are easy to miss

- `var list` view: `secret` printed = `row.secret || row.resolvedSecret`. `value` is shown unless
  `!--show-secrets && row.secret`; `resolved` is shown unless `!--show-secrets && (row.secret ||
  row.resolvedSecret)`. Hidden → `null` in JSON, `••••••••` in the table; stderr notes
  `<n> secret value(s) hidden; --show-secrets reveals them`. Table: `KEY  VALUE` where value is
  `value` or `value  → resolved` when they differ.
- `var set` keeps each existing key's secret flag unless `--secret` was **given** (then `--secret`
  or `--secret=false` decides). A failure part-way: the error's message gets ` (after A, B went
  through)` and `Extra {"done":["A","B"]}` (only when ≥ 1 key was done and the error is an
  `*output.Error`). Same for `var delete`.
- `service create` prints the node as `nodes:list` renders it, so `staged: true`, `status:
  "pending"`, `replicas` = requested or 1, `port` = requested or 80.
- `ship` naming a service that has **no** staged change still deploys it (server: `only`
  bypasses `dirty`). Names of groups/volumes resolve (they are in the list) but the server drops
  non-deployable nodes → possibly `NOTHING_TO_SHIP`.
- `--detach`: after start, `deployments:get` once and print it; human `Started: <message>\nWatch it: keel deployment get <id> --wait`.
- `deployment get --wait` on an already `failed` deployment → await → `DEPLOYMENT_FAILED`
  (non-zero). Without `--wait` a failed deployment prints with exit 0.
- `tracing status` human: `Tracing for <svc>: on|off[ (staged changes; keel ship <svc> deploys them)]`;
  store `off` → `Nowhere to send spans yet: open Observability in the dashboard and Sign in with Axiom`;
  `old` → `This Axiom connection predates traces: Sign in with Axiom again on Observability`;
  when enabled, a table `KEY VALUE [(replaced by the service's own variables)]`.
- `whoami` human table: `User  email (name)`, `Organization  name (role)` or `none yet`, `Instance  name  url`.
- `status` human: `Project slug · env`, `Instance`, `Servers N`, `Staged N service(s) → keel ship` or `nothing`, `Last deploy status · message · <ago>`, blank line, services table `NAME TYPE STATUS IMAGE REPLICAS URL` (`status (staged)` when dirty, `-` for empty, `running/replicas`).
- `ago`: `<10s` "just now", `<1m` "Ns ago", `<1h` "Nm ago", `<48h` "Nh ago", else "Nd ago". Durations: `finishedAt - startedAt` rounded to 1s, `-` when running.

### A10.2 Deployment await (`deploy.go`)

```
names := map serviceId → name (from the nodes:list done before start)
ctx := WithTimeout(ctx, --timeout)          // default 10m; the server times out at 5m
loop: first iteration immediate, then every 1s
  d := deployments:get(id)
  if ctx.Err() → DeadlineExceeded: TIMEOUT "Still deploying after the --timeout; it keeps going on the server"
                 Canceled:         CANCELLED "Stopped waiting; the deployment keeps going on the server"
                 fix for both: "keel deployment get <id> --wait"
  d == nil → DEPLOYMENT_NOT_FOUND "Deployment <id> not found"
  first time: stderr "<Capitalized message> (<id>)"
  if printed > len(d.log): printed = 0        // server keeps the last 500 entries
  for each new log entry: stderr "  <name>: <text>" (unless text already starts with "<name>:") or "  <text>"
  running → continue
  success → print {"deployment": d}; human "Deployed: <message> in <duration>"
  failed  → failed := labels of steps with status "failed" and a serviceId
            error DEPLOYMENT_FAILED, message "Deployment failed: " + (failed joined ", " or d.message),
            fix "keel logs <failed[0]>" or "keel deployment get <id>", Extra {"deployment": d}
```

### A10.3 `keel run` (`run.go`, `run_unix.go`)

- Endpoint = `strings.TrimRight(convexSiteURL, "/") + "/otlp"` (the SDK appends `/v1/traces`;
  the server route is `POST /otlp/v1/traces`).
- `runEnv(shell, vars, tracing, endpoint)` builds the child env in layers, each key only if the
  previous layers left it unset: (1) the shell's own env (`os.Environ()`); (2) each service
  variable's **resolved** value, except values matching `\bsvc-[0-9a-z]{32}\b` (an overlay
  hostname: a Convex id is 32 chars of `[0-9a-z]`), which are skipped and reported by key (only if
  the shell didn't set them); (3) if tracing env is non-nil: `OTEL_EXPORTER_OTLP_ENDPOINT=<endpoint>`
  first, then the tracing map in sorted key order, skipping `OTEL_EXPORTER_OTLP_HEADERS` when the
  shell or service already set `OTEL_EXPORTER_OTLP_ENDPOINT` or
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (the ingest key only goes to Keel's endpoint).
- Warnings (stderr): skipped keys → `warning: left out A, B: it points|they point inside the cluster; set it|them in this shell to run against something reachable`; no tracing →
  `warning: no tracing variables: <reason> (open Observability in <url>)`; with tracing →
  progress `<svc>: tracing to <endpoint> as deployment.environment.name=local; keel traces <svc> shows the requests`.
- Exec: `exec.LookPath(argv[0])` failure or `Start` failure → `USAGE "Can't run <cmd>: <err>"`, fix
  `Check the command after --`. stdin/stdout/stderr inherited.
- Signals: keel `signal.Notify`s SIGINT, SIGTERM, SIGHUP, SIGQUIT. stdin is a TTY → child shares the
  foreground process group, so SIGINT is not forwarded (the terminal already sent it), other signals
  go to the child. Not a TTY → child gets its own process group (`Setpgid`) and every signal goes to
  the whole group (`kill(-pid, sig)`). Non-unix: no group, `Process.Signal`.
- Exit: child killed by signal → `childExit{128+signal}`; else `childExit{exitCode}`; nothing printed.

### A10.4 `logs` details

- One-shot: stable-sort lines by `time`, keep the last `n` (Docker tails every task including old
  replicas, so the server may return more). Human: `YYYY-MM-DD HH:MM:SS  <text>` in local time; no
  lines → stderr `No log lines for <svc> yet`.
- `--follow`: prints the initial (cut) tail as events, marks the older fetched lines as seen, then
  every 2s polls `logs:tail` with `tail: 200` under a 15s per-poll timeout. `TIMEOUT`/`NETWORK_ERROR`
  → `warning: <msg>; retrying`; other errors end the command. Ctx done → return nil (exit 0).
  JSON event per line: `{"service","time","stream","task"?,"text"}`.
- De-dup (`lineSet`): keeps `last` time and the set of keys (`task\0stream\0text`) printed at `last`.
  A line older than `last` is dropped; newer resets the set; equal time prints only if its key is new.

### A10.5 `traces` details

`traces:overview` → CLI renames `duration` → `durationMs`, `stats.p50/p95/p99` →
`p50Ms/p95Ms/p99Ms`. Human: `TIME SERVICE REQUEST STATUS DURATION SPANS` (+ `local` column),
status = `httpStatus` or `error` or `ok`; ms formatting `<1` `%.2fms`, `<1000` `%.1fms`, else a
Go duration rounded to 10ms. stderr summary: `<n> request(s) in the last <since> · <errors> failed · p50 <x> · p95 <y> · trace ids with --json`. Empty → stderr `No requests from <svc|this project> in the last <since>`.

## A11. Convex functions the CLI calls

Auth rule shorthand: **member** = signed-in user whose Better Auth `member` row's
`organizationId` equals the project's `organizationId` (`ownedProject`/`ownedEnvironment`/
`ownedNode` return null otherwise). One organization per install.

| Function | Kind | Args | Returns (fields the CLI reads) | Access / errors | Side effects |
| --- | --- | --- | --- | --- | --- |
| `auth:getCurrentUser` | query | `{}` | user doc or `null`; CLI reads `_id`, `email`, `name` | none; null when signed out | — |
| `organizations:current` | query | `{}` | `{id,name,slug,role}` or `null` | null when no membership | — |
| `projects:list` | query | `{}` | `[{id,name,slug,environments:[{id,name,isProduction}]}]`, environments production-first | no membership: signed out → `Not authenticated`; an organization exists → `You're not in an organization yet. Ask a member for an invite link.`; none exists → `[]` | — |
| `projects:create` | mutation | `{name: string}` | `{id,name,slug,environments:[{id,name:"production",isProduction:true}]}` | `joinOrFound` (below); `Project name: 1–60 characters` (trimmed, JS length); `Project name needs a letter or digit (a-z, 0-9)`; `Project "<slug>" already exists` | may found org + owner membership; inserts project + production environment |
| `environments:summary` | query | `{environmentId}` | `{pendingChanges, counts:{status:n}, servers}` or `null` | null when not member | — |
| `nodes:list` | query | `{environmentId}` | `[view]`; CLI reads `id,name,type,status,image?,port?,replicas,running,dirty,publicUrl?,error?` and drops `type=="group"` | `[]` when not member | — |
| `nodes:create` | mutation | `{environmentId, type, name?, position?, image?, engine?, port?, replicas?, deploy?}` (CLI: type `service`, name, image, port?, replicas?) | `{id, deploymentId?}` | `Environment not found`; validation (A9 list); `"<name>" is already taken` (checked **before** the name regex) | inserts node (`desired {image, revision:0, replicas ?? 1, port ?? 80}`, `dirty:true`, position right of the rightmost top-level node: `x + (config.width ?? 220) + 60`, same `y`, else `0,0`) |
| `nodes:remove` | mutation | `{id}` | — | **silently returns** when the node is missing or not the caller's | marks referrers dirty, deletes its variables, un-parents children, cancels `observeScheduled`, deletes the row; if endpoints → `proxy.sync`; if `desired` → `swarm.remove {id}` and `reconcile.run` |
| `variables:list` | query | `{nodeId}` | `[{key,value,resolved,secret,resolvedSecret,parts}]` | `[]` when not member | — |
| `variables:set` | mutation | `{nodeId,key,value,secret,previousKey?}` (CLI never sends previousKey) | — | `Node not found`; `Key: UPPER_SNAKE_CASE only` (`^[A-Z_][A-Z0-9_]{0,63}$`); `Value too long` (> 4096 JS chars) | upsert by key, `dirty:true` on the node, `markReferrersDirty` (transitive) |
| `variables:remove` | mutation | `{nodeId,key}` | — | `Node not found`; missing key → no-op | delete, `dirty:true`, `markReferrersDirty` |
| `logs:tail` | action | `{nodeId, tail?}` (default 200, clamped 1–1000) | `{source:"docker"|"axiom", lines:[{time,text,stream,task}], replicas}` | `Node not found` | reads Axiom (org sink) or `docker service logs svc-<id>` |
| `traces:overview` | action | `{environmentId, range:"15m"|"1h"|"24h"|"7d", search?, nodeId?}` (search cut to 200) | `{source,from,to,bucketMs,stats:{requests,errors,p50,p95,p99},buckets,traces:[{traceId,name,service,kind,start,duration,httpStatus,spans,errors,error,local}]}` | `Environment not found`; `Connect Axiom to see traces`; `Sign in with Axiom again to turn on traces`; `Node not found` (node not in the env) | Axiom APL query |
| `tracing:forNode` | query | `{nodeId}` | `null` (not member / not a service / no desired) or `{enabled, traces:"off"|"old"|"on", env:[{key,value,secret,overridden}]}` (ingest key masked `keel_otlp_…<last4>`) | — | — |
| `tracing:enable` | action | `{nodeId, on}` | — | `Node not found`; `Only services can be traced`; when `on`: the two TRACES_OFF messages | `on`: ensure the environment's OTLP ingest key; toggles `desired.tracing`, `dirty:true` (no-op if unchanged) |
| `tracing:localEnv` | action | `{nodeId}` | `{env: {KEY: value} | null, reason: string | null}` | `Node not found`; `Only services can be traced` | ensures the ingest key when traces are `on` |
| `tracing:prompt` | query | `{nodeId?, environmentId?}` | string (the agent prompt, `tracingPrompt.ts`) | never throws; names service/project only when visible to the caller | — |
| `deployments:start` | mutation | `{environmentId, only?: Id<nodes>[], refresh?: bool}` | deployment id (string) | `Environment not found`; `A deployment is already running`; `Nothing to ship` | bumps `desired.revision`, clears `dirty`/`applyError`, sets `shippedAt`; inserts deployment `{status:"running", message, steps:[…nodes, {label:"health checks"}], log:[]}`; schedules `swarm.apply {id, deploymentId, pull: refresh}` per node and `reconcile.timeoutDeployment` after 5 min |
| `deployments:get` | query | `{id: string}` (any string; malformed → null) | doc or `null` | null when not member | — |
| `deployments:latest` | query | `{environmentId}` | newest doc (by creation) or `null` | null when not member | — |
| `deployments:listForNode` | query | `{nodeId}` | newest 50 of the environment, filtered to those with a step for the node, first 20 | `[]` when not member | — |

`joinOrFound` (projects:create, also ensureDefault): requires a user (`Not authenticated`); has a
membership → use it; else if any organization exists → `You're not in an organization yet. Ask a
member for an invite link.`; else create organization `{name:"Default", slug:"default"}` and a
`member {role:"owner"}` for the caller.

`slugOf(name)`: NFKD normalize → strip U+0300–U+036F → lowercase → replace runs of
`[^a-z0-9]+` with `-` → take the first 40 chars → trim leading/trailing `-`. (`Acme API` → `acme-api`.)

`deployments:start` message: `verb ?? (only ? (refresh ? "redeploy" : "deploy") : "ship")` + `" "`
+ affected names joined `", "`. Affected = nodes with `desired` and type in
`{service,database,cache}` that are in `only` (if given) else `dirty`.

### CLI-side types (their JSON names are the contract)

- `User {id,email,name}`, `Organization {id,name,slug,role}`, `Project {id,name,slug,environments}`, `Environment {id,name,isProduction}`.
- `Service {id,name,type,status,image?,port?(0 omitted),replicas,running,staged,publicUrl?,error?}` — `staged` = server `dirty`; `status` ∈ `healthy|done|deploying|stopping|error|stopped|pending`; `type` ∈ `service|database|cache|volume`.
- `Deployment {id,status:"running"|"success"|"failed",message,startedAt,finishedAt?,steps:[{serviceId?,label,status:"pending"|"running"|"done"|"failed"}],log?:[{at,serviceId?,text}]}` — renamed from the doc's `_id`, `nodeId`.
- `LogLine {time,stream:"stdout"|"stderr",task?,text}`, tail `{source,lines}`.
- `TraceSummary {traceId,name,service,start,durationMs,httpStatus:int|null,spans,errors,error,local}`; `TraceStats {requests,errors,p50Ms,p95Ms,p99Ms}` (null when no requests).
- `Tracing {enabled, store:"off"|"old"|"on", env:[{key,value,secret,overridden}]}`.

### Tests that pin this behaviour (port them)

`api_test.go` (translate table incl. fixes), `auth_test.go` keel (device code/token request
bodies, Origin header, outcome table), `cli/auth_test.go` (finishLogin: pending, approved saves
token, slow_down re-polls once, expired does not poll, token taken by a concurrent run, nothing
pending, denied forgets the link, login keeps the token when Connect fails, loginTarget ignores
KEEL_TOKEN), `cli_test.go` (pickProject, USAGE before connecting for 16 arg lists, runEnv,
findService, normalizeURL, lineSet, jsonArg), `config_test.go` (LinkFor walks up, 0600, round
trip, RemoveInstance), `convex_test.go` (nil args → `{}`, error cleaning, 401 → unauthenticated),
`output_test.go` (`ok` first, no HTML escaping, JSON failure shape, human failure keeps stdout empty).

## A12. Web image and `/config.js`

`apps/web/Dockerfile` (context = repo root):

1. `node:24-slim` + bun copied from `oven/bun:1`; `bun x turbo@2.10.12 prune web --docker`.
2. Build stage: copy `out/json`, `bun install --frozen-lockfile --ignore-scripts`, copy `out/full`,
   `NODE_ENV=production`, `cd apps/web && bun x varlock codegen && bun run build` (vite → `dist`).
3. `nginx:1.29-alpine`: `dist` → `/usr/share/nginx/html`, `nginx.conf` → `conf.d/default.conf`,
   `docker-entrypoint.sh` → `/docker-entrypoint.d/40-keel-config.sh`. `EXPOSE 80`.

Entrypoint: requires `KEEL_CONVEX_URL` and `KEEL_CONVEX_SITE_URL`; each must start with
`http://`/`https://` and contain none of `" \ < >` or space (else exit 1 with
`40-keel-config: …`); writes `/usr/share/nginx/html/config.js`.

nginx: `listen 80`, gzip for text/css/json/js/svg; `/config.js` → `Cache-Control: no-store`;
`/assets/` → `Cache-Control: public, max-age=31536000, immutable`; SPA fallback
`try_files $uri $uri/ /index.html`. `index.html` loads `<script src="/config.js">` before the app.

## A13. `install.sh`

Usage: `curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash`.
`set -Eeuo pipefail`. Everything runs from `main "$@"` at the last line (a truncated download
executes nothing).

### Options

| Variable | Default | Saved in `.env` | Notes |
| --- | --- | --- | --- |
| `KEEL_TAILSCALE_AUTHKEY` | – | no | Written to a temp file, `tailscale up --auth-key=file:<tmp>`, file removed |
| `KEEL_ADDR` | tailnet IPv4 | as `KEEL_ADDR_OVERRIDE` | Explicit value sticks across runs and skips Tailscale; otherwise re-read every run. Cannot be cleared by passing empty (falls back to saved) |
| `KEEL_PUBLIC_IP` | detected | as `KEEL_PUBLIC_IP_OVERRIDE` (+ last value in `KEEL_PUBLIC_IP`) | Explicit sticks; detected one re-detected every run, last value kept if detection fails |
| `KEEL_ACME_EMAIL` | – | yes | Cannot be cleared by passing empty |
| `KEEL_VERSION` | `latest` | yes | Tag for every Keel image |
| `KEEL_WEB_PORT` | `80` | yes | |
| `KEEL_IMAGE_PREFIX` | `ghcr.io/thallesp` | yes | |
| `KEEL_JSON` | – | no | `1` → JSON on stdout |
| `KEEL_DIR` | `/opt/keel` | no | |
| `KEEL_REF` | `main` | no | Git ref for fetched files |
| `KEEL_SRC` | – | no | Local checkout instead of fetching |
| `KEEL_PULL` | `1` | no | `0` → no pulls |
| `CONVEX_SELF_HOSTED_ADMIN_KEY` | saved | yes | Env wins over saved |

### State file `$KEEL_DIR/.env` (0600; dir 0700)

Written by `save_state` through `umask 077` + `.env.tmp` + `mv`. Every value single-quoted, no
escaping (a `'` in a value breaks it). Read back by `saved KEY` =
`sed -n "s/^KEY='\{0,1\}\([^']*\)'\{0,1\}$/\1/p" | tail -1`. Keys, in order: `KEEL_ADDR`,
`KEEL_ADDR_OVERRIDE`, `KEEL_PUBLIC_IP`, `KEEL_PUBLIC_IP_OVERRIDE`, `KEEL_ACME_EMAIL`,
`KEEL_VERSION`, `KEEL_WEB_PORT`, `KEEL_IMAGE_PREFIX`, `INSTANCE_SECRET`, `BETTER_AUTH_SECRET`,
`KEEL_WORKER_TOKEN`, `CONVEX_SELF_HOSTED_ADMIN_KEY`. Secrets are `rand_hex` = 32 random bytes as
64 hex chars, generated once and never rotated (CI asserts this). The same file is the compose
`--env-file`.

### Steps

| # | Function | What it does | Failure (`error:` / `fix:`) |
| --- | --- | --- | --- |
| 0 | `main` | Resolve options (table above); `KEEL_ADDR=""` until step 3 | |
| 1 | `preflight` | uid 0; `uname -s == Linux`; arch `x86_64|amd64|aarch64|arm64`; `curl` present; MemTotal < 1,900,000 kB → `warning: less than 2 GB RAM; the control plane may run out of memory` | `must run as root` / `curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh \| sudo bash`; `Keel runs on Linux (got <os>)`; `unsupported CPU architecture <m>` / `use an amd64 or arm64 server`; `curl is required` / `apt-get install -y curl (or your distro's equivalent)` |
| 2 | `ensure_docker` | No `docker` → `curl -fsSL https://get.docker.com \| sh`; `docker info`; `docker compose version` | `the Docker daemon is not running` / `systemctl enable --now docker`; `the Docker Compose plugin is missing` / `install docker-compose-plugin, or reinstall Docker from https://get.docker.com` |
| 3 | `ensure_tailscale` | Override → `KEEL_ADDR=override`, log `using KEEL_ADDR=… (Tailscale skipped)`. Else install Tailscale if missing (`tailscale.com/install.sh`); no `tailscale ip -4` → auth key path or `tailscale up --timeout=15m` (prints a login URL on stderr); `KEEL_ADDR` = first `tailscale ip -4` | `tailscale up failed` / `check the auth key at https://login.tailscale.com/admin/settings/keys`; `Tailscale login did not complete` / `re-run with KEEL_TAILSCALE_AUTHKEY=tskey-auth-... (https://login.tailscale.com/admin/settings/keys)`; `no tailnet IPv4 address` / `check 'tailscale status'` |
| 4 | `detect_public_ip` | Override → use it. Else try `curl -4fsS --max-time 5` on `https://api.ipify.org`, `https://ifconfig.me/ip`, `https://icanhazip.com`, accept the first matching `^[0-9]{1,3}(\.[0-9]{1,3}){3}$`; none → keep the saved value with a warning (`could not detect this server's public IPv4; keeping <ip> from the last run` / `…; set KEEL_PUBLIC_IP and re-run to expose services`). Never fatal | |
| – | | `SITE_URL = http://$KEEL_ADDR` (+ `:$KEEL_WEB_PORT` unless 80) | |
| 5 | `write_state` | `mkdir -p $KEEL_DIR/scripts`, `chmod 700 $KEEL_DIR`; fetch `deploy/compose.yml` (644), `scripts/bootstrap-swarm.sh` (755), `scripts/deploy-worker.sh` (755) via `install -m` from `KEEL_SRC` or `curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/$KEEL_REF/<path> -o <dest>.tmp` + chmod + mv; generate missing secrets; `save_state` | `could not download <path> from ref <ref>` / `check KEEL_REF and network access to raw.githubusercontent.com` |
| – | | `export CONVEX_SELF_HOSTED_ADMIN_KEY SITE_URL BETTER_AUTH_SECRET KEEL_WORKER_TOKEN KEEL_PUBLIC_IP KEEL_ACME_EMAIL` | |
| 6 | `start_swarm` | `TAILSCALE_IP=$KEEL_ADDR bootstrap-swarm.sh --swarm-only` (A14) — before compose because `proxy` joins the `keel` overlay | ERR trap |
| 7 | `start_control_plane` | If `KEEL_PULL=1`: `compose pull --quiet`, `docker pull -q` of `keel-functions` and `keel-worker`. `compose up -d --wait --wait-timeout 180 --remove-orphans`. No admin key → `compose exec -T backend ./generate_admin_key.sh \| grep '\|' \| tail -1`, save. `functions check`. `functions deploy` | `could not pull the control-plane images` / `check KEEL_VERSION and access to ghcr.io`; `the control plane did not become healthy` / `docker compose -p keel logs backend web proxy`; `could not generate the Convex admin key` / `docker compose -p keel logs backend`; `the Convex backend rejected the stored admin key` / `delete CONVEX_SELF_HOSTED_ADMIN_KEY from <ENV_FILE> and re-run`; `pushing functions failed` / `see the output above; the backend needs outbound access to registry.npmjs.org` |
| 8 | `start_workers` | `TAILSCALE_IP=$KEEL_ADDR KEEL_URL=http://$KEEL_ADDR:3211 KEEL_WORKER_IMAGE=$PREFIX/keel-worker:$VERSION bootstrap-swarm.sh` (A14; `KEEL_WORKER_TOKEN` is already exported) | ERR trap |
| 9 | `check_health` | `curl -fsS http://$KEEL_ADDR:3210/version`; `curl -fsS $SITE_URL/config.js \| grep -q $KEEL_ADDR`; `curl -fsS -H 'Authorization: Bearer <token>' http://$KEEL_ADDR:3211/worker/config` (header via stdin `-H @-`); up to 60×2s: `docker service ls --filter name=keel-worker --format '{{.Replicas}}'` must be `n/n` with `n != 0` | `Convex API is not answering on <addr>:3210` / `docker compose -p keel logs backend`; `the dashboard is not serving its config on <SITE_URL>` / `docker compose -p keel logs web`; `Convex does not accept the worker token` / `re-run install.sh; it re-sends KEEL_WORKER_TOKEN`; `keel-worker is not running (replicas: <r|none>)` / `docker service ps keel-worker --no-trunc` |
| 10 | output | stderr: `Keel is running.` block with Dashboard, Convex `http://<addr>:3210`, State `<.env> (secrets, 0600)`, `Open the dashboard from any device on your tailnet and sign up.`, `Upgrade: re-run the install command.`, `Exposed services are served from <public ip|this server>: let ports 80 and 443 (TCP) through its firewall or router, plus each TCP/UDP port you expose.` stdout (JSON mode) below | |

`functions <cmd>` = `docker run --rm --network host -e CONVEX_SELF_HOSTED_URL=http://$KEEL_ADDR:3210 -e CONVEX_SELF_HOSTED_ADMIN_KEY -e SITE_URL -e BETTER_AUTH_SECRET -e KEEL_WORKER_TOKEN -e KEEL_PUBLIC_IP -e KEEL_ACME_EMAIL $PREFIX/keel-functions:$VERSION <cmd>`.
`compose <args>` = `docker compose -p keel --env-file $KEEL_DIR/.env -f $KEEL_DIR/compose.yml <args>`.

### Output contract

- Progress: `==> <msg>` (bold arrow) on stderr; warnings `warning: <msg>` on stderr.
- `die msg [fix]`: stderr `error: <msg>` + `fix:   <fix>`; with `KEEL_JSON=1` stdout
  `{"ok":false,"error":<msg>,"fix":<fix|"">}`; `exit 1`.
- `trap … ERR` → `die "install.sh failed at line $LINENO: $BASH_COMMAND" "re-run with the same command; it is safe to repeat"`.
- Success JSON (only with `KEEL_JSON=1`):
  `{"ok":true,"url":"<SITE_URL>","convexUrl":"http://<addr>:3210","convexSiteUrl":"http://<addr>:3211","version":"<KEEL_VERSION>","stateDir":"<KEEL_DIR>","publicIp":"<ip or \"\">"}`.
- `json_str` only escapes `\` and `"`: control characters (a newline in `$BASH_COMMAND`) produce
  invalid JSON. The Go-era installer should JSON-encode properly.

### Verification (README contract)

`curl -fsS "$convexUrl/version"`, `curl -fsS "$url/config.js"`, `docker compose -p keel ps`
(backend, web, proxy healthy), `docker service ls --filter name=keel-worker` (n/n).

### Upgrade / uninstall

Upgrade = re-run (secrets kept, images re-pulled, functions re-pushed + `migrations:run`, worker
updated in place). Pin with `KEEL_VERSION` (`1.2.3`, `sha-abc1234`; remembered). Uninstall:
`docker compose -p keel -f /opt/keel/compose.yml down`; `docker service rm keel-worker`;
`docker service ls -q --filter label=keel.service | xargs -r docker service rm`. Data kept:
volumes `keel_convex-data`, `keel_proxy-data`, user volumes; `/opt/keel`.

## A14. Swarm bootstrap and worker deploy scripts

`scripts/bootstrap-swarm.sh [--swarm-only]` (idempotent):

1. `TAILSCALE_IP` default `tailscale ip -4`.
2. `docker info --format '{{.Swarm.LocalNodeState}}' != active` → `docker swarm init --advertise-addr $IP --listen-addr $IP:2377 --data-path-addr $IP --default-addr-pool 10.200.0.0/16 --default-addr-pool-mask-length 24`.
3. No network `keel` → `docker network create -d overlay --attachable --opt com.docker.network.driver.mtu=1200 keel`.
4. Prints `swarm ready, manager advertised on <ip>`; `--swarm-only` exits here; else runs `deploy-worker.sh`.

`scripts/deploy-worker.sh` (idempotent):

- Sources `$ROOT/infra/worker/.env.local` if present (`ROOT` = script dir/..; on an install `/opt/keel`).
- `KEEL_URL` default `http://<tailnet ip>:3211`; `KEEL_REGISTRY` default empty. No `KEEL_WORKER_TOKEN` (dev only) → `openssl rand -hex 32`, written to `.env.local` (0600) and `convex env set KEEL_WORKER_TOKEN` via stdin.
- Image: `KEEL_WORKER_IMAGE` if set, else build `apps/worker` tagged `[REGISTRY/]keel-worker:<sha256 of Dockerfile, package.json, tsconfig.json, sorted src files, first 12 hex>` (push when a registry is set).
- Secret `keel-worker-token-<first 12 hex of sha256(token)>` created if missing (from stdin).
- Pin by digest: first `RepoDigests` entry → `WANT=image@sha256:…`; flags `--no-resolve-image` (+ `--with-registry-auth` when a digest exists).
- Service `keel-worker` missing → `docker service create --detach --quiet --name keel-worker --mode global --network host --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly --mount type=volume,src=keel-worker-state,dst=/var/lib/keel-worker --secret source=<secret>,target=keel_worker_token --env KEEL_URL=<url> --restart-condition any --restart-delay 2s --stop-grace-period 10s <WANT>`.
- Exists → compare current image, secret name, `KEEL_URL` env; all equal → `service keel-worker up to date`; else `docker service update --image <WANT> --secret-rm <old> --secret-add source=<new>,target=keel_worker_token --env-add KEEL_URL=<url>` and `docker secret rm <old>` (best effort).
- Removes the legacy `keel-events` service and its `keel-events-*` configs / `keel-events-token-*` secrets.

The worker reads `KEEL_URL`, the token from `KEEL_WORKER_TOKEN` or `/run/secrets/keel_worker_token`,
`DOCKER_SOCKET` (default `/var/run/docker.sock`), `KEEL_STATE` (default
`/var/lib/keel-worker/state.json`), `KEEL_CONFIG_POLL_MS` (default 30000). It calls
`POST /worker/events` and `GET /worker/config` (bearer = worker token).

> **Go now:** `keel agent` reads `KEEL_URL`, the token the same way, `KEEL_STATE` (default `/var/lib/keel-agent/state.json`), `KEEL_CONFIG_POLL_MS` and an optional `KEEL_TS_AUTHKEY`; there is no `DOCKER_SOCKET` (moby's `client.FromEnv`, so `DOCKER_HOST`). It still calls `POST /worker/events` (JSON arrays only) and `GET /worker/config`.

`scripts/dev-https.sh` (dev only): `tailscale serve --bg --https=443 http://127.0.0.1:3001`,
`--https=8443 → 3210`, `--https=10000 → 3211`; rewrites `VITE_CONVEX_URL`/`VITE_CONVEX_SITE_URL`
in `apps/web/.env`; `convex env set SITE_URL https://<host>`.

## A15. `deploy/compose.yml` (project `keel`)

| Service | Image | Ports | Volumes | Env | Other |
| --- | --- | --- | --- | --- | --- |
| `backend` | `ghcr.io/get-convex/convex-backend:5c7cb5bc7db457290f1769f95f1d1340912f7fd7` (pinned) | `$KEEL_ADDR:3210:3210`, `$KEEL_ADDR:3211:3211` | `convex-data:/convex/data`, `/var/run/docker.sock:/var/run/docker.sock`, `proxy-admin:/run/keel-proxy` | `INSTANCE_NAME=keel`, `INSTANCE_SECRET`, `CONVEX_CLOUD_ORIGIN=http://$KEEL_ADDR:3210`, `CONVEX_SITE_ORIGIN=http://$KEEL_ADDR:3211`, `DISABLE_METRICS_ENDPOINT=true` | `restart: unless-stopped`, `stop_signal: SIGINT`, `stop_grace_period: 10s`, health `curl -f http://localhost:3210/version` every 5s, start 10s |
| `web` | `$PREFIX/keel-web:$VERSION` | `$KEEL_ADDR:${KEEL_WEB_PORT:-80}:80` | – | `KEEL_CONVEX_URL=http://$KEEL_ADDR:3210`, `KEEL_CONVEX_SITE_URL=http://$KEEL_ADDR:3211` | `depends_on backend: service_healthy`; health `wget -q --spider http://127.0.0.1/` 5s |
| `proxy` | `$PREFIX/keel-proxy:$VERSION` | none (listeners opened in the host netns) | `/proc/1/ns/net:/run/hostns/net:ro`, `proxy-admin:/run/keel-proxy`, `proxy-data:/data` (certs, ACME accounts), `proxy-config:/config` (autosaved config for `--resume`) | – | `cap_drop: [ALL]`, `cap_add: [SYS_ADMIN, NET_BIND_SERVICE]`, `no-new-privileges:true`, networks `[keel]`, health `test -S /run/keel-proxy/admin.sock` |

Networks: `keel` external (the Swarm overlay). Volumes: `convex-data`, `proxy-admin`,
`proxy-data`, `proxy-config`. `${KEEL_ADDR:?}` and `${INSTANCE_SECRET:?}` are required.

Other images: `keel-proxy` = `caddy:2.11.7-builder` + `xcaddy build v2.11.7 --with github.com/mholt/caddy-l4@v0.1.2 --with github.com/ThallesP/keel/apps/proxy=/src`, run `caddy run --resume --config /etc/keel-proxy/caddy.json` where the config is only `{"admin":{"listen":"unix//run/keel-proxy/admin.sock|0600"}}`. `keel-worker` = `oven/bun:1-alpine`, `CMD bun src/index.ts`.

### `keel-functions` image (`deploy/functions.Dockerfile`, `deploy/functions-entrypoint.sh`)

Image: `node:24-slim` + bun; `turbo prune @my-better-t-app/backend --docker`; `bun install
--frozen-lockfile --ignore-scripts`; workdir `/app/packages/backend`; entrypoint `keel-functions`,
default `deploy`. Requires `CONVEX_SELF_HOSTED_URL`, `CONVEX_SELF_HOSTED_ADMIN_KEY`; unsets
`CONVEX_DEPLOYMENT`, `CONVEX_DEPLOY_KEY`.

- `check`: `convex env list` (exit 0 iff the admin key is accepted).
- `deploy`: requires `SITE_URL`, `BETTER_AUTH_SECRET`, `KEEL_WORKER_TOKEN` (`keel-functions: <NAME> is required`, exit 1); writes them to a mktemp file (0600, removed on exit); `KEEL_PUBLIC_IP`/`KEEL_ACME_EMAIL` appended when set, else `convex env remove <NAME>` (ignore errors); `convex env set --from-file <file> --force`; `convex deploy --typecheck disable --codegen disable`; `convex run migrations:run`.

`migrations:run` (internalMutation, idempotent; returns `{quickTunnelsConverted, redisPasswords, domainsMoved}`): (1) nodes with legacy `public`/`ingress` fields → cleared; services with `public`, a port, a known public IP and no endpoints get one `http` endpoint on the default domain, status `starting`; (2) Redis cache nodes without `REDIS_PASSWORD` get a 20-char secret one, `dirty:true`, referrers dirty; (3) with a public IP, every `http` endpoint whose domain is a default domain for an old IP moves to the new IP (status `starting`); (4) schedules `swarm.removeLegacyTunnels` and `proxy.sync`.

## A16. Control-plane environment (Convex deployment env today)

| Name | Set by | Meaning / default |
| --- | --- | --- |
| `SITE_URL` | install (functions deploy) | Dashboard origin. better-auth `trustedOrigins: [SITE_URL]`; device `verificationUri = SITE_URL + "/device"` |
| `CONVEX_SITE_URL` | Convex system (= `CONVEX_SITE_ORIGIN`) | better-auth `baseURL`; default OTLP endpoint `<it>/otlp`; proxy report URL `<it>/proxy/events` |
| `BETTER_AUTH_SECRET` | install | better-auth signing secret |
| `KEEL_WORKER_TOKEN` | install | Bearer for `/worker/*` and `/proxy/events` (constant-time compare) |
| `KEEL_PUBLIC_IP` | install (removed when empty) | Names default domains and tcp/udp addresses |
| `KEEL_ACME_EMAIL` | install (removed when empty) | ACME email → LE + ZeroSSL issuers |
| `KEEL_ACME_CA` | dev/CI only | Sole ACME CA (e.g. LE staging) |
| `KEEL_OTLP_URL` | optional | Override of the endpoint given to traced services |
| `KEEL_PROXY_SOCKET` | optional | Default `/run/keel-proxy/admin.sock` |
| `KEEL_PROXY_REPORT_URL` | optional | Default `<CONVEX_SITE_URL>/proxy/events` |
| `KEEL_ALLOW_LOCAL_SINKS`, `KEEL_AXIOM_AUTH_URL`, `KEEL_AXIOM_API_URL` | dev only | Mock Axiom (`KEEL_AXIOM_AUTH_URL` default `https://authorization.axiom.co`) |

## A17. CI and release

### `ci.yml` (on `pull_request`, and `workflow_call` from images.yml; `permissions: contents: read`)

| Job | Runner | Steps |
| --- | --- | --- |
| `check` | ubuntu-24.04 | checkout v5; `oven-sh/setup-bun@v2` with `bun-version-file: package.json` (bun 1.4.0); `bun install --frozen-lockfile`; `bun run check-types` (turbo: web = `vite build && tsc --noEmit`, ui and worker = `tsc --noEmit`) |
| `cli` | ubuntu-24.04, workdir `apps/cli` | setup-go v6 (`go-version-file: apps/cli/go.mod`, cache `apps/cli/go.sum`); `test -z "$(gofmt -l .)"`; `go vet ./...`; `go test ./...` |
| `proxy` | ubuntu-24.04, workdir `apps/proxy` | same three Go checks |
| `install` | ubuntu-24.04 | see below |

`install` job:

1. checkout; `docker/setup-buildx-action@v3`.
2. Build: `docker buildx build --load -t local/keel-web:ci -f apps/web/Dockerfile .`; `local/keel-functions:ci -f deploy/functions.Dockerfile .`; `local/keel-worker:ci apps/worker`; `local/keel-proxy:ci apps/proxy`.
3. `$GITHUB_ENV`: `KEEL_ADDR=$(hostname -I | awk '{print $1}')` (Swarm won't advertise on loopback), `KEEL_SRC=$PWD`, `KEEL_IMAGE_PREFIX=local`, `KEEL_VERSION=ci`, `KEEL_PULL=0`, `KEEL_JSON=1`.
4. `sudo -E ./install.sh > first.json`; `jq -e '.ok == true and .url == "http://" + env.KEEL_ADDR'`.
5. Auth: with `Origin: http://$KEEL_ADDR` against `http://$KEEL_ADDR:3211/api/auth/…`: `sign-up/email {email:"ci@example.com",password:"correct-horse-battery",name:"CI"}` → `.user.id`; `sign-in/email` → `.token`; a second sign-up (`second@example.com`) must fail (`curl -f` non-zero).
6. setup-go; CLI login: `go build -o $RUNNER_TEMP/keel ./cmd/keel`; `KEEL_CONFIG_DIR=$RUNNER_TEMP/keel-config`; `keel login http://$KEEL_ADDR > pending.json` (no TTY → returns); assert `.ok and .status == "pending" and (.approvalUrl | startswith("http://$KEEL_ADDR/device?user_code="))`; `! keel whoami > early.json` and `.code == "AUTHORIZATION_PENDING"`; sign in, `GET /api/auth/device?user_code=<code>` with the bearer → `.status == "pending"` (binds the code); `POST /api/auth/device/approve {"userCode":code}` → `.success`; `keel whoami` → `.ok and .user.email == "ci@example.com" and .url == "http://$KEEL_ADDR"`; `keel login` → `.ok and .status == "loggedIn"`.
7. CLI as an agent: `keel project list` → `.ok and .projects == []` (no org yet); `keel project create "CI App" --link` → `.project.slug == "ci-app"`, `.project.environments[0].name == "production"`, `.url == "http://$KEEL_ADDR/p/ci-app"`; `! keel project create ci-app` → `.code == "NAME_TAKEN"`; `keel service create api --image nginx:alpine --port 8080 --replicas 2` → `.project.slug == "ci-app" and .service.staged and .service.port == 8080 and .service.replicas == 2`; again → `NAME_TAKEN`; `keel var set api GREETING=hello` → `.ok and .staged`; `keel status` → `.pendingChanges == 1`; `! keel service delete api` → `USAGE`; `keel service delete api --yes` → `.ok and .deleted`; `keel service list` → `.services == []`.
8. Proxy: `docker compose -p keel exec -T backend curl -fsS --unix-socket /run/keel-proxy/admin.sock http://proxy/keel/host-addrs` → non-empty array; `…/config/` → `(.apps // {}) == {}`.
9. Idempotency: sha256 of the `INSTANCE_SECRET|BETTER_AUTH_SECRET|KEEL_WORKER_TOKEN|CONVEX_SELF_HOSTED_ADMIN_KEY` lines before/after a second `sudo -E ./install.sh > second.json` (which must be `.ok == true`) must match.
10. On failure: print both JSONs, `compose ps`, `compose logs --tail 200`, `docker service ps keel-worker --no-trunc`, `docker service logs keel-worker --tail 100`.

### `images.yml` (push to `main`, tags `v*`, manual; `contents: read`, `packages: write`; `REGISTRY=ghcr.io/thallesp`)

- `ci`: `uses: ./.github/workflows/ci.yml` — gates everything.
- `build` (needs ci; matrix image × platform, `fail-fast: false`): images `keel-web` (ctx `.`, `apps/web/Dockerfile`), `keel-functions` (`.`, `deploy/functions.Dockerfile`), `keel-worker` (`apps/worker`), `keel-proxy` (`apps/proxy`); platforms `linux/amd64` on `ubuntu-24.04`, `linux/arm64` on `ubuntu-24.04-arm` (native, no QEMU). Login ghcr with `GITHUB_TOKEN`; `docker/build-push-action@v6` with OCI labels `source`, `revision`, `licenses=MIT`, output `push-by-digest=true,name-canonical=true,push=true`, GHA cache scope `<image>-<arch>`; digest written to `/tmp/digests/<hex>` and uploaded as artifact `digests-<image>-<arch>` (1 day).
- `merge` (needs build; per image): download digests; `docker/metadata-action@v5` tags `latest` (default branch only), `sha-<short>`, semver `{{version}}` on tags; `docker buildx imagetools create -t … <registry>/<image>@sha256:<d>…`; inspect.

`pullfrog.yml`: manual `workflow_dispatch` running the Pullfrog review agent; unrelated to builds.

## A18. Monorepo tooling

- Root `package.json`: workspaces `apps/*`, `packages/*` with a dependency catalog (react 19, convex 1.45, better-auth 1.6.17, @convex-dev/better-auth 0.12.5, zod 4, typescript 6, varlock 1.18.0, tailwind 4…); scripts `dev`, `build`, `check-types` (turbo), `dev:web`, `dev:server`, `dev:setup` (backend), `env:generate` and `postinstall` = `varlock codegen --path ./apps/web/`, `check` = `oxlint && oxfmt --write`; `packageManager: bun@1.4.0`.
- `turbo.json`: `build` (`^build`, outputs `dist/**`, inputs incl. `.env*`), `lint`, `check-types` (`^check-types`), `dev` (no cache, persistent), `dev:setup` (no cache, interactive); `globalEnv` includes `VITE_CONVEX_URL`, `VITE_CONVEX_SITE_URL`, `NODE_ENV`, `CI`, Vercel vars, `_VARLOCK_ENV_KEY`.
- `bunfig.toml` (root and `apps/web`): `env = false` (bun does not auto-load `.env`).
- `apps/web/.env.schema` (varlock): `NODE_ENV` enum; optional `VITE_CONVEX_URL`, `VITE_CONVEX_SITE_URL` (URLs, not `example.convex.*`); generates `src/env.ts`.
- `.dockerignore`: node_modules, .git, build outputs, Dockerfiles, `.env*` (except `.env.example`/`.env.schema`), `**/.convex`, `infra`.

---

# Part B — what each piece becomes with one Go binary

Target: `keel` = one Go binary with three faces.

| Face | Command | Where it runs |
| --- | --- | --- |
| Control plane | `keel serve` | Control-plane server: HTTP JSON API + one WebSocket, embedded web (`//go:embed`), SQLite file, embedded Caddy (public ingress), Swarm reconciler, Axiom/OTLP relay |
| Node agent | `keel agent` | Every Swarm node (global service): Docker events → control plane, container logs → sink. Replaces `apps/worker` |
| CLI | every other command | Anyone's machine. Same command tree, flags, JSON contract and codes as Part A |
| Admin (new) | `keel admin …` | One-shots for install/CI/ops (swarm init, health, migrate, proxy config dump) |

Build: `serve`, `agent` and `admin` are compiled on Linux only (Caddy host-namespace listeners,
Docker socket); macOS/Windows builds are CLI-only so the downloaded CLI stays small. Use a pure-Go
SQLite driver (`modernc.org/sqlite`) so the binary is static and cross-compiles without CGO.

## B1. CLI: what stays, what changes

**Stays exactly:** `internal/output` (envelope, codes, exit codes), `internal/config` file format
(plus one added field), every command, flag, argument check, human text and JSON field in A10,
the polling cadences (1s await, 2s/15s/200 logs follow, device poll interval), `runEnv`,
`lineSet`, `jsonArg`, signal handling, the tests in A11.

**Changes:**

1. **Transport** — `internal/convex` is replaced by a plain HTTP JSON client against `keel serve`.
   `internal/keel/api.go` keeps its method set and CLI-side types; only the request side changes
   (B2). Accept server numbers as ints or floats (`keel.Int` already does) and times as epoch-ms
   numbers **or** RFC 3339 strings (extend `keel.Time.UnmarshalJSON`) so the API spec may choose.
2. **One origin** — the dashboard URL *is* the API URL. The config gains `"apiUrl"` (set to the
   dashboard URL by discovery); `convexUrl`/`convexSiteUrl` are still read so an old file loads,
   but are no longer used. An instance with an empty `apiUrl` re-runs discovery once and saves.
3. **Auth** — no JWT exchange. The session token is the bearer for every API call.
   `Connect` no longer calls `/api/auth/convex/token`; to keep "fail fast" semantics (`keel login`
   detects a dead saved session in step 4), `Connect` calls `GET /api/me` (B2) and maps 401 to
   `NOT_AUTHENTICATED "Session expired or signed out"`. The device flow keeps its paths and wire
   format exactly (A8 server table), now served by `keel serve` at `<dashboard>/api/auth/device/*`.
   `SignOut` stays `POST /api/auth/sign-out`. The CLI keeps sending `Origin: <dashboard>`.
4. **Flags/env** — add `--api-url` (login) and `KEEL_API_URL` (with `KEEL_URL`) to skip discovery.
   Keep `--convex-url`, `--convex-site-url`, `KEEL_CONVEX_URL`, `KEEL_CONVEX_SITE_URL` accepted
   but hidden; when given, `--convex-site-url`/`KEEL_CONVEX_SITE_URL` is used as the API URL (it is
   where auth lived). Help text is not contract; flags and env vars are only added or hidden,
   never made to fail.
5. **`translate`** — match server **error codes** instead of message text, but produce the exact
   CLI code/message/fix of A9. The server error body (B2) carries `code` and the unchanged English
   `message`. Mapping:

   | Server `code` (proposed) | Server message (unchanged) | CLI result |
   | --- | --- | --- |
   | `unauthenticated` (HTTP 401) | `Not authenticated` | A9 row 2 |
   | `no_organization` (403) | `You're not in an organization yet. Ask a member for an invite link.` | row 3 |
   | `deployment_running` (409) | `A deployment is already running` | row 4 |
   | `nothing_to_ship` (409) | `Nothing to ship` | row 5 |
   | `node_not_found` (404) | `Node not found` | row 6 |
   | `traces_off` (409) | `Connect Axiom to see traces` / `Sign in with Axiom again to turn on traces` | row 7 (message verbatim) |
   | `environment_not_found` (404) | `Environment not found` | row 8 |
   | `project_exists` (409) | `Project "<slug>" already exists` (+ `slug` field) | row 9, fix uses `slug` |
   | `name_taken` (409) | `"<name>" is already taken` | row 10 |
   | `invalid_input` (400) | any validation message | row 11 |
   | `internal` (500) / unknown | — | row 12 (`SERVER_ERROR`, message) |

   Keep the message-text fallback for rows 2–10 when `code` is missing, so a server that forgot a
   code still maps correctly. Transport rows 13–17 are unchanged; HTTP 401 without a JSON body is
   still `NOT_AUTHENTICATED`.
6. **`keel run`** endpoint = `<apiUrl>/otlp` (server route `POST /otlp/v1/traces`). The overlay
   regex `\bsvc-[0-9a-z]{32}\b` depends on the id format: **keep ids 32 chars of `[0-9a-z]`**
   (B6), or change the regex in the same release as the id format.
7. **Distribution** — new: release assets `keel_<version>_<os>_<arch>.tar.gz` (+ `checksums.txt`)
   for linux/darwin/windows × amd64/arm64 on `v*` tags; the server image also carries the binary,
   and `install.sh` copies it to `/usr/local/bin/keel` on the control plane.

> **Go now:** an instance in `config.json` is `{url, email, token, pending}` (no `apiUrl`, no Convex URLs; a Convex-era file is not read specially); there is no `--api-url`, no `--convex-*` flag and no `KEEL_CONVEX_*`; the CLI uses the server's problem `code` as is, with no message fallback; ids are `domain.NewID()` (20 base32 chars), so `keel run`'s overlay filter is `\bsvc-[0-9a-z]{20}\b`, and its endpoint is `<install URL>/otlp`.

## B2. API calls the CLI needs (proposal; 1:1 with A11)

All under the dashboard origin, `Authorization: Bearer <session token>`, JSON in/out. Error body:
`{"code": "...", "message": "...", ...extra}` with the status in the table above. Ids are opaque
strings. "null" results of A11 become 404 with the codes listed (the CLI maps each 404 back to
the null behaviour it had).

| Convex today | Method + path | Request | Response | Not found / null case |
| --- | --- | --- | --- | --- |
| `auth:getCurrentUser` | `GET /api/me` | – | `{id,email,name}` | 401 `unauthenticated` → CLI `NOT_AUTHENTICATED "Session expired or signed out"` |
| `organizations:current` | `GET /api/organization` | – | `{id,name,slug,role}` or `null` (200) | – |
| `projects:list` | `GET /api/projects` | – | `[Project]` | errors as A11 |
| `projects:create` | `POST /api/projects` | `{name}` | `Project` (201) | – |
| `environments:summary` | `GET /api/environments/{id}/summary` | – | `{pendingChanges,counts,servers}` | 404 → CLI `PROJECT_NOT_FOUND "Environment not found"` |
| `nodes:list` | `GET /api/environments/{id}/nodes` | – | `[NodeView]` (fields of A11, `dirty` included) | not member → `[]` (keep) |
| `nodes:create` | `POST /api/environments/{id}/nodes` | `{type,name?,image?,port?,replicas?,engine?,position?,deploy?}` | `{id,deploymentId?}` (may return the view too; the CLI can then skip the re-list) | – |
| `nodes:remove` | `DELETE /api/nodes/{id}` | – | 204, also when already gone | – |
| `variables:list` | `GET /api/nodes/{id}/variables` | – | `[{key,value,resolved,secret,resolvedSecret,parts}]` | not member → `[]` |
| `variables:set` | `POST /api/nodes/{id}/variables` | `{key,value,secret,previousKey?}` (key in the body: user input may contain `/`) | 204 | – |
| `variables:remove` | `POST /api/nodes/{id}/variables/delete` (or `DELETE …?key=`) | `{key}` | 204 (missing key: no-op) | – |
| `logs:tail` | `GET /api/nodes/{id}/logs?tail=N` | – | `{source,lines,replicas}` | – |
| `traces:overview` | `GET /api/environments/{id}/traces?range=&search=&nodeId=` | – | as A11 | – |
| `tracing:forNode` | `GET /api/nodes/{id}/tracing` | – | `{enabled,traces,env}` | 404 → CLI INVALID_INPUT "…only services can be traced" |
| `tracing:enable` | `PUT /api/nodes/{id}/tracing` | `{on}` | 204 | – |
| `tracing:localEnv` | `POST /api/nodes/{id}/tracing/local-env` (POST: may mint the key) | – | `{env,reason}` | – |
| `tracing:prompt` | `GET /api/tracing/prompt?nodeId=&environmentId=` | – | `{prompt}` | – |
| `deployments:start` | `POST /api/environments/{id}/deployments` | `{only?,refresh?}` | `{id}` (201) | – |
| `deployments:get` | `GET /api/deployments/{id}` | – | Deployment doc (`id` instead of `_id`; steps `nodeId`; log `nodeId`) | malformed/missing/foreign → 404 → `DEPLOYMENT_NOT_FOUND` |
| `deployments:latest` | `GET /api/environments/{id}/deployments/latest` | – | doc or `null` | – |
| `deployments:listForNode` | `GET /api/nodes/{id}/deployments` | – | `[doc]` (≤ 20) | – |

All server semantics in A11 (validation order, exact messages, side effects, scheduling) carry
over; Convex `scheduler.runAfter(0, …)` becomes an in-process job queue in `keel serve`.

### Realtime: CLI-triggered writes and the invalidation keys they must publish

The CLI does not subscribe (it polls). The dashboard does; every write above must push the coarse
keys the web's subscribed queries use. Proposed keys and what they cover:

| Key | Queries it refreshes (web) | Published by these CLI-reachable writes |
| --- | --- | --- |
| `org:<orgId>` | `projects:list`, `organizations:current` | `projects:create` (incl. founding the org), membership changes |
| `env:<envId>` | `nodes:list`, `environments:summary`, `variables:list` (resolved values cross nodes), `deployments:latest`, `deployments:listForNode`, `tracing:forNode` | `nodes:create`, `nodes:remove`, `variables:set`, `variables:remove`, `tracing:enable`, `deployments:start`, and every async writer they schedule (apply, observe, reconcile, timeout, proxy sync status) |
| `deployment:<id>` | `deployments:get` | `deployments:start` and every step/log write |
| `user:<userId>` | `auth:getCurrentUser` | sign-in/out, device approval (the session list) |

`nodes:remove` must publish `env:<envId>` for the node's environment **before** the row is gone
(capture the id first). Variable writes mark referrers dirty in the same environment, so one
`env:` key covers them.

## B3. `/config.js` in the Go world

> **Go now:** neither `/config.js` nor `/version` exists. The dashboard shares the API's origin and reads no runtime config, the CLI discovers an install through `GET /api/meta` (`{name, version, siteUrl}`), and `install.sh` checks `/api/meta`.

`keel serve` serves `GET /config.js` itself (`Content-Type: application/javascript`,
`Cache-Control: no-store`) with the **same shape** — one JSON object between the first `{` and
the last `}`:

```
window.__KEEL__ = {"apiUrl":"http://100.64.0.1","version":"1.2.3"};
```

- `apiUrl` = `SITE_URL` (the dashboard origin). The web uses it (defaulting to `location.origin`);
  the CLI's discovery reads `apiUrl`.
- Do **not** emit `convexUrl`/`convexSiteUrl`: an old (Convex-protocol) CLI then fails with
  `DISCOVERY_FAILED "…has no Convex URLs (a dev server?)"` instead of half-working. The new CLI,
  seeing `convexUrl` but no `apiUrl`, reports `DISCOVERY_FAILED "<url> runs an older Keel; re-run
  install.sh on it to upgrade"` with fix `curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash`.
- Dev: vite serves `public/config.js` (`window.__KEEL__ = {};`) only if not proxied. Proxy
  `/api`, `/ws`, `/otlp`, `/config.js`, `/version` from vite (:3001) to `keel serve`, so a dev
  dashboard URL discovers like an installed one and `--convex-*` flags are no longer needed.
- Also serve `GET /version` (plain text version) so the README's `curl "$convexUrl/version"`
  verification keeps working when `convexUrl` is kept in install output (B5).

Embedded web serving must reproduce nginx: SPA fallback to `index.html` for unknown non-API
paths, `/assets/*` with `Cache-Control: public, max-age=31536000, immutable`, gzip for text types,
`/config.js` uncached.

## B4. `deploy/compose.yml` → one service

Recommendation: keep Compose (project `keel`) with a single service, so `compose up --wait`,
healthchecks, `-p keel` and the README's uninstall/logs commands survive:

> **Go now:** two services from one image (`deploy/compose.yml`): `keel` (`keel serve`, SQLite in volume `keel-data` at `/data`, the Docker socket, `KEEL_SITE_URL`) and `proxy` (`keel proxy`, embedded Caddy with the admin socket in the shared `proxy-admin` volume, never the Docker socket). Start-up runs schema migrations only; there are no data migrations and no `BETTER_AUTH_SECRET`.

```yaml
name: keel
services:
  keel:
    image: ${KEEL_IMAGE_PREFIX:-ghcr.io/thallesp}/keel:${KEEL_VERSION:-latest}
    command: ["serve"]
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "${KEEL_ADDR:?}:${KEEL_WEB_PORT:-80}:8080"     # dashboard + API + /otlp + agent routes
    environment:
      KEEL_ADDR: ${KEEL_ADDR}
      SITE_URL: ${SITE_URL:?}
      KEEL_SECRET: ${BETTER_AUTH_SECRET:?}             # same value, renamed inside
      KEEL_WORKER_TOKEN: ${KEEL_WORKER_TOKEN:?}
      KEEL_PUBLIC_IP: ${KEEL_PUBLIC_IP:-}
      KEEL_ACME_EMAIL: ${KEEL_ACME_EMAIL:-}
      KEEL_IMAGE: ${KEEL_IMAGE_PREFIX:-ghcr.io/thallesp}/keel:${KEEL_VERSION:-latest}
    volumes:
      - ${KEEL_DIR:-/opt/keel}/data:/var/lib/keel      # keel.db (+ -wal/-shm), caddy/ (certs, ACME), caddy-autosave.json
      - /var/run/docker.sock:/var/run/docker.sock
      - /proc/1/ns/net:/run/hostns/net:ro              # public listeners in the host netns (as keel-proxy today)
    cap_drop: [ALL]
    cap_add: [SYS_ADMIN, NET_BIND_SERVICE]
    security_opt: ["no-new-privileges:true"]
    networks: [keel]                                   # dial svc-<id>:<port> over the overlay
    healthcheck:
      test: ["CMD", "/usr/local/bin/keel", "admin", "health"]
      interval: 5s
      start_period: 10s
networks:
  keel:
    external: true
```

What each old piece becomes:

| Today | Go world |
| --- | --- |
| `backend` (Convex, :3210/:3211, `convex-data`, docker.sock) | `keel serve` API + SQLite at `/var/lib/keel/keel.db` (WAL, single writer — same single-writer assumption as Convex). Ports 3210/3211 disappear; everything is on `KEEL_WEB_PORT` |
| `web` (nginx, `/config.js` from env) | Embedded in `keel serve`; `/config.js` generated from `SITE_URL` |
| `proxy` (Caddy + caddy-l4 + plugin, admin unix socket, `proxy-data`, `proxy-config`) | Caddy embedded as a library in `keel serve`; config loaded in-process (`caddy.Load`), no admin socket; the `host-tcp`/`host-udp` networks and host-address listing move in as-is; cert events become in-process callbacks instead of `POST /proxy/events`; storage under `/var/lib/keel/caddy` |
| `keel-functions` one-shot (env set, deploy, `migrations:run`) | Gone. Env comes from compose; schema migrations (embedded SQL) and data migrations run at `keel serve` start, then a proxy sync |
| `INSTANCE_SECRET`, `CONVEX_SELF_HOSTED_ADMIN_KEY`, `generate_admin_key.sh`, `functions check` | Gone after the Convex → SQLite migration (B6); kept in `.env` until it has run |

Decision to record: merging the proxy into the control plane puts internet-facing parsing in the
same process as the Docker socket (today the proxy has 2 capabilities and no socket). The
container model above keeps the proven overlay + host-netns trick from `docs/networking.md`.
The alternative — `keel serve` as a host systemd service — needs an overlay **dialer** (setns into
the netns of an overlay-attached anchor container for both DNS `127.0.0.11` and the connection)
and gives up nothing else; pick one before writing the ingress code.

## B5. `install.sh` in the Go world

Contract kept: same one-liner, idempotent, re-run = upgrade, `KEEL_JSON=1`, `error:`/`fix:`
lines, same option names (A13). Changes per step:

| Step | Today | Go world |
| --- | --- | --- |
| preflight, docker, tailscale, public IP | A13 1–4 | Unchanged (same messages) |
| write_state | fetch compose + 2 scripts; 4 secrets | Fetch `compose.yml` only. Secrets: keep `BETTER_AUTH_SECRET` (passed as `KEEL_SECRET`) and `KEEL_WORKER_TOKEN` forever; keep `INSTANCE_SECRET` and `CONVEX_SELF_HOSTED_ADMIN_KEY` only while a Convex install awaits migration, then drop them from `.env`. Save the same non-secret keys. Write values JSON-safe and quote-safe |
| start_swarm | `bootstrap-swarm.sh --swarm-only` | `docker run --rm -v /var/run/docker.sock:/var/run/docker.sock $IMAGE admin swarm-init --addr $KEEL_ADDR` (same init flags, same `keel` overlay, MTU 1200, pool 10.200.0.0/16 /24). `keel serve` re-checks at start (heals a reboot) |
| pull | compose images + functions + worker | `compose pull --quiet` (one image) unless `KEEL_PULL=0` |
| migrate (new) | – | If a Convex control plane is present (compose has `backend`, or volume `keel_convex-data` exists and `data/keel.db` does not): run B6 before switching |
| start_control_plane | `compose up -d --wait --wait-timeout 180 --remove-orphans` + admin key + functions | `compose up -d --wait --wait-timeout 180 --remove-orphans` (removes the old `backend`/`web`/`proxy` containers; their volumes stay for rollback). Failure: `the control plane did not become healthy` / `docker compose -p keel logs keel` |
| install CLI (new) | – | `docker create` + `docker cp <ctr>:/usr/local/bin/keel /usr/local/bin/keel` (works with `KEEL_PULL=0` local images, as CI needs) |
| start_workers | `bootstrap-swarm.sh` → `deploy-worker.sh` | Gone from the installer: `keel serve` creates/updates the `keel-agent` global service at start (B7), pinned to its own image digest; the installer only waits for it |
| check_health | `:3210/version`, `/config.js` contains addr, `:3211/worker/config` bearer, `keel-worker` n/n | `curl -fsS $SITE_URL/version`; `curl -fsS $SITE_URL/config.js \| grep -q $KEEL_ADDR`; `curl -fsS -H @- $SITE_URL/agent/config` (bearer `KEEL_WORKER_TOKEN`; keep `/worker/config` as an alias); `docker service ls --filter name=keel-agent` n/n (60×2s). Messages: `the control plane is not answering on <SITE_URL>` / `docker compose -p keel logs keel`; config and token messages as today with `keel` in the fix; `keel-agent is not running (replicas: …)` / `docker service ps keel-agent --no-trunc` |
| output | A13 10 | stderr block: Dashboard `SITE_URL`, State, same hints (no Convex line). JSON: `{"ok":true,"url":SITE_URL,"apiUrl":SITE_URL,"convexUrl":SITE_URL,"convexSiteUrl":SITE_URL,"version","stateDir","publicIp"}` — `convexUrl`/`convexSiteUrl` kept (README contract; fields are only added) and equal to `url` |

> **Go now:** no migrate step and no Convex secrets (`.env` keeps `KEEL_WORKER_TOKEN` among the settings); `check_health` reads `$SITE_URL/api/meta` (it must name `SITE_URL`), `$SITE_URL/`, `GET $SITE_URL/worker/config` with the bearer (there are no `/agent/*` routes) and waits for `keel-agent` n/n; the success JSON is `{ok, url, apiUrl, version, stateDir, publicIp, warnings}` with `apiUrl` equal to `url` and no `convexUrl` / `convexSiteUrl`.

Ports after the switch (README table): `KEEL_WEB_PORT` (80) on the tailnet IP serves dashboard,
API, WebSocket, `/otlp`, `/agent/*`; Swarm ports unchanged; public 80/443 + exposed ports from
the embedded Caddy on non-tailnet host addresses. Deployed services' OTLP endpoint becomes
`<SITE_URL>/otlp`; the agent's `KEEL_URL` becomes `SITE_URL`.

Uninstall becomes: `docker compose -p keel -f /opt/keel/compose.yml down`; `docker service rm
keel-agent`; user services by label `keel.service` (unchanged); data in `/opt/keel/data`.

Verification (README "Install with an agent") becomes: `curl -fsS "$url/version"`,
`curl -fsS "$url/config.js"`, `docker compose -p keel ps` (keel: healthy),
`docker service ls --filter name=keel-agent` (n/n).

## B6. Upgrading an existing Convex install

Must be automatic in `install.sh` and safe to re-run:

1. While the old `backend` still runs: export a snapshot with the stored admin key
   (`npx convex export --path /opt/keel/convex-export-<ts>.zip`, through the previous
   `keel-functions` image, which has the convex CLI) — or have `keel admin import-convex` read
   `convex-data` directly. The snapshot includes the Better Auth component tables.
2. `keel admin import-convex <zip> --db /opt/keel/data/keel.db`: import every table, **keeping
   document ids verbatim** (Swarm service names `svc-<id>`, the `keel.service_id` OTLP resource
   attribute, default-domain hashes `FNV(node id)`, CLI links and agents' stored ids all depend on
   them). New ids in Go: 32 chars from Convex's alphabet (`[0-9a-z]` subset) so `svc-<id>` and the
   CLI regex keep matching. Import Better Auth `session` rows (tokens) so saved `keel login`
   sessions and `KEEL_TOKEN`s keep working; import `user`, `account` (password hashes: Better
   Auth's scrypt format must be verified by Go), `member`, `organization`, `invitation`;
   `deviceCode` rows may be dropped.
3. Copy `keel_proxy-data` (`/data/caddy/certificates`, ACME accounts) into
   `/opt/keel/data/caddy` so certificates are not re-issued (sslip.io shares one Let's Encrypt
   quota). Copy `keel_proxy-config` autosave only if the Go config is format-compatible; else let
   the first sync rebuild it.
4. Replace `keel-worker` with `keel-agent`: `keel serve` removes the `keel-worker` service (and
   its `keel-worker-token-*` secret) after `keel-agent` is running; the agent's state volume can
   reuse `keel-worker-state` to keep log resume points.
5. Keep `keel_convex-data` and the export zip for rollback; print where they are.
6. Idempotent: a present `keel.db` with a `migrations` row for the import skips steps 1–3.

> **Go now:** dropped (docs/go/spec/INDEX.md): no export, no `import-convex`, no certificate copy, and `keel serve` does not remove `keel-worker` or reuse `keel-worker-state`; `keel-agent` keeps its state on its own `keel-agent-state` volume.

## B7. `keel agent` (replaces `apps/worker` and `deploy-worker.sh`)

`keel serve` reconciles its own agent service at start (and after upgrades), with the same
create-or-update-if-changed logic as `deploy-worker.sh`:

- Name `keel-agent`, `--mode global`, `--network host`, image `$KEEL_IMAGE` pinned to the digest
  `keel serve` runs from (inspect its own container), command `agent`.
- Mounts: `/var/run/docker.sock` (read-only), volume `keel-agent-state` → `/var/lib/keel-agent`.
- Secret `keel-agent-token-<sha256(token)[:12]>` → target `keel_worker_token` (old secret removed
  after update); env `KEEL_URL=<SITE_URL>`.
- Restart any, delay 2s, stop grace 10s. Compare image + secret + `KEEL_URL`; update only on a
  difference (an unchanged agent is a no-op).
- Agent env stays: `KEEL_URL`, token from `KEEL_WORKER_TOKEN` or `/run/secrets/keel_worker_token`,
  `DOCKER_SOCKET`, `KEEL_STATE` (default `/var/lib/keel-agent/state.json`), `KEEL_CONFIG_POLL_MS`.
  Routes `POST /agent/events` and `GET /agent/config` (aliases `/worker/*` during the transition).

> **Go now:** `EnsureAgent` runs when `KEEL_AGENT_IMAGE` is set; it labels the spec with a hash and updates only when that changes; `KEEL_URL` is `KEEL_AGENT_CONTROL_URL`, else `KEEL_SITE_URL`; command `keel agent`; every capability dropped. There is no `DOCKER_SOCKET` (`DOCKER_HOST`) and no `/agent/*` route: the agent calls `POST /worker/events` and `GET /worker/config`.

## B8. CI and release in the Go world

`ci.yml`:

| Job | Steps |
| --- | --- |
| `web` | bun setup, `bun install --frozen-lockfile`, `bun run check-types` (web + ui only; backend and worker packages are gone) |
| `go` | setup-go from the root `go.mod`; `test -z "$(gofmt -l .)"`; `go vet ./...`; `go test ./...` (and `-race`). The embedded web needs a placeholder `dist/index.html` committed (or a `noweb` build tag) so Go builds without bun. Folds in today's `cli` and `proxy` jobs |
| `install` | Build one image: `docker buildx build --load -t local/keel:ci .` (root `Dockerfile`: bun stage builds `apps/web/dist` → Go stage embeds it, `-ldflags "-X main.version=ci"` → minimal runtime image with `/usr/local/bin/keel` and CA certs). Same `$GITHUB_ENV` (`KEEL_IMAGE_PREFIX=local`, `KEEL_VERSION=ci`, `KEEL_PULL=0`, …). Same assertions as A17 steps 4–7 with auth at `http://$KEEL_ADDR/api/auth/…` (no `:3211`) and the CLI taken from the image (`/usr/local/bin/keel` the installer copied) instead of `go build`. Step 8 becomes `docker compose -p keel exec -T keel keel admin proxy-config` → no apps, plus `keel admin host-addrs` non-empty. Step 9 hashes `BETTER_AUTH_SECRET` and `KEEL_WORKER_TOKEN`. Diagnostics: `compose logs keel`, `docker service ps/logs keel-agent` |
| `upgrade` (new) | Install the last released Convex-era images, create a project + service + variable + session via the CLI, re-run the new `install.sh`, assert the same ids, the same `keel whoami` without re-login, and the service still running |

`images.yml`: same gate (`uses: ci.yml`), same per-arch native build + digest merge + tags
(`latest` on main, `sha-<short>`, semver), matrix reduced to one image `keel` (context `.`, file
`Dockerfile`). New job on `v*` tags (`contents: write`): cross-compile the CLI for
linux/darwin/windows × amd64/arm64 with `-X main.version=<tag>`, tar/zip, `checksums.txt`, attach
to the GitHub release. `pullfrog.yml` unchanged.

Monorepo after the move: `packages/backend`, `apps/worker`, `apps/proxy` and `deploy/functions*`
are deleted; `apps/cli` and `apps/proxy` Go code merge into one module (root or `apps/keel`, with
`cmd/keel`); root scripts `dev:server`/`dev:setup` become `go run ./cmd/keel serve --dev`;
`turbo.json`/`.env.schema` drop `VITE_CONVEX_URL`/`VITE_CONVEX_SITE_URL`; `scripts/dev-https.sh`
needs only `tailscale serve --https=443 → vite` and sets `SITE_URL` for `keel serve`;
`.dockerignore` keeps excluding `.env*` and adds `**/data/keel.db*`.

## B9. Things to update together (checklist)

- `README.md` Install (ports table, what the installer does, options: drop the Convex mentions,
  uninstall, troubleshooting: `docker compose -p keel logs keel`, no npm requirement), "Install
  with an agent" contract (add `apiUrl`), Development, Layout.
- `apps/cli/README.md`: Auth section (no JWT exchange, no `/api/auth/convex/token`), `--api-url`,
  dev-server note, Layout (`internal/convex` gone), Next.
- `CLAUDE.md` Install/CLI sections (images, `/config.js` shape note, `translate` now keys on codes).
- `docs/workers.md` (agent replaces worker; Convex reaches Docker → `keel serve` reaches Docker),
  `docs/networking.md` (embedded Caddy; admin socket gone).
- CI `install` job and the new `upgrade` job.

## B10. Open decisions

1. `keel serve` as a container (recommended, B4) vs host systemd service + overlay dialer.
2. Route naming in B2 vs whatever the API spec settles on (CLI only needs the semantics and the
   error codes in B1.5).
3. Whether `/config.js` keeps `convexUrl` (B3 says no; install output keeps it equal to `url`).
4. Id format: keep Convex-shaped ids (recommended) or migrate `svc-<id>` names (would recreate
   every user service and change default domains).
5. Session import vs forcing every CLI to `keel login` again after the upgrade.

---

## Addendum (critic)

### C1. Server error codes: B1.5/B2 disagree with `docs/go/ARCHITECTURE.md` "Errors"

B1.5 and B2 propose lowercase server codes (`unauthenticated`, `node_not_found`,
`invalid_input` → **400**, `project_exists` + `slug`, `internal`, …) in a `{"code","message"}`
body. ARCHITECTURE (and projects.md, auth-orgs.md, proxy-ingress.md, which cite `api.Code*`)
prescribes that the server's `code` **is the CLI vocabulary** of A2, in RFC 9457
`application/problem+json` with an extra `code` member, and `INVALID_INPUT` is **422**. Resolve to
ARCHITECTURE; then `translate` becomes "use the server's `code` as is" and only the `fix` text is
computed client-side. Row-by-row equivalence:

| B1.5 proposal | ARCHITECTURE `code` (HTTP) | Server message (unchanged) | CLI `fix` source |
| --- | --- | --- | --- |
| `unauthenticated` (401) | `NOT_AUTHENTICATED` (401) | `Not authenticated` | A9 row 2 |
| `no_organization` (403) | `NO_ORGANIZATION` (403) | `You're not in an organization yet. Ask a member for an invite link.` | row 3 |
| `deployment_running` (409) | `DEPLOYMENT_RUNNING` (409) | `A deployment is already running` | row 4 |
| `nothing_to_ship` (409) | `NOTHING_TO_SHIP` (409) | `Nothing to ship` | row 5 |
| `node_not_found` (404) | `SERVICE_NOT_FOUND` (404) | `Node not found` | row 6 |
| `traces_off` (409) | `TRACES_OFF` (409) | `Connect Axiom to see traces` / `Sign in with Axiom again to turn on traces` | row 7 |
| `environment_not_found` (404) | `PROJECT_NOT_FOUND` (404) | `Environment not found` | row 8 |
| `project_exists` (409, `slug`) | `NAME_TAKEN` (409) + problem extension member `slug` | `Project "<slug>" already exists` | row 9 (`slug` member, else parse the message as today) |
| `name_taken` (409) | `NAME_TAKEN` (409) | `"<name>" is already taken` | row 10 |
| `invalid_input` (400) | `INVALID_INPUT` (**422**) | the validation message verbatim | row 11 |
| `internal` (500) | `SERVER_ERROR` (500; generic message, cause logged) | — | row 12 |
| — | `CONFLICT` (409) | endpoint collisions (`<domain> is already used by <owner>`, `Port <p>/<proto> is already used by <owner>`) — proxy-ingress.md §4.5 | CLI has no expose command today; maps to `INVALID_INPUT` if it ever gets one, unless a `CONFLICT` code is added to A2 (codes are only ever added) |
| — | `UNAVAILABLE` (503) | `Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP` (proxy-ingress.md suggests it) | same remark |
| — | `DEPLOYMENT_NOT_FOUND` (404) | (B2 only: `deployments:get` miss) | conflicts with web-data.md, which keeps `200 {deployment: null}`; see web-data.md Addendum W1 |

Keep the message-text fallback of B1.5 (rows 2–10) for one release.

### C2. Control-plane env var names differ between specs

Four documents name the same settings differently. One table to settle before writing
`internal/config`:

| Today (Convex env / compose) | ARCHITECTURE "Env (serve)" | This spec B4 compose | Other specs | Recommendation |
| --- | --- | --- | --- | --- |
| `SITE_URL` | `KEEL_SITE_URL` | `SITE_URL` | auth-orgs.md §2: `KEEL_SITE_URL` replaces it | `KEEL_SITE_URL`; accept `SITE_URL` as a fallback so an old `.env` keeps working |
| `BETTER_AUTH_SECRET` | — (not listed) | `KEEL_SECRET: ${BETTER_AUTH_SECRET}` | auth-orgs.md §2/§13: not needed if cookies are not signed | only if signed cookies are kept; otherwise drop (keep it in `.env` until the import, B6) |
| `CONVEX_SITE_URL` (base for `/otlp`, `/proxy/events`, auth `baseURL`) | — (same origin) | — | observability.md §15: "the control plane's public base URL" | derive from `KEEL_SITE_URL` |
| — (port 3210/3211) | `KEEL_LISTEN` (`:8080`) | container port `8080` | — | `KEEL_LISTEN`, default `:8080` |
| `convex-data` volume | `KEEL_DATA_DIR` (`/data`) | volume at `/var/lib/keel` | proxy-ingress.md §12: Caddy storage under the data dir | pick one default; B4's `/var/lib/keel` needs `KEEL_DATA_DIR=/var/lib/keel` in compose, or change ARCHITECTURE's default |
| `KEEL_PROXY_SOCKET` (`/run/keel-proxy/admin.sock`) | `KEEL_PROXY_ADMIN` (same default) | — (Caddy embedded, no socket) | proxy-ingress.md §9, §12.1: socket disappears when embedded | drop if Caddy is embedded (B4, proxy-ingress §12); otherwise rename consistently |
| `KEEL_PROXY_REPORT_URL` | — | — | proxy-ingress.md §9 | drop when embedded (cert events become in-process callbacks) |
| `/var/run/docker.sock` (hard-coded) | `DOCKER_HOST` | bind mount | projects.md §13 | `DOCKER_HOST`, default `unix:///var/run/docker.sock` |
| `KEEL_WORKER_TOKEN`, `KEEL_PUBLIC_IP`, `KEEL_ACME_CA`, `KEEL_ACME_EMAIL`, `KEEL_OTLP_URL`, `KEEL_ALLOW_LOCAL_SINKS`, `KEEL_AXIOM_AUTH_URL`, `KEEL_AXIOM_API_URL` | same | same | same | unchanged |

Agent env (`KEEL_URL`, `KEEL_WORKER_TOKEN` / `/run/secrets/keel_worker_token`, `KEEL_STATE`,
`KEEL_CONFIG_POLL_MS`, `DOCKER_SOCKET`) is unchanged in every spec (swarm-worker.md §13.1).

> **Go now:** `KEEL_SITE_URL` with no `SITE_URL` fallback, `KEEL_PROXY_SOCKET` and `KEEL_PROXY_REPORT_URL` kept (the edge stayed its own container), `KEEL_DATA_DIR` `/data`. The agent has no `DOCKER_SOCKET` (`DOCKER_HOST`), its `KEEL_STATE` defaults to `/var/lib/keel-agent/state.json`, and `KEEL_TS_AUTHKEY` is new.
