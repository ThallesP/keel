# keel CLI

Keel from the terminal, for agents first and people too. A thin client over the same HTTP API the dashboard calls (`/api`, described by `openapi.json`); no API of its own.

## Build

```bash
go build -o bin/keel ./cmd/keel                        # the whole binary: CLI, serve, proxy (Linux only), agent
go build -tags keel_noproxy -o bin/keel ./cmd/keel     # leaves the embedded Caddy edge out (laptops, agents)
```

Go 1.27. The CLI verbs are the `keel` binary that also runs the control plane (`keel serve`), its edge (`keel proxy`) and the node agent (`keel agent`); one module at the repo root. CI runs `gofmt`, `go vet` and `go test` (`ci.yml`, job `go`), and the install job logs in with it against a fresh install, approving the link with curl.

## Use

```bash
keel login https://keel.example.ts.net          # prints a link; approve it in the dashboard
keel link acme-support                          # this directory → that project
keel status
keel service create api --image ghcr.io/acme/api:1.4 --port 3000   # staged, like a canvas drop
keel logs api -n 200
keel var set api LOG_LEVEL=debug                # staged, like the dashboard
keel ship                                       # deploy staged changes, wait for the result
keel redeploy api                               # pull the image again and roll out
keel tracing prompt api | pbcopy                # instructions for a coding agent: add OpenTelemetry
keel run api -- bun dev                         # local run with api's variables + tracing to Keel
keel traces api --since 15m                     # the requests that arrived, local ones marked
keel tracing enable api && keel redeploy api    # deployed api gets the OTEL_* variables
```

