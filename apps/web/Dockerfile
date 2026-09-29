# Keel web: static SPA on nginx. Build context is the repo root.
# The Convex URLs are not baked in: docker-entrypoint.sh writes /config.js at container start
# from KEEL_CONVEX_URL / KEEL_CONVEX_SITE_URL, so one image serves every install.
FROM node:24-slim AS prune
COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /app
COPY . .
RUN bun x turbo@2.10.12 prune web --docker

FROM node:24-slim AS build
COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /app
COPY --from=prune /app/out/json/ .
# Root postinstall (varlock codegen) needs sources that are not copied yet; run it below.
RUN bun install --frozen-lockfile --ignore-scripts
COPY --from=prune /app/out/full/ .
ENV NODE_ENV=production
RUN cd apps/web && bun x varlock codegen && bun run build

FROM nginx:1.29-alpine
COPY --from=build /app/apps/web/dist /usr/share/nginx/html
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY apps/web/docker-entrypoint.sh /docker-entrypoint.d/40-keel-config.sh
EXPOSE 80
