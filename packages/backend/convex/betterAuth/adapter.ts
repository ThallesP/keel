import { createApi } from "@convex-dev/better-auth";

import { createAuthOptions } from "../auth";
import schema from "./schema";

// The component's database API, generated from the local schema. Called by the Better Auth
// adapter in `convex/auth.ts` and, for reads of members and invitations, by `convex/access.ts`.
export const { create, findOne, findMany, updateOne, updateMany, deleteOne, deleteMany } =
  createApi(schema, createAuthOptions);
