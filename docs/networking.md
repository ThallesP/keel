# Networking — Tailscale mesh + keel-proxy on the control plane

> How servers reach each other and how the public reaches a service. Mesh decided 2026-09-13 (Tailscale). **Public ingress revised 2026-10-06: the user opens ports on the control plane and `keel-proxy` (Caddy + caddy-l4) serves every exposed service from there.** It replaces the Cloudflare Quick Tunnel per service (2026-10-01 → 2026-10-06), which replaced Tailscale Funnel; see "History". Read this before touching node join, ingress, domains or auth. Worker scheduling lives in [`workers.md`](./workers.md).

## Invariant

- **Private by default, one door when exposed.** Convex, the dashboard and Swarm bind the tailnet address only. The only public listeners on any machine are the ones `keel-proxy` opens on the control plane for what someone exposed: 80/443 for HTTPS, plus each exposed TCP/UDP port. Workers never listen publicly; there is no per-worker proxy.
- **The user's networking job is exactly this:** let 80, 443 and the exposed TCP/UDP ports reach the control plane (cloud firewall, `ufw`, or a router port-forward on a homelab). Keel says which ports, on which IP, in the install output and in each service's Settings → Public networking. Certificates, routing, proxying and the path to the service (overlay over the tailnet) are Keel's.

This is a step back from "no user ever opens an inbound port". The tunnel products that keep that promise each failed a requirement (History); a better zero-port path may replace the front of this later without changing anything per service.

## Decision

