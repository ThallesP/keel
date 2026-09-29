# Keel per-node worker. No dependencies: Bun runs the TypeScript directly.
FROM oven/bun:1-alpine
WORKDIR /app
COPY package.json tsconfig.json ./
COPY src ./src
ENV NODE_ENV=production
CMD ["bun", "src/index.ts"]
