import { defineSchema } from "convex/server";

import { tables } from "./generatedSchema";

// Generated tables (see scripts/generate-auth-schema.ts) plus the compound indexes the
// organization plugin's lookups use: a member by organization and user, pending invitations by
// organization, and by email within it. Names follow the generator's convention (fields joined
// by `_`), which is how the component's query planner finds them.
const schema = defineSchema({
  ...tables,
  member: tables.member.index("organizationId_userId", ["organizationId", "userId"]),
  invitation: tables.invitation
    .index("organizationId_status", ["organizationId", "status"])
    .index("email_organizationId_status", ["email", "organizationId", "status"]),
});

export default schema;
