# Keel functions: one-shot job that pushes packages/backend to a self-hosted Convex backend
# and sets its environment. install.sh runs it on every install/upgrade. Build context is the
# repo root.
FROM node:24-slim AS prune
COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /app
COPY . .
RUN bun x turbo@2.10.12 prune @my-better-t-app/backend --docker

FROM node:24-slim
COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /app
COPY --from=prune /app/out/json/ .
RUN bun install --frozen-lockfile --ignore-scripts
COPY --from=prune /app/out/full/ .
COPY deploy/functions-entrypoint.sh /usr/local/bin/keel-functions
WORKDIR /app/packages/backend
ENTRYPOINT ["keel-functions"]
CMD ["deploy"]
