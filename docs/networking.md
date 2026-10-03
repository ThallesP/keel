# Networking — Tailscale mesh + per-service Funnel ingress

> How servers reach each other and how the public reaches a service. Mesh decided 2026-09-13 after evaluating Tailscale, tailcat, Cloudflare Mesh/Tunnel, NetBird, Pangolin, Headscale, Nebula, ZeroTier and OpenZiti. **Public ingress revised 2026-10-01: Cloudflare Tunnel, not Tailscale Funnel** (see "Public ingress"). Read this before touching node join, ingress, domains or auth. Worker scheduling lives in [`workers.md`](./workers.md). This file supersedes the "Networking notes" section there.

## Invariant

**No user ever opens an inbound port. Not 443, not 22, not anything.** Every path is an outbound tunnel. Any design that needs "open port X on your server" is wrong for this product. Homelab behind CGNAT must work identically to a Hetzner VPS.

## Decision

- **Mesh:** Tailscale. Host `tailscaled` on every server. Swarm control traffic (2377, 7946, 4789) rides the tailnet, see `workers.md`. The per-node event forwarder (`keel-events`) makes one outbound HTTP call per Docker event to the control plane's tailnet address and listens on nothing.
- **Public HTTP:** Cloudflare Tunnel. Today a **Quick Tunnel** per exposed service (`cloudflared tunnel --url http://svc-<id>:<port>`, no account, temporary `https://<random>.trycloudflare.com`). Next: one **named tunnel** per cluster with stable hostnames on the user's own domain. The control plane is never in the request path. Details under "Public ingress".
- **Proxy:** none of ours. `cloudflared` dials the app's Swarm VIP over the overlay.
- **Auth:** ours (better-auth). Tailscale identity is optional sign-in sugar later, never the account system.
- **Custom domains:** the named-tunnel slice. Any zone on the user's Cloudflare account.

## Why not the alternatives

