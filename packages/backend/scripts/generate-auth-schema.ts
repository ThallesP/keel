// Regenerates convex/betterAuth/generatedSchema.ts from the Better Auth options in convex/auth.ts,
// so the component's tables match the plugins in use (the organization plugin adds its own).
// convex/betterAuth/schema.ts imports those tables and adds Keel's own indexes. Run from
// packages/backend after changing plugins:
//
//   bun scripts/generate-auth-schema.ts
//
// Same output as `npx @better-auth/cli generate` in convex/betterAuth (the documented way); this
// calls the component's generator directly so no CLI version needs to line up with better-auth.
import { getAuthTables } from "better-auth/db";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { createAuthOptions } from "../convex/auth";

process.env.SITE_URL ??= "http://localhost:3001";

const pkgDir = dirname(fileURLToPath(import.meta.resolve("@convex-dev/better-auth")));
const { createSchema } = (await import(join(pkgDir, "create-schema.js"))) as {
  createSchema: (input: {
    tables: ReturnType<typeof getAuthTables>;
    file?: string;
  }) => Promise<{ code: string; path: string }>;
};

const out = join(import.meta.dirname, "../convex/betterAuth/generatedSchema.ts");
const { code } = await createSchema({
  tables: getAuthTables(createAuthOptions({} as never)),
  file: out,
});
await Bun.write(out, code);
console.log(`wrote ${out}`);