- **Mesh:** Tailscale. Host `tailscaled` on every server. Swarm control traffic (2377, 7946, 4789) and the `keel` overlay ride the tailnet, see `workers.md`. The per-node event forwarder makes one outbound HTTP call per Docker event to the control plane's tailnet address and listens on nothing.
- **Public ingress:** `keel-proxy`, one container on the control plane (`deploy/compose.yml`). HTTPS by hostname on 80/443 with automatic certificates; raw TCP on any other port, raw UDP on any port (80 and 443 included: the HTTPS server holds only their TCP side). It dials `svc-<id>:<port>` over the overlay, so a service on any server, and every replica, is reachable.
- **Proxy:** Caddy 2.11 + [caddy-l4](https://github.com/mholt/caddy-l4) + our plugin (`apps/proxy`). Convex owns its whole configuration and pushes it through the admin API.
- **Default domain:** `https://<service>-<hash6>.<public-ip-dashed>.sslip.io` (sslip.io resolves to the IP inside the name; `hash6` = FNV of the node id, so two projects' `api` never collide and renames keep the URL). When the public IP changes, `migrations.run` moves default domains to the new one, keeping the name. Custom domains: any name with an A record to the control plane's public IP.
- **Auth:** ours (better-auth). Tailscale identity is optional sign-in sugar later, never the account system.

## Why Caddy + caddy-l4

Requirements: everything through an API (adding a TCP or UDP port included, no config file, no restart), automatic HTTPS, raw TCP and UDP, small footprint (control planes are often the smallest box).

| Option                       | Verdict                                                                                                                                                                                                                                                                                                                                                                                                       |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Caddy + caddy-l4** (chose) | One Go binary. Admin REST API for the whole config; a load is atomic and graceful (listeners are reused across reloads, open connections survive: a Postgres session through the proxy kept its backend pid across a reload that added a port). ACME built in (Let's Encrypt; ZeroSSL fallback once an email is set). caddy-l4 brings TCP and UDP servers configured by the same API. ~12–17 MB RSS measured. |
| Traefik                      | TCP/UDP entrypoints are static config: a new port means a restart. That is Pangolin's limitation (Pangolin runs Traefik). HTTP provider is poll-only.                                                                                                                                                                                                                                                         |
| HAProxy + Data Plane API     | The API rewrites the config file and reloads; UDP load balancing is Enterprise-only.                                                                                                                                                                                                                                                                                                                          |
| Envoy                        | Fully dynamic listeners over xDS, but needs a gRPC control plane, heavy for one box. Railway left it over 45 s config rollouts.                                                                                                                                                                                                                                                                               |
| nginx                        | No runtime API outside NGINX Plus.                                                                                                                                                                                                                                                                                                                                                                            |
| Sōzu                         | Hot reconfiguration, but no ACME and no UDP.                                                                                                                                                                                                                                                                                                                                                                  |
| Own proxy                    | `certmagic` + `ReverseProxy` + a UDP relay is buildable, and is what Caddy already is.                                                                                                                                                                                                                                                                                                                        |

## How keel-proxy listens: overlay inside, host namespace outside

A container's published ports are fixed at create time, so a proxy behind `ports:` needs a restart for every new TCP/UDP port. A proxy with `network_mode: host` can open any port but cannot reach the overlay (`svc-<id>` names and VIPs live in the overlay's namespace). keel-proxy does both:

- The container is on the `keel` overlay (attachable, so a Compose container can join). It resolves and dials `svc-<id>:<port>` like any service.
- Its public sockets are created in the **host's** network namespace. The plugin registers Caddy networks `host-tcp` and `host-udp` (`caddy.RegisterNetwork`, the same hook caddy-tailscale uses): the listener function locks an OS thread, `setns` into the host namespace (`/proc/1/ns/net` bind-mounted at `/run/hostns/net`), opens the socket through Caddy's own `tcp`/`udp` path (so SO_REUSEPORT and the listener pool still apply), and switches back. A socket keeps the namespace it was created in. Needs `CAP_SYS_ADMIN`. It is the one container that parses internet traffic, so it drops every other capability but `NET_BIND_SERVICE` (80/443 in the host namespace) and runs with `no-new-privileges`.
- **Never a wildcard bind.** `tailscale serve` (the dashboard over HTTPS) holds 443 on the tailnet address, and `0.0.0.0:443` cannot bind next to it. The plugin's `GET /keel/host-addrs` lists the host's addresses minus loopback, link-local, Tailscale (100.64.0.0/10, fd7a:115c:a1e0::/48 and `tailscale*`) and Docker interfaces; Convex writes one listen address per entry. An address change (DHCP) takes effect on the next sync.
- **The admin API is a unix socket** in a volume shared with the backend container only (`proxy-admin`, mode 0600). Nothing on the overlay can reach it.
- Upstreams resolve at connection time through Docker's DNS, so redeploys, rescheduling and replica changes never touch the proxy. A stopped service answers 502 (HTTP) or closes the connection (TCP).

`caddy run --resume`: after a restart the proxy serves the last config Convex pushed (autosaved in the `proxy-config` volume). Certificates and ACME accounts live in `proxy-data`.

## Endpoints

`nodes.endpoints` (`convex/schema.ts`), one entry per way in:

| Field        | http                                       | tcp / udp                                      |
| ------------ | ------------------------------------------ | ---------------------------------------------- |
| `port`       | container port the proxy dials             | same                                           |
| `domain`     | hostname, unique per install               | –                                              |
| `publicPort` | – (always 80/443)                          | port on the control plane, unique per protocol |
| `status`     | `starting` → `live` / `failed` (+ `error`) | same                                           |

- **Expose** (`nodes.expose`) is immediate, not Ship-gated, like the Quick Tunnel was. No options: https on the default domain for a service, tcp on its own port for a database or cache. `publicPort` defaults to the container port when it is free on that protocol, else the first free one from 20000. TCP cannot take 80/443. At most 10 endpoints per node.
- **Unexpose** closes one endpoint (domain, or protocol + public port) or all ("Make private"). Deleting a node closes its endpoints.
- **The canvas:** toolbar Expose / ⋯ Make private; Settings → Public networking lists endpoints (address with copy, target port, state, ✕) and adds https domains or tcp/udp ports; the card subtitle is the best https domain (live > starting > failed); the Deployments meta strip lists every address.
- `KEEL_PUBLIC_IP` (Convex env, detected by `install.sh`) names default domains and tcp/udp addresses. Without it, Expose asks for it.

## Sync and status

`proxy.sync` (`convex/proxy.ts`, internal, scheduled after every change and by `migrations.run` on every install) builds the whole `apps` config from every endpoint and `POST`s it to `/config/apps`:

- `http`: one server, `host-tcp/<addr>:443` per host address, a host-matched `reverse_proxy` route per domain. Automatic HTTPS adds the `:80` server on the same network and addresses (HTTP→HTTPS redirect, ACME HTTP-01). HTTP/1.1 and HTTP/2 only; HTTP/3 needs a UDP 443 host listener, not yet.
- `layer4`: one server per tcp/udp endpoint, `host-<proto>/<addr>:<publicPort>` → `proxy` to `[udp/]svc-<id>:<port>`.
- `tls`: only when `KEEL_ACME_CA` / `KEEL_ACME_EMAIL` are set. `KEEL_ACME_CA` (dev and CI: Let's Encrypt staging) is the only issuer; `KEEL_ACME_EMAIL` (`install.sh`) gives Let's Encrypt then ZeroSSL, the pair Caddy builds itself when it has an email. With neither, there is no `tls` app and Caddy uses Let's Encrypt alone. `*.localhost` names get Caddy's internal CA, which is how development tests HTTPS end to end.
- `events`: the plugin's `keel` handler subscribed to `cert_obtained` / `cert_failed`.

A load is all or nothing, so a port someone else holds would block every endpoint. Caddy names the listener that failed (`listen tcp 192.0.2.1:5432: bind: address already in use`); `sync` marks that endpoint `failed` ("Port 5432/tcp is already in use on the control plane"), drops it and loads again (80/443 failures belong to every https endpoint). An error no endpoint owns fails them all and the proxy keeps its previous config. Two overlapping syncs converge: each re-reads after loading and goes again if the endpoints moved.

A cron (`proxyInternal.resync`, every 2 minutes while anything is exposed) syncs again, so what failed for a passing reason (the proxy restarting, a port freed) recovers by itself and a changed host address is picked up. Caddy answers an unchanged config without reloading, and a status that did not change is not written, so the cron costs nothing while all is well.

An endpoint dials the node's port unless Expose was given another one (`pinnedPort`): when a port change ships, `swarm.apply` calls `nodesInternal.followPort` and the endpoint moves with it.

Status after a load: tcp/udp `live`; https `live` when `GET /keel/certs` reports a valid certificate, `failed` when it reports the last obtain error, else `starting`. From then on **certificates report themselves**: the `keel` event handler POSTs `/proxy/events` (worker bearer token, dialled from the host namespace) and `proxyInternal.certReport` flips the endpoint. No polling. Errors are rewritten into the next step (`endpoints.certHint`): a `connection` problem says to open 80 and 443, a `dns` one says which IP the A record must point at. Caddy keeps retrying with backoff, so opening the port later turns the endpoint live by itself. An attempt cancelled by a config reload is not reported.

## Upgrades from the Quick Tunnel

`migrations.run` (run by `deploy/functions-entrypoint.sh` after every deploy): a node with the old `public`/`ingress` fields gets an https endpoint on its default domain (services with a port, when `KEEL_PUBLIC_IP` is known) and both fields are cleared; `swarm.removeLegacyTunnels` removes every Swarm service labelled `keel.ingress`. The two fields stay in the schema as `v.any()` until every install has run it.

## Limits

- **The user opens ports.** Behind CGNAT with no port-forward, nothing public works. Homelabs need router forwards for 80, 443 and each TCP/UDP port.
- **sslip.io and Let's Encrypt.** sslip.io is not on the Public Suffix List, so every sslip.io user shares one (raised) Let's Encrypt quota; it has run out before. With `KEEL_ACME_EMAIL` set, Caddy falls back to ZeroSSL; without it, default domains wait until the quota frees up. A custom domain gets its own quota and is the production answer.
- One public IPv4 per install (`KEEL_PUBLIC_IP`); the proxy also binds global IPv6 addresses it finds, but default domains are IPv4.
- The control plane carries all public traffic. Fine for a small cluster; a second proxy on a worker is the scaling path, not built.
- Raw TCP/UDP endpoints are unauthenticated beyond what the service does. Every database and cache Keel creates gets a generated password (Redis included: `REDIS_PASSWORD`, run as `--requirepass`; `migrations.run` backfills older Redis nodes and marks them and their referrers dirty, so they ship together; until that Ship, Expose refuses the Redis).

## History

### Tailscale Funnel (rejected 2026-10-01)

Funnel publishes exactly the node's own MagicDNS name on 443/8443/10000, cannot use custom domains ([#11563](https://github.com/tailscale/tailscale/issues/11563)) and does not support Tailscale Services ([#17849](https://github.com/tailscale/tailscale/issues/17849)). Per-service hostnames meant a userspace tsnet node per service (tens of MB, a tailnet device and an auth key each); hosting on the host `tailscaled` meant one hostname per server, and `tailscale serve` only proxies to loopback. Bandwidth is capped and undisclosed.

### Cloudflare Quick Tunnel (2026-10-01 → 2026-10-06)

One `cloudflared tunnel --url http://svc-<id>:<port>` Swarm service per exposed service, URL read from its logs. Zero setup, but: the `trycloudflare.com` URL changed on every tunnel restart, 200 in-flight requests then 429, no SSE, no UDP, no uptime guarantee ("testing and development"), and Cloudflare's video/large-file terms. The planned named tunnel (API token, stable hostnames on the user's Cloudflare zone) fixed the URL but kept TLS at Cloudflare's edge, the 100 MB body cap and no raw TCP/UDP. Dropped for keel-proxy. A zero-inbound-port front (a named tunnel, or our own relay) can come back later as something that forwards to keel-proxy's 80/443, with no per-service provider: the `nodes.public.provider` switch it had is gone.

### Mesh alternatives (2026-09-13)

| Option                       | Why not                                                                                                                                                                      |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| tailcat                      | No control plane or identity, address is a bearer secret, userspace pipe containers can't route over, no stability promises. Maybe later for `keel connect` dev pipes.       |
| Cloudflare Mesh              | No peer-to-peer, every packet through a Cloudflare PoP (NetBird's benchmark: 250 vs 1,300 Mbps Hetzner→Hetzner), 50 nodes then Enterprise, decrypts at the edge.             |
| NetBird self-hosted          | Strongest runner-up (BSD-3 client, AGPL server, ingress proxy, no node cap). Four more containers on the control plane and it opens UDP 3478. Tailscale's NAT traversal won. |
| Pangolin                     | Client-to-site only, no site-to-site mesh; Traefik underneath (static TCP/UDP entrypoints); AGPL and commercial license mixed per file.                                      |
| Headscale                    | Single tailnet, two maintainers, no ingress. Kept as an escape hatch via `ControlURL`.                                                                                       |
| Nebula / ZeroTier / OpenZiti | No ingress (Nebula), BSL with a SaaS-controller trigger (ZeroTier), a PKI + controller + router fleet to run (OpenZiti).                                                     |

## Dashboard over HTTPS

The dashboard is tailnet-only and needs HTTPS: secure-context APIs (`crypto.subtle`, clipboard) are off on plain-http origins like a tailnet IP. Host `tailscaled` does it with `tailscale serve` on the node's own MagicDNS name, no extra device:

| URL                                     | Target                             |
| --------------------------------------- | ---------------------------------- |
| `https://<node>.<tailnet>.ts.net`       | web                                |
| `https://<node>.<tailnet>.ts.net:8443`  | Convex API + sync websocket (3210) |
| `https://<node>.<tailnet>.ts.net:10000` | Convex HTTP actions, auth (3211)   |

Convex must be https too or the browser blocks it as mixed content. These listeners sit on the tailnet address, which keel-proxy never binds, so both coexist on 443. Workers keep talking to the tailnet IP over plain http; the tailnet is already encrypted.

Dev does this with `scripts/dev-https.sh`. `install.sh` still serves `http://<tailnet IP>`; moving it over means `KEEL_CONVEX_URL`, `KEEL_CONVEX_SITE_URL` and `SITE_URL` follow the MagicDNS name. keel-proxy could serve the dashboard instead (Caddy gets `*.ts.net` certificates from `tailscaled`), not done.

## "Sign in with Tailscale" (later, optional)

Not an identity provider. Tailscale OAuth apps are alpha and same-tailnet only; useless for outside users. What works: serve the dashboard on a tsnet listener too, call `LocalClient().WhoIs(remoteAddr)` on the request, get the user's login, mint a better-auth session, redirect to the public dashboard URL. Only reachable from inside the tailnet, which is the point. `tsidp` does the same via OIDC if we want a standard flow.

## Not doing yet

- HTTP/3 (needs a `host-udp` 443 listener for QUIC).
- Port ranges, and per-endpoint access rules (IP allowlists, basic auth). caddy-l4 and Caddy have the matchers; nothing exposes them.
- A public dashboard.
- Sign in with Tailscale.

## Sources

- Caddy: [admin API](https://caddyserver.com/docs/api), [`RegisterNetwork`](https://github.com/caddyserver/caddy/blob/master/listeners.go), [automatic HTTPS](https://caddyserver.com/docs/automatic-https), [events](https://caddyserver.com/docs/json/apps/events/); [caddy-l4](https://github.com/mholt/caddy-l4); [caddy-tailscale](https://github.com/tailscale/caddy-tailscale) (the custom-network precedent)
- sslip.io: [site](https://sslip.io), [Let's Encrypt quota exhausted #108](https://github.com/cunnie/sslip.io/issues/108); [Let's Encrypt rate limits](https://letsencrypt.org/docs/rate-limits/)
- Rejected proxies: [Traefik HTTP provider](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/http/), [Pangolin architecture](https://docs.pangolin.net/development/system-architecture), [Railway edge proxy changelog](https://railway.com/changelog/2024-05-17-new-edge-proxy-beta)
- Tailscale: [Funnel](https://tailscale.com/docs/features/tailscale-funnel), [tsnet](https://pkg.go.dev/tailscale.com/tsnet), [pricing](https://tailscale.com/pricing)
- Cloudflare: [Quick Tunnels](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/), [remote tunnel API](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel-api/), [video delivery terms](https://developers.cloudflare.com/fundamentals/reference/policies-compliances/delivering-videos-with-cloudflare/)
- Mesh: [tailcat](https://tailscale.com/blog/tailcat), [Cloudflare Mesh](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-mesh/), [NetBird benchmark](https://netbird.io/knowledge-hub/cloudflare-mesh-vs-netbird-vs-tailscale), [NetBird self-host](https://docs.netbird.io/selfhosted/selfhosted-quickstart)