| Option                                | Why not                                                                                                                                                                                                                                                                                                                                                            |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **tailcat**                           | No control plane, no identity, address is a bearer secret ("treat it like a password"). Userspace pipe, containers can't route over it. README: no API/CLI/wire stability promises, public relays revocable any time. Maybe later for `keel connect` dev pipes.                                                                                                    |
| **Cloudflare Mesh**                   | No peer-to-peer at all, every packet hairpins through a Cloudflare PoP. NetBird's benchmark: Hetzner→Hetzner 250 Mbps vs 1,300 on Tailscale. 50 nodes then Enterprise sales. Cloudflare decrypts at the edge.                                                                                                                                                      |
| **Cloudflare Tunnel** (as the mesh)   | Adopted for public ingress on 2026-10-01, see below. Not the mesh: that stays Tailscale. Video/large-file ToS restriction on public hostnames.                                                                                                                                                                                                                     |
| **Tailscale Funnel** (public ingress) | One hostname per Tailscale node, `ts.net` names only ([#11563](https://github.com/tailscale/tailscale/issues/11563) open since 2024), no Funnel for Tailscale Services ([#17849](https://github.com/tailscale/tailscale/issues/17849)), so every public service is its own userspace WireGuard node needing a key or a browser authorization. Rejected 2026-10-01. |
| **NetBird self-hosted**               | Strongest runner-up: BSD-3 client + AGPL server, embed SDK, ingress proxy with custom domains, no node cap. Costs us four containers on the control plane and the control plane must open UDP 3478. User picked Tailscale's NAT traversal and simpler install.                                                                                                     |
| **Pangolin**                          | Client-to-site only. No site-to-site mesh. AGPL and commercial license mixed per file.                                                                                                                                                                                                                                                                             |
| **Headscale**                         | Single tailnet, two maintainers, no ingress. Kept as escape hatch via `ControlURL`.                                                                                                                                                                                                                                                                                |
| **Nebula / ZeroTier / OpenZiti**      | No ingress (Nebula, $1/host past 100), BSL with SaaS-controller trigger (ZeroTier), you run a PKI + controller + router fleet (OpenZiti).                                                                                                                                                                                                                          |

Proxy choice:

| Option        | Why not                                                                                                                                                                                                                                                                                                                |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Caddy**     | Best REST admin API of the shelf (push, granular, ETag). But `caddy-tailscale` is "highly experimental" and has no Funnel ([#26](https://github.com/tailscale/caddy-tailscale/issues/26) open since Dec 2023). Would mean embedding Caddy as a library with our own listener. Revisit if we want its plugin ecosystem. |
| **Traefik**   | v3 HTTP provider is poll-only (default 5s), no push endpoint. Docker-label discovery we don't need.                                                                                                                                                                                                                    |
| **Envoy**     | xDS gRPC, heavy. Railway left it because rolling config diffs took 45s at scale. Wrong tool for one process per service.                                                                                                                                                                                               |
| **Own proxy** | Certs, ingress, TLS and NAT are all Tailscale's problem. What's left is "forward HTTP to one target". Go's `ReverseProxy` does websockets and h2c. Config is a struct in memory.                                                                                                                                       |

## Public ingress (2026-10-01)

Verified before deciding: Funnel publishes exactly the node's own MagicDNS name on 443/8443/10000, cannot use custom domains, and does not support Tailscale Services. Per-service hostnames therefore mean per-service tsnet nodes (tens of MB each, a tailnet device each, an auth key or a browser authorization each). Hosting on the host `tailscaled` instead gives one hostname per _server_ with path or port routing, and `tailscale serve` only proxies to loopback. Thalles rejected both. Cloudflare Tunnel is "one connector, many hostnames", which is Railway's model.

### Quick Tunnel (shipped)

- **Expose** on a service is one click and immediate, not Ship-gated: `nodes.expose` sets `nodes.public = { provider: "quick-tunnel" }` and schedules `swarm.applyIngress`, which creates Swarm service `ingress-<id>` (label `keel.ingress=<id>`, image `cloudflare/cloudflared:<pinned>`, args `tunnel --url http://svc-<id>:<port>`, 1 replica, `stop-first`, on the `keel` overlay, no ports, no mounts). Nothing is pasted or stored. A shipped port change re-runs `applyIngress`.
- **URL discovery.** `cloudflared` prints the assigned hostname 2–5 s after start. `swarm.observeNode` lists `keel.ingress=<id>` tasks next to the app's, reads `docker service logs ingress-<id>` on the manager (timestamp-sorted, last `*.trycloudflare.com` match wins, so a restart's new URL replaces the old one) and writes `nodes.ingress = { state: starting | live | failed, url?, error? }`. While `starting` it re-checks every 3 s, at most 10 times. The worker forwards `ingress-*` Docker events like `svc-*` ones, so a restart is observed without polling.
- **Canvas.** Card subtitle is the domain while live (link), "Exposing…" / "Expose failed" otherwise; the Deployments meta strip has the link and a copy button. Both say the URL is temporary. ⋯ → Make private removes the tunnel; deleting the node removes both services.
- **Limits, stated to users:** the URL changes whenever that `cloudflared` restarts (node reboot, reschedule, Make private then Expose). Redeploying the app does not change it (target is the Swarm VIP). 200 in-flight requests then 429. No Server-Sent Events. No uptime guarantee; Cloudflare: "testing and development". Anyone with the URL can reach the service, and `trycloudflare.com` hosts get indexed. Outbound only: UDP 7844 (QUIC) with HTTP/2 over TCP 7844 fallback. One `cloudflared` per exposed service, ~25 MB image, tens of MB RSS.
- Stopping a service leaves its tunnel up (answers 502 until Start). Databases and raw TCP are never exposed.

### Named tunnel (next)

Removes every limit above and needs no per-service process. One-time: the user pastes a Cloudflare API token (Account → Cloudflare Tunnel: Edit, Zone → DNS: Edit) and picks a zone; the domain must be on Cloudflare DNS (free plan is fine).

- `POST /accounts/{id}/cfd_tunnel` `{"name":"keel-<cluster>","config_src":"cloudflare"}` → tunnel id + run token. One Swarm service `keel-tunnel` on the overlay, `cloudflared tunnel run --token …`, 2 replicas for HA (connectors of one tunnel).
- Expose = `PUT /accounts/{id}/cfd_tunnel/{tid}/configurations` with the full ingress list (`<service>.<zone> → http://svc-<id>:<port>` per public node, catch-all `http_status:404` last) plus `POST /zones/{zid}/dns_records` CNAME `<service>.<zone> → <tid>.cfargotunnel.com`, proxied. Remote config is picked up live; no restart.
- Health: `GET …/cfd_tunnel/{tid}` → `status`, `connections`.
- `nodes.public.provider = "cloudflare"` with the hostname; `nodes.ingress` keeps the same shape.
- Caveats: TLS terminates at Cloudflare's edge, 100 MB request body on Free/Pro, video/large-file terms on non-Enterprise, Cloudflare's uptime becomes yours.

Later, if a hosted Keel ever exists, `*.keel.sh`-style generated domains would be a named tunnel in Keel's own account; not possible for self-hosted installs.

---

> **Everything from here to "Naming" is the per-service tsnet Funnel design rejected on 2026-10-01.** Kept for reference. Nothing in code implements it.

## Traffic path

```
browser ──HTTPS──▶ Tailscale Funnel ingress (Tailscale-run, anycast)
                        │  raw TCP, relayed, still TLS-encrypted end to end
                        ▼
              ingress-<svc> container (tsnet node "<name>", any Swarm node)
                        │  TLS terminated here with the Let's Encrypt cert tailscaled fetched
                        │  httputil.ReverseProxy
                        ▼
              http://svc-<id>:<port>   (Swarm overlay VIP, load-balanced across tasks)
```

Funnel traffic is always relayed through Tailscale's ingress servers; that's how Funnel works and why no port opens on our side. TLS terminates inside our container, Tailscale sees ciphertext. Funnel only listens on 443, 8443 and 10000. We use 443 only.

Blue/green and rolling deploys never touch ingress: the target is the Swarm service VIP, Swarm swaps tasks underneath with `start-first`. Ingress changes only when the service is renamed, its port changes, or it is made private.

## The ingress container

One Go binary, distroless image, ~15 MB. Runs as an ordinary Swarm service `ingress-<id>` on the `keel` overlay. No host network, no `NET_ADMIN`, no socket mount. tsnet is userspace (gVisor netstack), so it needs nothing from the host.

```go
// cmd/ingress/main.go
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"tailscale.com/tsnet"
)

func main() {
	target, err := url.Parse(os.Getenv("OS_TARGET")) // http://svc-abc123:3000
	if err != nil {
		log.Fatal(err)
	}

	s := &tsnet.Server{
		Hostname:      os.Getenv("OS_HOSTNAME"), // "myapp" -> myapp.<tailnet>.ts.net
		Dir:           "/state",                 // named volume; without it every restart is a new device
		AuthKey:       os.Getenv("TS_AUTHKEY"),  // ignored once /state has a node key
		AdvertiseTags: []string{"tag:keel-ingress"},
		Logf:          func(string, ...any) {},  // quiet; UserLogf still prints auth URLs
	}
	defer s.Close()

	// Errors if HTTPS certs are off in the tailnet or the tag lacks the funnel attr.
	ln, err := s.ListenFunnel("tcp", ":443")
	if err != nil {
		log.Fatal(err)
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded() // X-Forwarded-For/Host/Proto; client IP is the Tailscale ingress
			r.Out.Host = r.In.Host
		},
	}
	log.Fatal(http.Serve(ln, rp))
}
```

Facts verified in `tailscale.com/tsnet` source (2026-09):

- `ListenFunnel` fetches the existing `ServeConfig` and merges one `AllowFunnel` key. Multiple Funnel ports on one node work; [#8800](https://github.com/tailscale/tailscale/issues/8800) is closed.
- Hostname for the cert is `CertDomains()[0]`, i.e. the node's own MagicDNS name. You cannot listen for a different host. One public hostname == one tsnet node.
- `AuthKey` is ignored when `Dir` already holds state. First `Up` in a process wipes persisted serve config, which is fine because `ListenFunnel` re-applies it.
- `ListenFunnel` default serves both tailnet and public. Keep that; users can hit the private side too.
- Do **not** set `Ephemeral: true`. Ephemeral minutes are pooled (1,000/month on Personal and Standard) and an ephemeral node present over 4 hours converts to a regular tagged node anyway. Persist state, count it as a tagged node.

## Convex side

Extends the schema in `workers.md`. `desired.public` is the switch; `observed.url` is what the canvas shows.

```ts
// services.desired gains:
public: v.optional(v.object({
  hostname: v.string(), // tailnet-unique slug, default = service name
  port: v.number(),     // container port to forward to
})),

// services.observed gains:
url: v.optional(v.string()), // https://<hostname>.<tailnet>.ts.net once the ingress task is running
```

`apply` in `convex/swarm.ts` grows one branch. Same idempotent create-or-update, second Swarm service:

```ts
function toIngressSpec(s: { id: string; hostname: string; port: number; authKey: string }) {
  return {
    Name: `ingress-${s.id}`,
    Labels: { "keel.ingress": s.id },
    TaskTemplate: {
      ContainerSpec: {
        Image: `${REGISTRY}/keel-ingress:${INGRESS_VERSION}`,
        Env: [
          `OS_HOSTNAME=${s.hostname}`,
          `OS_TARGET=http://svc-${s.id}:${s.port}`,
          `TS_AUTHKEY=${s.authKey}`,
        ],
        Mounts: [{ Type: "volume", Source: `ingress-${s.id}`, Target: "/state" }],
      },
      RestartPolicy: { Condition: "any", Delay: 5_000_000_000 },
      Networks: [{ Target: "keel" }],
    },
    Mode: { Replicated: { Replicas: 1 } },
  };
}
```

- Setting `desired.public` schedules `apply` for both `svc-<id>` and `ingress-<id>`. Clearing it removes `ingress-<id>` and schedules device cleanup.
- `observe` (existing cron) also lists tasks with label `keel.ingress`. Running task → `observed.url = https://<hostname>.<tailnet>.ts.net`. Failed task with an auth error in `Status.Err` → mint a new auth key, update the service env.
- Ingress is unpinned. If Swarm reschedules it to another server the local volume is gone, the container registers as a **new** device with the same hostname, and the old device lingers. `observe` notices `nodeIds` changed for an ingress task and schedules `DELETE /api/v2/device/{id}` for any device with that hostname that is not the current one. Rare; reschedules happen on node death only.
- Auth key: one reusable, pre-authorized, `tag:keel-ingress` key per tailnet, 90-day expiry, minted with the user's OAuth client and stored in Convex. Cron rotates it at 80 days. Keys never leave Convex except into ingress container env on the user's own machines.