| Command                                                         | What it does                                                                                                                                                                                                                                                                                                                                                                                                  |
| --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `login [dashboard-url] [--wait\|--no-wait]`                     | Sign in by approving a link in the dashboard; checks the URL is a Keel install at `/api/meta`                                                                                                                                                                                                                                                                                                                 |
| `logout`                                                        | Revoke the session and forget the install                                                                                                                                                                                                                                                                                                                                                                     |
| `whoami`                                                        | Account, organization, install                                                                                                                                                                                                                                                                                                                                                                                |
| `token`                                                         | Print the session token, for `KEEL_TOKEN`                                                                                                                                                                                                                                                                                                                                                                     |
| `project list`                                                  | Projects of the organization                                                                                                                                                                                                                                                                                                                                                                                  |
| `project create <name> [--link]`                                | New project with a production environment; the slug comes from the name (`Acme API` → `acme-api`). `--link` links this directory to it. Prints its canvas URL (`url`): the dashboard has no project switcher yet                                                                                                                                                                                              |
| `link [project]` / `unlink`                                     | Pin a directory (and its subdirectories) to a project                                                                                                                                                                                                                                                                                                                                                         |
| `status`                                                        | Services, staged changes, last deployment                                                                                                                                                                                                                                                                                                                                                                     |
| `service list`                                                  | Services, databases, caches, volumes                                                                                                                                                                                                                                                                                                                                                                          |
| `service create <name> --image <ref> [--port N] [--replicas N]` | Stage a service, as dropping one on the canvas does; `keel ship <name>` deploys it. Port defaults to 80, replicas to 1                                                                                                                                                                                                                                                                                        |
| `service delete <service> [-y]`                                 | Delete now, **not staged**: containers and variables go, services referencing it get staged changes. Asks with a terminal, needs `--yes` without one                                                                                                                                                                                                                                                          |
| `logs <service> [-n N] [-f]`                                    | Last N lines (default 100); `-f` polls until interrupted                                                                                                                                                                                                                                                                                                                                                      |
| `traces [service] [--since 15m\|1h\|24h\|7d] [--search s]`      | Latest requests (root spans), newest first, with counts and latency; `local: true` on those from `keel run`. `TRACES_OFF` when the organization has no Axiom traces dataset                                                                                                                                                                                                                                   |
| `run <service> -- <command...>`                                 | Run a command here with the service's variables and tracing variables (`deployment.environment.name=local`, spans to Keel). The shell's own variables win; variables pointing at `svc-…` cluster hosts are left out with a warning. stdout is the command's; keel exits with its code. Signals keel gets reach the command; without a terminal, its whole process group (`npm run dev` and the node under it) |
| `tracing prompt [service]`                                      | Print the prompt that has a coding agent add OpenTelemetry to a repo and check it with `run` and `traces` (the dashboard's Copy agent prompt)                                                                                                                                                                                                                                                                 |
| `tracing enable\|disable <service>`                             | Stage the service's tracing switch: on, it gets the `OTEL_*` variables on the next ship                                                                                                                                                                                                                                                                                                                       |
| `tracing status <service>`                                      | The switch and the variables it sets                                                                                                                                                                                                                                                                                                                                                                          |
| `var list <service> [--show-secrets]`                           | Variables; secret values are `null` unless asked                                                                                                                                                                                                                                                                                                                                                              |
| `var set <service> KEY=VALUE... [--secret]`                     | Stage variables                                                                                                                                                                                                                                                                                                                                                                                               |
| `var delete <service> KEY...`                                   | Stage deletions; fails without deleting anything if a key is missing                                                                                                                                                                                                                                                                                                                                          |
| `ship [service...] [-d]`                                        | Deploy staged changes (all, or only these)                                                                                                                                                                                                                                                                                                                                                                    |
| `redeploy <service...> [-d]`                                    | Roll out again with a fresh image pull                                                                                                                                                                                                                                                                                                                                                                        |
| `deployment list <service>`                                     | Last 20 deployments that touched a service                                                                                                                                                                                                                                                                                                                                                                    |
| `deployment get [id] [--wait]`                                  | One deployment with steps and log; the latest without an id                                                                                                                                                                                                                                                                                                                                                   |

`ship`, `redeploy` and `deployment get --wait` wait until the deployment settles, stream its log to stderr, and exit non-zero if it fails. `-d/--detach` returns once it starts.

## Contract

What agents rely on. Fields and codes are only ever added.

- **stdout is results only.** Text or a table, or with `--json` / `KEEL_JSON=1` exactly one JSON object: `{"ok":true,...}`. `logs --follow --json` prints one object per line instead, and `run` leaves stdout to the command it runs. Progress and warnings go to stderr.
- **Errors** are `{"ok":false,"code":"…","error":"…","fix":"…"}` on stdout in JSON mode, and `error:` / `fix:` lines on stderr always, the same shape as `install.sh`. `fix` is the next command to run. A failed deployment adds `"deployment"`.
- **Codes:** `USAGE`, `NOT_AUTHENTICATED`, `AUTHORIZATION_PENDING`, `NO_ORGANIZATION`, `NO_PROJECTS`, `PROJECT_REQUIRED`, `PROJECT_NOT_FOUND`, `SERVICE_NOT_FOUND`, `VARIABLE_NOT_FOUND`, `DEPLOYMENT_NOT_FOUND`, `DEPLOYMENT_RUNNING`, `DEPLOYMENT_FAILED`, `NOTHING_TO_SHIP`, `NAME_TAKEN` (project slug or service name in use), `TRACES_OFF` (no Axiom sink, or one without a traces dataset), `INVALID_INPUT`, `DISCOVERY_FAILED`, `NETWORK_ERROR`, `SERVER_ERROR`, `CONFIG_ERROR`, `TIMEOUT`, `CANCELLED`, `CONFLICT` (a domain or public port another service uses), `UNAVAILABLE` (the install can't do it yet), `FORBIDDEN` (signed in, not allowed), `NOT_FOUND` (anything else that isn't there), `RATE_LIMITED` (too many attempts; `fix` says when to retry). Errors from the server carry their code as is: the API's codes are this vocabulary, and the CLI only adds `fix`.
- **Exit codes:** 0 ok, 1 error, 2 usage, 4 not logged in (or the login awaits approval), 130 interrupted (`logs -f` exits 0 on Ctrl-C). `run` exits with its command's code (128 + signal when it was killed).
- **No prompts without a terminal.** Missing input is a `USAGE` error naming the flag.
- **Times** are RFC 3339 in UTC.

## Auth and context

`keel login <dashboard-url>` first checks the URL is a Keel install (`GET /api/meta`; anything else is `DISCOVERY_FAILED`). Then it uses device authorization (RFC 8628: `POST /api/auth/device/code`, then `POST /api/auth/device/token` until approved). It prints a link to the dashboard's `/device` page with a code (`ABCD-EFGH`, valid 30 minutes). Whoever opens it, signed in, sees the code and approves or denies; approving gives the CLI a session as their account. No password ever reaches the CLI.

- **With a terminal** it waits for the approval (Ctrl-C stops waiting; the link stays valid).
- **Without one, or with `--json` / `--no-wait`**, it returns at once: `{"ok":true,"status":"pending","approvalUrl":"…","code":"ABCD-EFGH","expiresAt":"…","next":"…"}`. The agent sends `approvalUrl` to its human and carries on. The first command after the approval finishes the login and runs; before it, commands fail with `AUTHORIZATION_PENDING` (exit 4) and the link in `fix`. `keel login --wait` blocks until approved instead.
- **Re-running** `keel login` while the link is valid prints the same link; when already logged in it prints `"status":"loggedIn"` and changes nothing. `keel logout` first to switch accounts.

The session token comes straight from the device token poll, handed out once, and is saved in `~/.config/keel/config.json` (0600; `KEEL_CONFIG_DIR` moves it), along with a pending login's device code. Every API call sends it as `Authorization: Bearer <token>`; each signed-in command first checks it with `GET /api/me` (a session the install no longer knows is `NOT_AUTHENTICATED`, "Session expired or signed out"; a URL that doesn't answer as Keel's API is `DISCOVERY_FAILED` as in `keel login`). `keel logout` revokes it with `POST /api/auth/sign-out`. Sessions last 7 days and renew while used.

Without a saved login, set `KEEL_URL` (dashboard) and `KEEL_TOKEN` (from `keel token`). Dashboard and API share one origin, so the dashboard URL is all the CLI needs, a dev one too (Vite proxies `/api` to `keel serve`).

- **Install:** `KEEL_URL`, else `--instance` / `KEEL_INSTANCE`, else the directory's link, else the last login.
- **Project:** `--project` / `KEEL_PROJECT`, else the directory's link (nearest parent), else the only project. Several projects and none picked is `PROJECT_REQUIRED`; none at all is `NO_PROJECTS`. On a fresh install the first account founds the organization with its first `keel project create`, if it never opened the dashboard's home page.
- **Environment:** production for now.
- **Services:** named by name (unique per environment) or id.

## Layout

```
cmd/keel/                 main: signals, then cli.Execute
internal/cli/             root.go: global flags, error funnel, exit codes, the server subcommands
                          (serve, openapi; proxy, agent through Extra);
                          one file per noun; commands resolve the target, call, print
internal/cli/output/      the contract above: printer, error codes, exit codes
internal/cli/config/      config.json: installs and directory links
internal/cli/client/      the HTTP API client: discovery (/api/meta), device login, typed calls
                          over internal/api's wire types; the CLI's printed types; problem
                          codes passed through, fixes added (withFix)
```

The version is `cli.Version` (`-ldflags "-X github.com/ThallesP/keel/internal/cli.Version=1.2.3"`); requests carry `User-Agent: keel-cli/<version>`.

A new command: add the API call to `internal/cli/client/api.go` (decode into `internal/api` types; return the CLI's types from `types.go`, whose JSON is what the CLI prints), then a cobra command in `internal/cli` that ends in `a.out.Result(v, human)`. A new server error code needs nothing here unless it deserves a `fix` (`withFix` in `client/client.go`); a code agents branch on is listed above.

## Next

- Scoped API tokens for CI, instead of a full session in `KEEL_TOKEN`.
- `stop` / `start`, `expose` (https domain or tcp/udp port through keel-proxy; `POST /api/nodes/{id}/expose` takes the same options as the dashboard); `database create` / `cache create` with `--engine`.
- `var set --stdin` so secrets stay out of argv.
- `node list`, `--environment` once there is more than production.
- `keel mcp` and an `llms.txt` generated from the command tree.
- `up` once Keel builds from source.
