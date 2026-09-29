import { convexClient, crossDomainClient } from "@convex-dev/better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";

import { config } from "./config";

export const authClient = createAuthClient({
  baseURL: config.convexSiteUrl,
  plugins: [convexClient(), crossDomainClient()],
});