## Tailnet onboarding

Done once when the user connects a tailnet. All via the Tailscale API with the OAuth client the user pastes in. Fail the onboarding loudly if any step fails; a missing step shows up later as an opaque `ListenFunnel` error.

1. **MagicDNS on.** `POST /api/v2/tailnet/{tailnet}/dns/preferences` `{"magicDNS": true}`. Default-on for tailnets created after Oct 2022, still check.
2. **HTTPS certs on.** Tailnet settings endpoint (`/api/v2/tailnet/{tailnet}/settings`, field `httpsEnabled`). Funnel refuses to start without it. Note to user: device hostnames land in Certificate Transparency logs.
3. **Policy file.** Merge into the existing ACL, never overwrite:
   ```json
   {
     "tagOwners": { "tag:keel-node": ["autogroup:admin"], "tag:keel-ingress": ["autogroup:admin"] },
     "nodeAttrs": [{ "target": ["tag:keel-ingress"], "attr": ["funnel"] }]
   }
   ```
   The OAuth client must be allowed to own those tags. Personal plan allows 3 ACL groups; tags are not groups, fine.
4. **Mint keys.** Host `tailscaled` on servers gets a `tag:keel-node` key (join script, see `workers.md`). Ingress gets the `tag:keel-ingress` key above.

