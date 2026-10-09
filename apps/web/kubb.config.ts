// The dashboard's API layer, generated from the Go server's OpenAPI document (`openapi.json` at
// the repo root, `make openapi` refreshes it). Output: src/api/gen (gitignored, rebuilt by
// `bun run api:generate`, which dev, build and check-types run first). Hand-written glue lives in
// src/lib/{api,query,realtime,session}.ts(x); src/api/README.md explains how to use it.
import { adapterOas } from "@kubb/adapter-oas";
import { pluginFetch } from "@kubb/plugin-fetch";
import { pluginReactQuery } from "@kubb/plugin-react-query";
import { pluginTs } from "@kubb/plugin-ts";
import { pluginZod } from "@kubb/plugin-zod";
import { defineConfig } from "kubb";

export default defineConfig({
  root: ".",
  input: "../../openapi.json",
  // JSON numbers stay numbers: times are epoch ms (`Date.now()` arithmetic), counts are small.
  adapter: adapterOas({
    integerType: "number",
    unknownType: "unknown",
    emptySchemaType: "unknown",
  }),
  // One barrel, `@/api/gen`: types, zod schemas, fetch functions and hooks by name.
  output: { path: "./src/api/gen", clean: true, barrel: { type: "named" } },
  plugins: [
    // Enums as literal unions (`status: "healthy" | "error" | …`), like the hand-written types.
    pluginTs({ output: { path: "./types" }, enum: { type: "inlineLiteral" } }),
    pluginZod({ output: { path: "./zod" } }),
    // Same-origin fetch functions. Their runtime (src/api/gen/.kubb/client.ts) is configured once
    // at start by setupApiClient (src/lib/api.ts): credentials, ApiError, Keel-Invalidate.
    pluginFetch({ output: { path: "./clients" } }),
    pluginReactQuery({
      output: { path: "./hooks" },
      hooks: true,
      suspense: {},
      // Every query key starts with the resolved request path (`/api/environments/abc/nodes`),
      // then the query parameters object when the operation has any: realtime topics and
      // Keel-Invalidate name path prefixes (docs/go/ARCHITECTURE.md, "Realtime").
      queryKey: ({ node }) => {
        const p = (node.path ?? "").replace(/\{([^}]+)\}/g, (_, n) => "${path." + n + "}");
        const hasQuery = node.parameters.some((x) => x.in === "query");
        return hasQuery ? ["`" + p + "`", "...(query ? [query] : [])"] : ["`" + p + "`"];
      },
    }),
  ],
});
