import { ENV } from "../env";

// An installed Keel ships one web image for every server, so the Convex URLs arrive at
// runtime via /config.js (written by apps/web/docker-entrypoint.sh). Dev uses the .env values.
declare global {
  interface Window {
    __KEEL__?: { convexUrl?: string; convexSiteUrl?: string };
  }
}

function required(name: string, value: string | undefined) {
  if (!value) throw new Error(`${name} is not configured (window.__KEEL__ or .env)`);
  return value;
}

export const config = {
  convexUrl: required("convexUrl", window.__KEEL__?.convexUrl || ENV.VITE_CONVEX_URL),
  convexSiteUrl: required(
    "convexSiteUrl",
    window.__KEEL__?.convexSiteUrl || ENV.VITE_CONVEX_SITE_URL,
  ),
};