OAuth client scopes needed: auth keys (write), devices (write, for cleanup), DNS (write), policy file (write), tailnet settings (write). Exact scope identifiers are on `https://tailscale.com/api`; verify there rather than from memory. Ask for the minimum; show the list on the onboarding screen.

## Naming

- MagicDNS names are unique per **tailnet**, not per project. Two projects with a service named `api` collide. Default `hostname` = service name; on collision default to `<service>-<project>`. Check against `GET /api/v2/tailnet/{tailnet}/devices` before creating.
- Renaming a public service = new hostname = new device + new Let's Encrypt cert. LE limits duplicate certs per name; rename loops can trip a ~34h wait. Warn in the UI, don't block.
- Hostname rules: lowercase, `[a-z0-9-]`, no leading/trailing dash, ≤63 chars.

## Gotchas

- **Tagged-node budget.** Every server (`tailscaled`) and every public service (ingress tsnet) is a tagged node. 50 included on every plan, then $1/month each. Show "N of 50 Tailscale nodes" in settings. A homelab with 3 servers and 10 public apps is 13.
- **Funnel bandwidth** is capped and the number is undisclosed. Fine for dashboards, demos and small apps. Say so in the docs; do not promise "production".
- **Funnel ports** are 443, 8443, 10000 only. We use 443. Never expose raw TCP (databases) via Funnel.
- **Memory.** Each tsnet node is its own WireGuard + netstack, on the order of tens of MB RSS. Ten public services on a Raspberry Pi is noticeable. Measure and surface in the node card.
- **Two Tailscale identities per server minimum** (host `tailscaled` + ingress containers). That's expected, not a bug.
- **MTU.** Overlay is 1200 (see `workers.md`). The ingress container talks to the app over the overlay, so nothing new here. Funnel side is Tailscale's problem.
- **Personal plan is non-commercial.** Indie hackers hosting side projects are fine. Tell people running a business to move to Standard ($8/seat, same 50 tagged nodes).
- **ToS.** Tailscale ToS bans reselling the service. Self-hosted Keel with the user's own tailnet is fine. A hosted Keel that bundles Tailscale needs an OEM deal with Tailscale sales. Don't ship hosted on this design without that conversation.

