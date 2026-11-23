# Development

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

Checks: `gofmt -l`, `go vet ./...`, `go test ./...`, `bun run check-types`, `bun run check` (Oxlint + Oxfmt).

## Layout

```
cmd/keel/     main: one binary for serve, proxy, agent and the CLI
internal/     domain, app (use cases), adapters (SQLite, Swarm, Caddy, Axiom), transport/http,
              serve, agent, proxy, cli
apps/
  web/        dashboard (React, TanStack Router, React Flow canvas), embedded into the binary
packages/
  ui/         shared shadcn/ui components
Dockerfile    the image, ghcr.io/thallesp/keel
deploy/       compose.yml for the control plane
scripts/      Swarm bootstrap (used by install.sh too), dev HTTPS
docs/         install, development, the CLI contract; design docs: canvas, workers, networking, volumes, logs, agents
install.sh    the installer
```

The CLI is `internal/cli` (contract in [`docs/cli.md`](cli.md)).

[`ci.yml`](../.github/workflows/ci.yml) runs the Go checks and the web typecheck and build, then runs `install.sh` end to end on a fresh runner, twice. It runs on every pull request, and as the first job of [`images.yml`](../.github/workflows/images.yml), which builds the image on every push to `main` (`:latest`, `:sha-<short>`) and on `v*` tags and publishes it only when `ci.yml` passed.
