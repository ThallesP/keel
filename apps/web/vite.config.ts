import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import { varlockVitePlugin } from "@varlock/vite-integration";
import react from "@vitejs/plugin-react";
import { type ProxyOptions, defineConfig } from "vite";

// `keel serve` in development (`make dev` listens on 127.0.0.1:3400). Override with KEEL_DEV_API.
const api = process.env.KEEL_DEV_API || "http://127.0.0.1:3400";
// The Host header is kept (no changeOrigin): keel serve accepts writes and the WebSocket only
// from the origin the browser shows (CSRF and Origin checks compare it with Host).
const toKeel: ProxyOptions = { target: api };

export default defineConfig({
  server: {
    port: 3001,
    // Loopback only. The tailnet reaches it as https://<node>.<tailnet>.ts.net through
    // `tailscale serve` (scripts/dev-https.sh), which keeps the original Host header.
    host: "127.0.0.1",
    allowedHosts: [".ts.net"],
    // Dashboard, API and WebSocket share one origin, as in production where keel serve embeds
    // the built dashboard.
    proxy: {
      "/api": { ...toKeel, ws: true },
      "/worker": toKeel,
      "/otlp": toKeel,
      "/proxy": toKeel,
    },
  },
  resolve: {
    tsconfigPaths: true,
  },
  plugins: [
    varlockVitePlugin({ ssrInjectMode: "auto-load" }),
    tailwindcss(),
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
      generatedRouteTree: "./src/gen/route-tree.ts",
    }),
    react(),
  ],
});