## "Sign in with Tailscale" (later, optional)

Not an identity provider. Tailscale OAuth apps are alpha and same-tailnet only; useless for outside users. What works: serve the dashboard on a tsnet listener too, call `LocalClient().WhoIs(remoteAddr)` on the request, get the user's login, mint a better-auth session, redirect to the public dashboard URL. Only reachable from inside the tailnet, which is the point. `tsidp` does the same via OIDC if we want a standard flow.

## Custom domains (superseded by "Named tunnel" above)

When someone needs `app.example.com`, the zero-inbound-port path is Cloudflare Tunnel:

- Domain on Cloudflare DNS. User pastes an API token (Cloudflare Tunnel Edit + DNS Edit).
- `POST /accounts/{id}/cfd_tunnel` `{"config_src":"cloudflare"}` → tunnel id + token.
- `PUT /accounts/{id}/cfd_tunnel/{tid}/configurations` with an ingress rule `app.example.com → http://svc-<id>:<port>`.
- `POST /zones/{zid}/dns_records` CNAME `app.example.com → <tid>.cfargotunnel.com`.
- Run `cloudflared` as a Swarm service on the overlay, same pattern as the ingress container, `--token`.

Caveats: TLS terminates at Cloudflare's edge; video/large-file hosting on public hostnames is restricted by Cloudflare's CDN terms on non-Enterprise plans; Cloudflare's uptime becomes yours. Alternative for a server with a public IP: run Caddy on that box with ACME. That one opens 80/443, so it's opt-in per server and labeled as such.

## Not doing in v1

- Custom domains (next slice: named Cloudflare tunnel).
- Raw TCP/UDP public exposure.
- Per-node agent. The ingress container is a Swarm service like everything else (`workers.md`: no agent).
- Cloudflare as a mesh backend. The tailnet stays the mesh.
- Sign in with Tailscale.

## Sources

- tsnet: [package docs](https://pkg.go.dev/tailscale.com/tsnet), [`tsnet.go` source](https://github.com/tailscale/tailscale/blob/main/tsnet/tsnet.go), [#8800 multiple Funnel listeners](https://github.com/tailscale/tailscale/issues/8800)
- Funnel: [docs](https://tailscale.com/docs/features/tailscale-funnel), [custom domain FR #11563](https://github.com/tailscale/tailscale/issues/11563), [enabling HTTPS](https://tailscale.com/docs/how-to/set-up-https-certificates)
- Tailscale API and plans: [pricing](https://tailscale.com/pricing), [terms](https://tailscale.com/terms), [OAuth clients](https://tailscale.com/docs/features/oauth-clients), [OAuth apps (alpha)](https://tailscale.com/docs/features/oauth-apps), [Tailnets API (alpha)](https://tailscale.com/docs/features/tailnets-api), [tsidp](https://tailscale.com/blog/building-tsidp)
- Rejected: [tailcat blog](https://tailscale.com/blog/tailcat), [tailcat repo](https://github.com/tailscale/tailcat), [Cloudflare Mesh](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-mesh/), [NetBird benchmark](https://netbird.io/knowledge-hub/cloudflare-mesh-vs-netbird-vs-tailscale), [NetBird self-host](https://docs.netbird.io/selfhosted/selfhosted-quickstart), [NetBird reverse proxy](https://docs.netbird.io/manage/reverse-proxy), [Pangolin architecture](https://docs.pangolin.net/development/system-architecture), [caddy-tailscale](https://github.com/tailscale/caddy-tailscale), [caddy-tailscale Funnel #26](https://github.com/tailscale/caddy-tailscale/issues/26), [Traefik HTTP provider](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/http/), [Railway edge proxy changelog](https://railway.com/changelog/2024-05-17-new-edge-proxy-beta)
- Cloudflare Tunnel (later): [create tunnel via API](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel-api/), [video delivery terms](https://developers.cloudflare.com/fundamentals/reference/policies-compliances/delivering-videos-with-cloudflare/)
