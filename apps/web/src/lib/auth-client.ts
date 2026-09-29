import { convexClient, crossDomainClient } from "@convex-dev/better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";

import { ENV } from "../env";

export const authClient = createAuthClient({
  baseURL: ENV.VITE_CONVEX_SITE_URL,
  plugins: [convexClient(), crossDomainClient()],
});
