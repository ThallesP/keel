# Keel

Self-hosted deploy platform. An alternative to Coolify and Dokploy, built around the UI and the deploy experience.

- **Canvas first.** A project is a canvas: services, databases and caches are nodes, and variables wire them together.
- **Multi-server by default.** Docker Swarm under the hood; a single server is a first-class cluster.
- **Almost no networking for you.** Servers, the control plane and your containers talk over [Tailscale](https://tailscale.com). Public traffic enters through one proxy on the control plane, with HTTPS certificates handled for you: open ports 80 and 443 there (plus any TCP/UDP port you expose) and that is the whole job.

Keel is one Go binary, `keel`: `keel serve` is the control plane (API, dashboard, SQLite database, Swarm driver), `keel proxy` the public edge next to it, `keel agent` runs on every server, and its other commands are the CLI.

> Early and moving fast. Expect breaking changes before 1.0.

## Install

On a fresh Linux server (amd64 or arm64, 2 GB RAM or more):

```bash
curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash
```

If the server isn't on your tailnet yet, the installer prints a Tailscale login link and waits. When it finishes, it prints the dashboard URL (`http://<tailnet-ip>`). Open it from any device on your tailnet and sign up.

To put services on the internet, let ports 80 and 443 (TCP) reach this server: its cloud firewall, `ufw`, or a port-forward on your router. Each TCP or UDP port you expose later (a database, a game server) needs the same. The dashboard and its API stay tailnet-only either way.

You need a Tailscale account (the free plan works). Reading [install.sh](install.sh) before piping it to a shell is a good habit; it is one file.

### What the installer does

1. Installs Docker ([get.docker.com](https://get.docker.com)) and Tailscale ([tailscale.com/install.sh](https://tailscale.com/install.sh)) if they are missing, and joins the tailnet. Looks up the server's public IPv4 (`api.ipify.org`), which names the HTTPS domains of exposed services.
2. Writes its state to `/opt/keel`. `.env` (mode 0600) holds generated secrets and settings; `compose.yml` defines the control plane.
3. Initialises Docker Swarm on the tailnet address and creates the `keel` overlay network.
4. On a server that ran the Convex-era Keel, moves its data into the new database first (see [Upgrade](#upgrade)). Then starts the control plane with Docker Compose (`docker compose -p keel`): two containers from one image, `ghcr.io/thallesp/keel`.
   - `keel`: `keel serve`, the dashboard and its API. Its SQLite database lives in the `keel_keel-data` volume; it has the Docker socket mounted, because it drives Swarm.
   - `proxy`: `keel proxy` ([Caddy](https://caddyserver.com) with [caddy-l4](https://github.com/mholt/caddy-l4)), the public way in to exposed services. It listens on nothing until you expose something and never gets the Docker socket; see [docs/networking.md](docs/networking.md).
5. `keel serve` runs its data migrations, syncs the proxy and starts the `keel-agent` global service: one agent per server, forwarding Docker events and shipping container logs.
6. Checks that everything answers, then prints the URL.

The control plane's own ports bind to the tailnet IP only:

| Port | What                                                                                                    |
| ---- | ------------------------------------------------------------------------------------------------------- |
| 80   | Dashboard and API, plus the routes agents, the proxy and traced services call (`/worker`, `/proxy`, `/otlp`) |

Swarm's ports (2377, 7946, 4789) are on the tailnet as well. The proxy binds the server's other addresses (never the tailnet one, never loopback): 80 and 443 once a service has an HTTPS endpoint, and each TCP/UDP port you expose. The first account created owns the install; after that, sign-up is by invitation only (account menu → Invite people).

### Upgrade

Re-run the install command. It is idempotent: secrets are kept, the image is pulled again, and `keel serve` updates the agent on every server itself.

Pin a version with `KEEL_VERSION` (an image tag such as `1.2.3` or `sha-abc1234`). The pin is remembered by later runs.

**From a Convex-era install** (Keel before the single binary), the installer moves your data before the new control plane first starts. While the old Convex backend still runs (it starts it from the saved `compose.convex.yml` if needed), it exports the deployment with the old `keel-functions` image into `/opt/keel/convex-export/snapshot.zip`, then imports that into the new database with `keel import-convex` (report in `/opt/keel/convex-import.json`, and as `convexImport` in JSON). Accounts keep their passwords and CLI logins keep working; the dashboard asks you to sign in again. If the export or the import fails, the installer stops with an `error:` and a `fix:` line, the old control plane keeps running, and re-running retries. Certificates carry over (`keel_proxy-data`), the old `keel-worker` service gives way to `keel-agent`, and the Convex-era secrets stay in `/opt/keel/.env`.

The installer never deletes the old data: the volume `keel_convex-data`, the snapshot and `compose.convex.yml` stay until you remove them, once the dashboard shows everything (`sudo rm -r /opt/keel/convex-export /opt/keel/compose.convex.yml`, `sudo docker volume rm keel_convex-data`). To start empty instead, re-run with `KEEL_SKIP_CONVEX_IMPORT=1`: every later run then warns that the data is not imported (`"convexImportNeeded": true` in JSON) until someone signs up. Meanwhile deployed services keep running, but Keel does not manage them and exposed ones lose their public endpoints.

### Options

All optional. Values you pass are saved in `/opt/keel/.env` and reused by later runs.

| Variable                 | Default            |                                                                                                                                                                                                                                 |
| ------------------------ | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `KEEL_TAILSCALE_AUTHKEY` | –                  | `tskey-auth-…` [auth key](https://login.tailscale.com/admin/settings/keys) to join the tailnet without a browser login                                                                                                          |
| `KEEL_VERSION`           | `latest`           | Tag of the `keel` image                                                                                                                                                                                                         |
| `KEEL_PUBLIC_IP`         | detected           | The server's public IPv4: what exposed services' domains and ports point at (behind NAT, the router's). Detected again on every run unless you set it; if detection fails, the last value is kept. Default domains move with it |
| `KEEL_ACME_EMAIL`        | –                  | Email for HTTPS certificates. Adds ZeroSSL after Let's Encrypt, so the shared sslip.io quota running out does not stop new domains                                                                                              |
| `KEEL_ACME_CA`           | –                  | Testing only: ACME directory that replaces every certificate issuer (e.g. Let's Encrypt staging). Not saved: pass it on every run that wants it                                                                                  |
| `KEEL_WEB_PORT`          | `80`               | Dashboard port                                                                                                                                                                                                                  |
| `KEEL_JSON`              | –                  | `1`: print one JSON result object on stdout (progress stays on stderr)                                                                                                                                                          |
| `KEEL_ADDR`              | tailnet IP         | Bind to this IP instead and skip Tailscale (LAN or CI only; not what you want in production). The dashboard then holds port 80 on that address, so HTTPS endpoints fail there unless `KEEL_WEB_PORT` moves it                   |
| `KEEL_DIR`               | `/opt/keel`        | State directory                                                                                                                                                                                                                 |
| `KEEL_REF`               | `main`             | Git ref that `compose.yml` and the Swarm bootstrap script are fetched from                                                                                                                                                      |
| `KEEL_SRC`               | –                  | Use a local checkout instead of fetching (development)                                                                                                                                                                          |
| `KEEL_IMAGE_PREFIX`      | `ghcr.io/thallesp` | Image registry and namespace                                                                                                                                                                                                    |
| `KEEL_PULL`              | `1`                | `0`: use images already present locally                                                                                                                                                                                         |
| `KEEL_SKIP_CONVEX_IMPORT` | –                 | `1`: when upgrading a Convex-era install, start without its data instead of exporting and importing it (see [Upgrade](#upgrade)). The data stays in `keel_convex-data`. Not saved                                                |

Pass them to the root side of the pipe: `curl -fsSL … | sudo KEEL_VERSION=1.2.3 bash`.

### Uninstall

```bash
sudo docker compose -p keel --env-file /opt/keel/.env -f /opt/keel/compose.yml down   # control plane
sudo docker service rm keel-agent                                                      # per-node agent
sudo docker service ls -q --filter label=keel.service | xargs -r sudo docker service rm  # your services
```

Data is kept: the database (`keel_keel-data`), the proxy's certificates (`keel_proxy-data`), your services' volumes and, on servers that ran the Convex-era Keel, `keel_convex-data`. Remove them with `docker volume rm` and delete `/opt/keel` to start from scratch. Leave the Swarm with `docker swarm leave --force` only if nothing else uses it.

### Troubleshooting

The installer stops at the first problem and prints an `error:` line plus a `fix:` line. Re-running it after fixing the problem is always safe.

- **Logs:** `sudo docker compose -p keel logs keel proxy` for the control plane, and `sudo docker service logs keel-agent` for the agents.
- **An exposed service says "Open ports 80 and 443…":** the certificate authority could not reach the server. Check the firewall or router; the endpoint turns live by itself once it can.
- **"exporting the Convex-era data failed" / "importing the Convex-era data failed":** nothing new was started and the old control plane keeps running; fix what the output says and re-run. The data is safe in `keel_convex-data`; see [Upgrade](#upgrade).

## Install with an agent

Everything above works unattended. Give your agent (Claude Code, Codex, etc.) shell access to the server and this:

```bash
curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh \
  | sudo KEEL_TAILSCALE_AUTHKEY=tskey-auth-... KEEL_JSON=1 bash
```

The contract:

- **Output:** stdout is exactly one JSON object and progress goes to stderr.
  - Success prints `{"ok":true,"url":…,"apiUrl":…,"convexUrl":…,"convexSiteUrl":…,"version":…,"stateDir":"/opt/keel","publicIp":…,"convexImportNeeded":false,"convexImport":null,"warnings":[]}` and exits 0. `publicIp` is `""` when it could not be detected. `apiUrl`, `convexUrl` and `convexSiteUrl` all equal `url` (the last two predate the single control plane and stay for older readers). `warnings` repeats every `warning:` line of the run.
  - Upgrading a Convex-era install: `convexImport` is what this run imported, `{"imported":{<table>:n,…},"skipped":{<table>:n,…},"warnings":[…]}` (else `null`). `convexImportNeeded` is `true` when the data is not imported (only after `KEEL_SKIP_CONVEX_IMPORT=1`): it is safe in the volume `keel_convex-data` (see [Upgrade](#upgrade)). Tell your human either way.
  - Failure prints `{"ok":false,"error":…,"fix":…,"warnings":[…]}` and exits non-zero. `fix` is the next step to try.
- **Without `KEEL_TAILSCALE_AUTHKEY`:** if the server is not on a tailnet yet, the installer prints a login URL on stderr and waits up to 15 minutes. Relay the URL to a human.
- **Idempotent:** re-running is safe, never rotates secrets, and is also how you upgrade.
- **Verify:**
  ```bash
  curl -fsS "$url/api/meta"                                       # control plane: name, version, siteUrl
  curl -fsS "$url/config.js"                                      # dashboard runtime config
  sudo docker compose -p keel ps                                  # keel, proxy: healthy
  sudo docker service ls --filter name=keel-agent                 # replicas n/n
  ```

## Development

Requirements: [Go](https://go.dev) 1.27+, [Bun](https://bun.sh) 1.4+, Docker, Tailscale.

```bash
bun install
make dev                          # the next two together, or each in its own terminal:
KEEL_LISTEN=127.0.0.1:3400 KEEL_DATA_DIR=./.keel KEEL_SITE_URL=http://localhost:3001 go run ./cmd/keel serve
bun run dev:web                   # dashboard on http://localhost:3001 (Vite)
scripts/dev-https.sh              # optional: https://<node>.<tailnet>.ts.net, tailnet-only
scripts/bootstrap-swarm.sh        # one-time: Swarm on the tailnet IP and the `keel` overlay network
```

In development `keel serve` listens on `127.0.0.1:3400` with its database in `./.keel`, and Vite (3001) proxies `/api`, `/worker`, `/otlp`, `/proxy` and `/config.js` to it, so the dashboard and the API share one origin as they do on an install. An installed Keel serves the built dashboard from the binary itself and its runtime settings from `/config.js`: never bake deployment URLs into the web build. `KEEL_SITE_URL` is the URL people open (device-login links and cookies use it).

`scripts/dev-https.sh` puts Vite behind `tailscale serve` on this machine's MagicDNS name (443); start `keel serve` with the `KEEL_SITE_URL` it prints. Needs HTTPS certificates enabled for the tailnet. Undo with `sudo tailscale serve reset`.

`keel serve` manages the `keel-agent` global service only when `KEEL_AGENT_IMAGE` is set (an install sets it to its own image).

Checks: `gofmt -l`, `go vet ./...`, `go test ./...`, `bun run check-types`, `bun run check` (Oxlint + Oxfmt). The architecture and the porting specs are in [docs/go](docs/go/ARCHITECTURE.md).

### Layout

```
cmd/keel/     main: one binary for serve, proxy, agent and the CLI
internal/     domain, app (use cases), adapters (SQLite, Swarm, Caddy, Axiom), transport/http,
              serve, agent, proxy, cli
apps/
  web/        dashboard (React, TanStack Router, React Flow canvas), embedded into the binary
  fumadocs/   docs site
packages/
  ui/         shared shadcn/ui components
Dockerfile    the image, ghcr.io/thallesp/keel
deploy/       compose.yml for the control plane
scripts/      Swarm bootstrap (used by install.sh too), dev HTTPS
docs/         design docs: canvas, workers, networking, volumes, logs, agents; go/: architecture, porting specs
install.sh    the installer
```

`apps/cli`, `apps/proxy`, `apps/worker` and `packages/backend` are the Convex-era code the binary replaces; they go away once the port lands.

[`ci.yml`](.github/workflows/ci.yml) runs the Go checks and the web typecheck and build, then runs `install.sh` end to end on a fresh runner: twice, then once more over a Convex-era data volume. It runs on every pull request, and as the first job of [`images.yml`](.github/workflows/images.yml), which builds the image on every push to `main` (`:latest`, `:sha-<short>`) and on `v*` tags and publishes it only when `ci.yml` passed.

## License

[MIT](LICENSE)
