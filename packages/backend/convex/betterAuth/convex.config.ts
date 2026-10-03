import { defineComponent } from "convex/server";

// Local install of the Better Auth component (labs.convex.dev/better-auth/features/local-install):
// the schema lives in this directory so plugins with their own tables (organization) can be used.
const component = defineComponent("betterAuth");

export default component;
