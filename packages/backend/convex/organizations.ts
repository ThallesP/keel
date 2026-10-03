import { v } from "convex/values";

import { components } from "./_generated/api";
import { query } from "./_generated/server";
import { currentMembership } from "./access";

// Organizations are Better Auth's organization plugin (convex/auth.ts): rows live in the
// component, invitations are created and accepted through its endpoints from the web client.
// These queries are the read side the dashboard needs.

type Organization = { _id: string; name: string; slug: string };
type Invitation = { organizationId: string; email: string; status: string; expiresAt: number };

/** The signed-in user's organization and role, or null (signed out, or not a member yet). */
export const current = query({
  args: {},
  handler: async (ctx) => {
    const membership = await currentMembership(ctx);
    if (!membership) return null;
    const org = (await ctx.runQuery(components.betterAuth.adapter.findOne, {
      model: "organization",
      where: [{ field: "_id", value: membership.organizationId }],
    })) as Organization | null;
    return org ? { id: org._id, name: org.name, slug: org.slug, role: membership.role } : null;
  },
});

/**
 * What an invite link shows before sign-up: the email it is for and the organization it joins.
 * Null when the link is unknown, spent or expired. Public: the id is the secret in the link.
 */
export const invitation = query({
  args: { id: v.string() },
  handler: async (ctx, { id }) => {
    const row = (await ctx
      .runQuery(components.betterAuth.adapter.findOne, {
        model: "invitation",
        where: [{ field: "_id", value: id }],
      })
      .catch(() => null)) as Invitation | null; // a malformed id is not a valid one
    if (!row || row.status !== "pending" || row.expiresAt < Date.now()) return null;
    const org = (await ctx.runQuery(components.betterAuth.adapter.findOne, {
      model: "organization",
      where: [{ field: "_id", value: row.organizationId }],
    })) as Organization | null;
    if (!org) return null;
    return { email: row.email, organization: org.name };
  },
});
