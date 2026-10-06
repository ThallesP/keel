# Keel

Self-hosted deploy platform. An alternative to Coolify and Dokploy, built around the UI and the deploy experience.

- **Canvas first.** A project is a canvas: services, databases and caches are nodes, and variables wire them together.
- **Multi-server by default.** Docker Swarm under the hood; a single server is a first-class cluster.
- **Almost no networking for you.** Servers, the control plane and your containers talk over [Tailscale](https://tailscale.com). Public traffic enters through one proxy on the control plane, with HTTPS certificates handled for you: open ports 80 and 443 there (plus any TCP/UDP port you expose) and that is the whole job.

> Early and moving fast. Expect breaking changes before 1.0.

## Install

On a fresh Linux server (amd64 or arm64, 2 GB RAM or more):

```bash
curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh | sudo bash
```

If the server isn't on your tailnet yet, the installer prints a Tailscale login link and waits. When it finishes, it prints the dashboard URL (`http://<tailnet-ip>`). Open it from any device on your tailnet and sign up.

To put services on the internet, let ports 80 and 443 (TCP) reach this server: its cloud firewall, `ufw`, or a port-forward on your router. Each TCP or UDP port you expose later (a database, a game server) needs the same. The dashboard and Convex stay tailnet-only either way.

You need a Tailscale account (the free plan works). Reading [install.sh](install.sh) before piping it to a shell is a good habit; it is one file.

### What the installer does

1. Installs Docker ([get.docker.com](https://get.docker.com)) and Tailscale ([tailscale.com/install.sh](https://tailscale.com/install.sh)) if they are missing, and joins the tailnet. Looks up the server's public IPv4 (`api.ipify.org`), which names the HTTPS domains of exposed services.
2. Writes its state to `/opt/keel`. `.env` (mode 0600) holds generated secrets and settings; `compose.yml` defines the control plane.
3. Initialises Docker Swarm on the tailnet address and creates the `keel` overlay network.
4. Starts the control plane with Docker Compose (`docker compose -p keel`):
   - `backend`: a self-hosted [Convex](https://github.com/get-convex/convex-backend) backend with the Docker socket mounted.
   - `web`: the dashboard.
   - `proxy`: `keel-proxy` ([Caddy](https://caddyserver.com) with [caddy-l4](https://github.com/mholt/caddy-l4)), the public way in to exposed services. It listens on nothing until you expose something; see [docs/networking.md](docs/networking.md).
5. Pushes Keel's backend functions and settings to Convex, runs its data migrations and syncs the proxy.
6. Starts the `keel-worker` global service.
7. Checks that everything answers, then prints the URLs.

The control plane's own ports bind to the tailnet IP only:

| Port | What                |
| ---- | ------------------- |
| 80   | Dashboard           |
| 3210 | Convex API          |
| 3211 | Convex HTTP actions |

Swarm's ports (2377, 7946, 4789) are on the tailnet as well. The proxy binds the server's other addresses (never the tailnet one, never loopback): 80 and 443 once a service has an HTTPS endpoint, and each TCP/UDP port you expose. The first account created owns the install; after that, sign-up is by invitation only (account menu → Invite people).

### Upgrade

Re-run the install command. It is idempotent: secrets are kept, images are pulled again, the functions are pushed again, and the worker is updated in place.

Pin a version with `KEEL_VERSION` (an image tag such as `1.2.3` or `sha-abc1234`). The pin is remembered by later runs.

### Options

All optional. Values you pass are saved in `/opt/keel/.env` and reused by later runs.

| Variable                 | Default            |                                                                                                                        |
| ------------------------ | ------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| `KEEL_TAILSCALE_AUTHKEY` | –                  | `tskey-auth-…` [auth key](https://login.tailscale.com/admin/settings/keys) to join the tailnet without a browser login |
| `KEEL_VERSION`           | `latest`           | Image tag for `keel-web`, `keel-functions`, `keel-worker`, `keel-proxy`                                                |
| `KEEL_PUBLIC_IP`         | detected           | The server's public IPv4: what exposed services' domains and ports point at (behind NAT, the router's)                 |
| `KEEL_WEB_PORT`          | `80`               | Dashboard port                                                                                                         |
| `KEEL_JSON`              | –                  | `1`: print one JSON result object on stdout (progress stays on stderr)                                                 |
| `KEEL_ADDR`              | tailnet IP         | Bind to this IP instead and skip Tailscale (LAN or CI only; not what you want in production)                           |
| `KEEL_DIR`               | `/opt/keel`        | State directory                                                                                                        |
| `KEEL_REF`               | `main`             | Git ref that `compose.yml` and the scripts are fetched from                                                            |
| `KEEL_SRC`               | –                  | Use a local checkout instead of fetching (development)                                                                 |
| `KEEL_IMAGE_PREFIX`      | `ghcr.io/thallesp` | Image registry and namespace                                                                                           |
| `KEEL_PULL`              | `1`                | `0`: use images already present locally                                                                                |

Pass them to the root side of the pipe: `curl -fsSL … | sudo KEEL_VERSION=1.2.3 bash`.

### Uninstall

```bash
sudo docker compose -p keel -f /opt/keel/compose.yml down   # control plane
sudo docker service rm keel-worker                          # per-node worker
sudo docker service ls -q --filter label=keel.service | xargs -r sudo docker service rm  # your services
```

Data is kept: the Convex volume `keel_convex-data`, the proxy's certificates (`keel_proxy-data`) and your services' volumes. Remove them with `docker volume rm` and delete `/opt/keel` to start from scratch. Leave the Swarm with `docker swarm leave --force` only if nothing else uses it.

### Troubleshooting

The installer stops at the first problem and prints an `error:` line plus a `fix:` line. Re-running it after fixing the problem is always safe.

- **Logs:** `sudo docker compose -p keel logs backend web proxy` for the control plane, and `sudo docker service logs keel-worker` for the worker.
- **An exposed service says "Open ports 80 and 443…":** the certificate authority could not reach the server. Check the firewall or router; the endpoint turns live by itself once it can.
- **"pushing functions failed":** the backend installs `dockerode` from npm on the first push, so it needs outbound access to `registry.npmjs.org`.
- **"rejected the stored admin key":** delete the `CONVEX_SELF_HOSTED_ADMIN_KEY` line from `/opt/keel/.env` and re-run.

## Install with an agent

Everything above works unattended. Give your agent (Claude Code, Codex, etc.) shell access to the server and this:

```bash
curl -fsSL https://raw.githubusercontent.com/ThallesP/keel/main/install.sh \
  | sudo KEEL_TAILSCALE_AUTHKEY=tskey-auth-... KEEL_JSON=1 bash
```

The contract:

- **Output:** stdout is exactly one JSON object and progress goes to stderr.
  - Success prints `{"ok":true,"url":…,"convexUrl":…,"convexSiteUrl":…,"version":…,"stateDir":"/opt/keel","publicIp":…}` and exits 0. `publicIp` is `""` when it could not be detected.
  - Failure prints `{"ok":false,"error":…,"fix":…}` and exits non-zero. `fix` is the next step to try.
- **Without `KEEL_TAILSCALE_AUTHKEY`:** if the server is not on a tailnet yet, the installer prints a login URL on stderr and waits up to 15 minutes. Relay the URL to a human.
- **Idempotent:** re-running is safe, never rotates secrets, and is also how you upgrade.
- **Verify:**
  ```bash
  curl -fsS "$convexUrl/version"                                  # Convex backend
  curl -fsS "$url/config.js"                                      # dashboard + its runtime config
  sudo docker compose -p keel ps                                  # backend, web, proxy: healthy
  sudo docker service ls --filter name=keel-worker                # replicas n/n
  ```

## Development

Requirements: [Bun](https://bun.sh) 1.4+, Docker, Tailscale.

```bash
bun install
bun run dev:setup                 # configure a Convex deployment (a local one works)
bun run dev                       # web on http://localhost:3001 + `convex dev`
scripts/dev-https.sh              # optional: https://<node>.<tailnet>.ts.net, tailnet-only
scripts/bootstrap-swarm.sh        # one-time: Swarm on the tailnet IP, `keel` network, keel-worker
```

`scripts/dev-https.sh` puts web and Convex behind `tailscale serve` on this machine's MagicDNS name (443, 8443, 10000), then points `apps/web/.env` and the deployment's `SITE_URL` at it. Needs HTTPS certificates enabled for the tailnet. Undo with `sudo tailscale serve reset`.

`scripts/deploy-worker.sh` rebuilds and redeploys the worker after changes to `apps/worker`. Against a local anonymous Convex deployment, export `CONVEX_AGENT_MODE=anonymous` first.

The web app reads `VITE_CONVEX_URL` / `VITE_CONVEX_SITE_URL` from `apps/web/.env` in dev. An installed Keel serves them at runtime from `/config.js` instead (`apps/web/src/lib/config.ts`), so one image works on every server.

Checks: `bun run check-types`, `bun run check` (Oxlint + Oxfmt).

Auth is Better Auth on a locally installed Convex component (`packages/backend/convex/betterAuth`). After changing its plugins in `convex/auth.ts`, regenerate the component schema: `bun scripts/generate-auth-schema.ts` in `packages/backend`.

### Layout

```
apps/
  web/        dashboard (React, TanStack Router, React Flow canvas)
  worker/     per-node worker: Docker events and container logs to the control plane
  proxy/      keel-proxy: Caddy plugin (host-namespace listeners, certificate reports) + image
  fumadocs/   docs site
packages/
  backend/    Convex control plane: schema, functions, Swarm reconciler
  ui/         shared shadcn/ui components
deploy/       compose.yml for the control plane, functions image
scripts/      Swarm bootstrap and worker deploy (used by install.sh too)
docs/         design docs: canvas, workers, networking, volumes, logs, agents
install.sh    the installer
```

[`ci.yml`](.github/workflows/ci.yml) typechecks, then runs `install.sh` end to end on a fresh runner, twice. It runs on every pull request, and as the first job of [`images.yml`](.github/workflows/images.yml), which builds the images on every push to `main` (`:latest`, `:sha-<short>`) and on `v*` tags and publishes them only when `ci.yml` passed.

## License

[MIT](LICENSE)
