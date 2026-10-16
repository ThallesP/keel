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

If the server isn't on your tailnet yet, the installer prints a Tailscale login link and waits. If your tailnet has HTTPS certificates off, it asks you to turn them on once ([DNS page of the Tailscale admin console](https://login.tailscale.com/admin/dns) → Enable HTTPS) and waits for that too; press Enter to skip. When it finishes, it prints the dashboard URL, `https://<server>.<tailnet>.ts.net` (or `http://<tailnet-ip>` without HTTPS). Open it from any device on your tailnet and sign up.

Turning on HTTPS is a tailnet-wide switch that only a person can flip, and it publishes the names of the servers that get a certificate in public Certificate Transparency logs ([Tailscale's note](https://tailscale.com/kb/1153/enabling-https)). Without it Keel still runs, but browsers treat the dashboard as insecure, so Sign in with Axiom (and copying to the clipboard) does not work.

To put services on the internet, let ports 80 and 443 (TCP) reach this server: its cloud firewall, `ufw`, or a port-forward on your router. Each TCP or UDP port you expose later (a database, a game server) needs the same. The dashboard and its API stay tailnet-only either way.

You need a Tailscale account (the free plan works). Reading [install.sh](install.sh) before piping it to a shell is a good habit; it is one file.

### What the installer does

1. Installs Docker ([get.docker.com](https://get.docker.com)) and Tailscale ([tailscale.com/install.sh](https://tailscale.com/install.sh)) if they are missing, and joins the tailnet. Orders Docker after Tailscale at boot, so the control plane can bind its tailnet address after a reboot: `/etc/systemd/system/docker.service.d/10-wait-tailscale.conf`, plus `/etc/systemd/system/tailscale-wait-online.service.d/10-keel-timeout.conf` so Docker still starts after 90 s if the tailnet never comes up. Looks up the server's public IPv4 (`api.ipify.org`), which names the HTTPS domains of exposed services.
2. With HTTPS on for the tailnet, puts the dashboard on the server's MagicDNS name with `tailscale serve --bg --https=443` (tailnet only, no Funnel). This replaces whatever that server already served on 443 of its MagicDNS name.
3. Writes its state to `/opt/keel`. `.env` (mode 0600) holds generated secrets and settings; `compose.yml` defines the control plane; `registry/` holds the image registry's CA, certificate, password file and config, written by `keel serve`.
4. Initialises Docker Swarm on the tailnet address and creates the `keel` overlay network.
5. Starts the control plane with Docker Compose (`docker compose -p keel`): two containers from one image, `ghcr.io/thallesp/keel`, and the registry.
   - `keel`: `keel serve`, the dashboard and its API. Its SQLite database lives in the `keel_keel-data` volume; it has the Docker socket mounted, because it drives Swarm.
   - `proxy`: `keel proxy` ([Caddy](https://caddyserver.com) with [caddy-l4](https://github.com/mholt/caddy-l4)), the public way in to exposed services. It listens on nothing until you expose something and never gets the Docker socket; see [docs/networking.md](docs/networking.md).
   - `registry`: [zot](https://zotregistry.dev), the image registry every server pulls from, internal to Keel. Images live in the `keel_registry-data` volume; see [docs/registry.md](docs/registry.md).
6. Makes this server's Docker trust the registry: `/etc/docker/certs.d/<tailnet-ip>:5000/ca.crt`.
7. `keel serve` runs its schema migrations, syncs the proxy and starts the `keel-agent` global service: one agent per server, forwarding Docker events and shipping container logs.
8. Checks that everything answers, then prints the URL.

The control plane's own ports bind to the tailnet IP only:

| Port | What                                                                                                    |
| ---- | ------------------------------------------------------------------------------------------------------- |
| 80   | Dashboard and API, plus the routes agents, the proxy and traced services call (`/worker`, `/proxy`, `/otlp`) |
| 443  | The dashboard over HTTPS on the server's MagicDNS name (`tailscale serve`), when the tailnet has HTTPS on |
| 5000 | Image registry (HTTPS with Keel's own CA, one account)                                                  |

Swarm's ports (2377, 7946, 4789) are on the tailnet as well. The proxy binds the server's other addresses (never the tailnet one, never loopback): 80 and 443 once a service has an HTTPS endpoint, and each TCP/UDP port you expose. The first account created owns the install; after that, sign-up is by invitation only (account menu → Invite people).

### Upgrade

Re-run the install command. It is idempotent: secrets are kept, the image is pulled again, and `keel serve` updates the agent on every server itself.

An install that was on `http://<tailnet-ip>` moves to `https://<server>.<tailnet>.ts.net` on the first run after HTTPS is on (the run warns about it). Sessions belong to one URL: sign in again at the new one, and run `keel login https://<server>.<tailnet>.ts.net` again wherever the CLI used the old URL.

The Convex-era Keel does not upgrade to this one. On a server that still has its `keel_convex-data` volume, the installer stops before changing anything, and its `fix:` line says how to remove the old install once your services are off it.

Pin a version with `KEEL_VERSION` (an image tag such as `1.2.3` or `sha-abc1234`). The pin is remembered by later runs.

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
| `KEEL_ADDR`              | tailnet IP         | Bind to this IP instead and skip Tailscale and its HTTPS (LAN or CI only; not what you want in production). The dashboard then holds port 80 on that address, so HTTPS endpoints fail there unless `KEEL_WEB_PORT` moves it   |
| `KEEL_DIR`               | `/opt/keel`        | State directory                                                                                                                                                                                                                 |
| `KEEL_REF`               | `main`             | Git ref that `compose.yml` and the Swarm bootstrap script are fetched from                                                                                                                                                      |
| `KEEL_SRC`               | –                  | Use a local checkout instead of fetching (development)                                                                                                                                                                          |
| `KEEL_IMAGE_PREFIX`      | `ghcr.io/thallesp` | Image registry and namespace                                                                                                                                                                                                    |
| `KEEL_PULL`              | `1`                | `0`: use images already present locally                                                                                                                                                                                         |

Pass them to the root side of the pipe: `curl -fsSL … | sudo KEEL_VERSION=1.2.3 bash`.

### Uninstall

```bash
sudo docker compose -p keel --env-file /opt/keel/.env -f /opt/keel/compose.yml down   # control plane
sudo docker service rm keel-agent                                                      # per-node agent
sudo docker service ls -q --filter label=keel.service | xargs -r sudo docker service rm  # your services
sudo tailscale serve --https=443 off                                                   # dashboard over HTTPS
sudo rm -r /etc/docker/certs.d/<tailnet-ip>:5000                                       # registry trust
sudo rm /etc/systemd/system/docker.service.d/10-wait-tailscale.conf \
  /etc/systemd/system/tailscale-wait-online.service.d/10-keel-timeout.conf && sudo systemctl daemon-reload
```

Data is kept: the database (`keel_keel-data`), the proxy's certificates (`keel_proxy-data`), the registry's images (`keel_registry-data`) and your services' volumes. Remove them with `docker volume rm` and delete `/opt/keel` to start from scratch. Leave the Swarm with `docker swarm leave --force` only if nothing else uses it.

### Troubleshooting

The installer stops at the first problem and prints an `error:` line plus a `fix:` line. Re-running it after fixing the problem is always safe.

- **Logs:** `sudo docker compose -p keel logs keel proxy registry` for the control plane, and `sudo docker service logs keel-agent` for the agents.
- **The dashboard is not answering after a reboot:** check that Tailscale is up (`tailscale status`). Docker waits for it at boot, for 90 s at most; once the tailnet is back, `sudo docker compose -p keel --env-file /opt/keel/.env -f /opt/keel/compose.yml up -d` (or re-running the installer) brings the control plane up.
- **An exposed service says "Open ports 80 and 443…":** the certificate authority could not reach the server. Check the firewall or router; the endpoint turns live by itself once it can.

## Install with an agent

Everything above works unattended. Give your agent (Claude Code, Codex, etc.) shell access to the server and this:

```bash
curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh \
  | sudo KEEL_TAILSCALE_AUTHKEY=tskey-auth-... KEEL_JSON=1 bash
```

The contract:

- **Output:** stdout is exactly one JSON object and progress goes to stderr.
  - Success prints `{"ok":true,"url":…,"apiUrl":…,"version":…,"stateDir":"/opt/keel","publicIp":…,"registry":"<tailnet-ip>:5000","warnings":[]}` and exits 0. `url` is `https://<server>.<tailnet>.ts.net` when the tailnet has HTTPS on, else `http://<tailnet-ip>`. `publicIp` is `""` when it could not be detected. `apiUrl` equals `url`. `warnings` repeats every `warning:` line of the run.
  - Failure prints `{"ok":false,"error":…,"fix":…,"warnings":[…]}` and exits non-zero. `fix` is the next step to try.
- **Without `KEEL_TAILSCALE_AUTHKEY`:** if the server is not on a tailnet yet, the installer prints a login URL on stderr and waits up to 15 minutes. Relay the URL to a human.
- **HTTPS off for the tailnet:** with `KEEL_JSON=1` the installer does not wait. It finishes on `http://<tailnet-ip>` with a warning naming https://login.tailscale.com/admin/dns. Relay it to a human (they select Enable HTTPS once), then re-run: that run moves the dashboard to HTTPS.
- **Idempotent:** re-running is safe, never rotates secrets, and is also how you upgrade.
- **Verify:**
  ```bash
  curl -fsS "$url/api/meta"                                       # control plane: name, version, siteUrl
  sudo docker compose -p keel ps                                  # keel, proxy: healthy; registry: running
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

In development `keel serve` listens on `127.0.0.1:3400` with its database in `./.keel`, and Vite (3001) proxies `/api`, `/worker`, `/otlp` and `/proxy` to it, so the dashboard and the API share one origin as they do on an install. An installed Keel serves the built dashboard from the binary itself, on the API's origin, so the dashboard needs no runtime config: never bake deployment URLs into the web build. `KEEL_SITE_URL` is the URL people open (device-login links and cookies use it).

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

The CLI is `internal/cli` (contract in [`docs/cli.md`](docs/cli.md)).

[`ci.yml`](.github/workflows/ci.yml) runs the Go checks and the web typecheck and build, then runs `install.sh` end to end on a fresh runner, twice. It runs on every pull request, and as the first job of [`images.yml`](.github/workflows/images.yml), which builds the image on every push to `main` (`:latest`, `:sha-<short>`) and on `v*` tags and publishes it only when `ci.yml` passed.

## License

[MIT](LICENSE)
