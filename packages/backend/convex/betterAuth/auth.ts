import { createAuth } from "../auth";

// Static instance for Better Auth schema generation only (`bunx @better-auth/cli generate`, run
// from this directory). Never import this at runtime: the deployment env is not available here.
export const auth = createAuth({} as any);
