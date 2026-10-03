# keel CLI

Keel from the terminal, for agents first and people too. A thin client over the same public Convex functions the dashboard calls; no API of its own.

## Build

```bash
cd apps/cli && go build -o bin/keel ./cmd/keel
```

Go 1.27. Its own module, outside bun and turbo; CI runs `gofmt`, `go vet` and `go test` (`ci.yml`, job `cli`), and the install job logs in with it against a fresh install.

## Use

```bash
keel login https://keel.example.ts.net          # prompts for email and password
keel link acme-support                          # this directory → that project
keel status
keel logs api -n 200
keel var set api LOG_LEVEL=debug                # staged, like the dashboard
keel ship                                       # deploy staged changes, wait for the result
keel redeploy api                               # pull the image again and roll out
```

| Command | What it does |
| --- | --- |
| `login <dashboard-url>` | Sign in with email and password; reads the Convex URLs from the dashboard's `/config.js` |
| `logout` | Revoke the session and forget the install |
| `whoami` | Account, organization, install |
| `token` | Print the session token, for `KEEL_TOKEN` |
| `project list` | Projects of the organization |
| `link [project]` / `unlink` | Pin a directory (and its subdirectories) to a project |
| `status` | Services, staged changes, last deployment |
| `service list` | Services, databases, caches, volumes |
| `logs <service> [-n N] [-f]` | Last N lines (default 100); `-f` polls until interrupted |
| `var list <service> [--show-secrets]` | Variables; secret values are `null` unless asked |
| `var set <service> KEY=VALUE... [--secret]` | Stage variables |
| `var delete <service> KEY...` | Stage deletions; fails without deleting anything if a key is missing |
| `ship [service...] [-d]` | Deploy staged changes (all, or only these) |
| `redeploy <service...> [-d]` | Roll out again with a fresh image pull |
| `deployment list <service>` | Last 20 deployments that touched a service |
| `deployment get [id] [--wait]` | One deployment with steps and log; the latest without an id |

`ship`, `redeploy` and `deployment get --wait` wait until the deployment settles, stream its log to stderr, and exit non-zero if it fails. `-d/--detach` returns once it starts.

## Contract

What agents rely on. Fields and codes are only ever added.

- **stdout is results only.** Text or a table, or with `--json` / `KEEL_JSON=1` exactly one JSON object: `{"ok":true,...}`. `logs --follow --json` prints one object per line instead. Progress and warnings go to stderr.
- **Errors** are `{"ok":false,"code":"…","error":"…","fix":"…"}` on stdout in JSON mode, and `error:` / `fix:` lines on stderr always, the same shape as `install.sh`. `fix` is the next command to run. A failed deployment adds `"deployment"`.
- **Codes:** `USAGE`, `NOT_AUTHENTICATED`, `NO_ORGANIZATION`, `NO_PROJECTS`, `PROJECT_REQUIRED`, `PROJECT_NOT_FOUND`, `SERVICE_NOT_FOUND`, `VARIABLE_NOT_FOUND`, `DEPLOYMENT_NOT_FOUND`, `DEPLOYMENT_RUNNING`, `DEPLOYMENT_FAILED`, `NOTHING_TO_SHIP`, `INVALID_INPUT`, `DISCOVERY_FAILED`, `NETWORK_ERROR`, `SERVER_ERROR`, `CONFIG_ERROR`, `TIMEOUT`, `CANCELLED`.
- **Exit codes:** 0 ok, 1 error, 2 usage, 4 not logged in, 130 interrupted (`logs -f` exits 0 on Ctrl-C).
- **No prompts without a terminal.** Missing input is a `USAGE` error naming the flag.
- **Times** are RFC 3339 in UTC.

## Auth and context

`keel login` signs in through better-auth and saves the session token in `~/.config/keel/config.json` (0600; `KEEL_CONFIG_DIR` moves it). Each run exchanges it for a 15-minute Convex JWT at `/api/auth/convex/token`. Sessions last 7 days and renew while used.

Without a saved login, set `KEEL_URL` (dashboard) and `KEEL_TOKEN` (from `keel token`). A dev server's `/config.js` is empty: pass `--convex-url` / `--convex-site-url` to `login`, or set `KEEL_CONVEX_URL` / `KEEL_CONVEX_SITE_URL`.

- **Install:** `KEEL_URL`, else `--instance` / `KEEL_INSTANCE`, else the directory's link, else the last login.
- **Project:** `--project` / `KEEL_PROJECT`, else the directory's link (nearest parent), else the only project. Several projects and none picked is `PROJECT_REQUIRED`.
- **Environment:** production for now.
- **Services:** named by name (unique per environment) or id.

## Layout

```
cmd/keel/          main: signals, version
internal/output/   the contract above: printer, error codes, exit codes
internal/config/   config.json: installs and directory links
internal/convex/   Convex HTTP API client (/api/query|mutation|action)
internal/keel/     discovery, sign-in, typed calls; maps Convex errors to codes
internal/cli/      one file per noun; commands resolve the target, call, print
```

A new command: add the Convex call to `internal/keel/api.go` (its types are the JSON the CLI prints), then a cobra command in `internal/cli` that ends in `a.out.Result(v, human)`. Server errors the CLI should branch on get a code in `translate`.

## Next

- Device-code login (`keel login` approves in the dashboard) and scoped API tokens for CI, both better-auth plugins.
- `service create --image`, `stop` / `start`, `expose` (returns the Quick Tunnel URL).
- `run -- <cmd>` with the service's resolved variables; `var set --stdin` so secrets stay out of argv.
- `node list`, `--environment` once there is more than production.
- `keel mcp` and an `llms.txt` generated from the command tree.
- `up` once Keel builds from source.
