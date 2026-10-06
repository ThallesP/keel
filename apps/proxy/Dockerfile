# keel-proxy: the control plane's public edge. Caddy (HTTPS, ACME) + caddy-l4 (raw TCP/UDP) +
# this directory's plugin (listeners in the host network namespace, certificate reports).
# Convex configures it through the admin API; nothing here is ever edited by hand.
# See docs/networking.md. Versions pinned: bump Caddy and caddy-l4 together, deliberately.
FROM caddy:2.11.7-builder AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY *.go ./
RUN xcaddy build v2.11.7 --output /usr/bin/caddy \
  --with github.com/mholt/caddy-l4@v0.1.2 \
  --with github.com/ThallesP/keel/apps/proxy=/src

FROM caddy:2.11.7-alpine
COPY --from=build /usr/bin/caddy /usr/bin/caddy
COPY caddy.json /etc/keel-proxy/caddy.json
# --resume: after a restart, serve the last config Convex pushed (autosaved under /config).
CMD ["caddy", "run", "--resume", "--config", "/etc/keel-proxy/caddy.json"]
