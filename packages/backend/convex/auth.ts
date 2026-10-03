import { createClient, type GenericCtx } from "@convex-dev/better-auth";
import { convex, crossDomain } from "@convex-dev/better-auth/plugins";
import { APIError } from "better-auth/api";
import { betterAuth, type BetterAuthOptions } from "better-auth/minimal";
import { organization } from "better-auth/plugins/organization";

import { components } from "./_generated/api";
import type { DataModel } from "./_generated/dataModel";
import { query } from "./_generated/server";
import authConfig from "./auth.config";
import authSchema from "./betterAuth/schema";

const siteUrl = process.env.SITE_URL!;

// Local install of the component (convex/betterAuth): the schema there is generated from
// createAuthOptions below, so the organization plugin's tables exist. Regenerate it after
// changing plugins: `bun scripts/generate-auth-schema.ts` in packages/backend.
export const authComponent = createClient<DataModel, typeof authSchema>(components.betterAuth, {
  local: { schema: authSchema },
});

/** An invite link is handed over out of band by the inviter; a week is plenty. */
const INVITATION_TTL_S = 7 * 24 * 60 * 60;

// The Better Auth database adapter, as the database hooks see it. Derived rather than imported
// so the hooks below type-check against exactly what they receive.
type UserCreateHook = NonNullable<
  NonNullable<NonNullable<BetterAuthOptions["databaseHooks"]>["user"]>["create"]
>;
type Endpoint = NonNullable<Parameters<NonNullable<UserCreateHook["before"]>>[1]>;
type Adapter = Endpoint["context"]["adapter"];

type Invitation = {
  id: string;
  organizationId: string;
  email: string;
  role: string | null;
  status: string;
  expiresAt: Date | number;
};

const sameEmail = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase();

/** `invitationId` rides along in the sign-up body (an invite link puts it there). */
function invitationIdOf(endpoint: Endpoint | null): string | null {
  const id = (endpoint?.body as { invitationId?: unknown } | undefined)?.invitationId;
  return typeof id === "string" && id.length > 0 ? id : null;
}

/** The invitation, if it still stands: pending and not expired. */
async function pendingInvitation(adapter: Adapter, id: string): Promise<Invitation | null> {
  const row = await adapter
    .findOne<Invitation>({ model: "invitation", where: [{ field: "id", value: id }] })
    .catch(() => null); // a malformed id is not a valid one
  if (!row || row.status !== "pending") return null;
  if (new Date(row.expiresAt).getTime() < Date.now()) return null;
  return row;
}

async function anyRow(adapter: Adapter, model: string) {
  const rows = await adapter.findMany<{ id: string }>({ model, limit: 1 });
  return rows.length > 0;
}

/**
 * One organization per install, invite-only after the first account:
 *
 * - The first account ever can sign up freely. It founds the organization on its first visit
 *   (projects.ensureDefault) and owns it. This also covers installs from before organizations
 *   existed: whoever signs in first founds it.
 * - Every later sign-up must carry a pending invitation for its email (Account → Invite people
 *   produces the link). The account joins the organization as the invitation was created, and
 *   the invitation is spent.
 * - Sessions carry the member's organization as `activeOrganizationId`, which the organization
 *   plugin's endpoints (invite, list members) default to.
 */
export const createAuthOptions = (ctx: GenericCtx<DataModel>) =>
  ({
    baseURL: process.env.CONVEX_SITE_URL,
    trustedOrigins: [siteUrl],
    database: authComponent.adapter(ctx),
    // Convex assigns `_id`; the organization plugin would otherwise mint its own invitation ids
    // and the component's create validator rejects a supplied `_id`.
    advanced: { database: { generateId: false } },
    emailAndPassword: {
      enabled: true,
      requireEmailVerification: false,
    },
    databaseHooks: {
      user: {
        create: {
          before: async (user, endpoint) => {
            const adapter = endpoint?.context.adapter;
            if (!adapter) throw new APIError("FORBIDDEN", { message: "Sign-up needs a request" });
            if (!(await anyRow(adapter, "user"))) return; // the first account
            const id = invitationIdOf(endpoint);
            const invitation = id ? await pendingInvitation(adapter, id) : null;
            if (!invitation || !sameEmail(invitation.email, user.email)) {
              throw new APIError("FORBIDDEN", {
                message: "Sign-up is by invitation. Ask a member for an invite link.",
              });
            }
          },
          after: async (user, endpoint) => {
            const adapter = endpoint?.context.adapter;
            const id = invitationIdOf(endpoint);
            if (!adapter || !id) return;
            const invitation = await pendingInvitation(adapter, id);
            if (!invitation || !sameEmail(invitation.email, user.email)) return;
            await adapter.update({
              model: "invitation",
              where: [{ field: "id", value: invitation.id }],
              update: { status: "accepted" },
            });
            await adapter.create({
              model: "member",
              data: {
                organizationId: invitation.organizationId,
                userId: user.id,
                role: invitation.role ?? "member",
                createdAt: new Date(),
              },
            });
          },
        },
      },
      session: {
        create: {
          before: async (session, endpoint) => {
            const adapter = endpoint?.context.adapter;
            if (!adapter) return;
            const member = await adapter.findOne<{ organizationId: string }>({
              model: "member",
              where: [{ field: "userId", value: session.userId }],
            });
            return { data: { ...session, activeOrganizationId: member?.organizationId ?? null } };
          },
        },
      },
    },
    plugins: [
      organization({
        // The organization is founded by the install (projects.ensureDefault), never by a user.
        allowUserToCreateOrganization: false,
        invitationExpiresIn: INVITATION_TTL_S,
        cancelPendingInvitationsOnReInvite: true,
        // Nothing is mailed: the inviter copies the link out of the dialog.
        sendInvitationEmail: async () => {},
      }),
      crossDomain({ siteUrl }),
      convex({
        authConfig,
        jwksRotateOnTokenGenerationError: true,
      }),
    ],
  }) satisfies BetterAuthOptions;

export const createAuth = (ctx: GenericCtx<DataModel>) => betterAuth(createAuthOptions(ctx));

export const getCurrentUser = query({
  args: {},
  handler: async (ctx) => {
    return await authComponent.safeGetAuthUser(ctx);
  },
});

/** True until the first account exists. The sign-up form shows only then, or from an invite link. */
export const signUpOpen = query({
  args: {},
  handler: async (ctx) => {
    const users = await ctx.runQuery(components.betterAuth.adapter.findMany, {
      model: "user",
      paginationOpts: { numItems: 1, cursor: null },
    });
    return users.page.length === 0;
  },
});
