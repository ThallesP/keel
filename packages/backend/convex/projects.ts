import { ConvexError, v } from "convex/values";

import { components } from "./_generated/api";
import { mutation, query } from "./_generated/server";
import { currentMembership, NO_ORGANIZATION, requireUser } from "./access";
import { uniqueName } from "./nodeHelpers";

const DEFAULT = { name: "acme-support", slug: "acme-support" };
/** The one organization of an install. Auto-created; nothing renames it yet. */
const ORGANIZATION = { name: "Default", slug: "default" };

/**
 * First-use bootstrap: the install's organization, then one project + a production environment
 * in it. Returns the project slug.
 *
 * Founding: with no organization yet, the signed-in user creates it and owns it. That is the
 * first account of a fresh install, or whoever signs in first after an upgrade from before
 * organizations existed. Once one exists, an account without a membership is one whose invite
 * did not go through; it needs a new invite link.
 */
export const ensureDefault = mutation({
  args: {},
  handler: async (ctx) => {
    const user = await requireUser(ctx);
    let membership = await currentMembership(ctx);
    if (!membership) {
      const existing = await ctx.runQuery(components.betterAuth.adapter.findMany, {
        model: "organization",
        paginationOpts: { numItems: 1, cursor: null },
      });
      if (existing.page.length > 0) throw new ConvexError(NO_ORGANIZATION);
      const org = (await ctx.runMutation(components.betterAuth.adapter.create, {
        input: { model: "organization", data: { ...ORGANIZATION, createdAt: Date.now() } },
      })) as { _id: string };
      await ctx.runMutation(components.betterAuth.adapter.create, {
        input: {
          model: "member",
          data: { organizationId: org._id, userId: user._id, role: "owner", createdAt: Date.now() },
        },
      });
      membership = { user, organizationId: org._id, role: "owner" };
    }
    // Projects from before organizations existed belonged to one user each. One organization per
    // install takes them all; `ownerId` goes with the adoption. Every user got the same default
    // project back then, so slugs collide: the founder's own keep theirs, the rest get a suffix.
    const legacy = await ctx.db
      .query("projects")
      .withIndex("by_organization", (q) => q.eq("organizationId", undefined))
      .collect();
    const own = await ctx.db
      .query("projects")
      .withIndex("by_organization", (q) => q.eq("organizationId", membership.organizationId))
      .collect();
    const taken = new Set(own.map((p) => p.slug));
    legacy.sort((a, b) => Number(b.ownerId === user._id) - Number(a.ownerId === user._id));
    for (const p of legacy) {
      const slug = uniqueName(p.slug, taken);
      taken.add(slug);
      await ctx.db.patch(p._id, {
        organizationId: membership.organizationId,
        ownerId: undefined,
        slug,
        name: slug === p.slug ? p.name : slug,
      });
    }
    const existing = legacy.find((p) => p.ownerId === user._id) ?? own[0] ?? legacy[0];
    if (existing) return (await ctx.db.get(existing._id))?.slug ?? existing.slug;
    const projectId = await ctx.db.insert("projects", {
      ...DEFAULT,
      organizationId: membership.organizationId,
    });
    await ctx.db.insert("environments", { projectId, name: "production", isProduction: true });
    return DEFAULT.slug;
  },
});

/** Project + its production environment, or null when missing / not in the user's organization. */
export const getBySlug = query({
  args: { slug: v.string() },
  handler: async (ctx, { slug }) => {
    const membership = await currentMembership(ctx);
    if (!membership) return null;
    const project = await ctx.db
      .query("projects")
      .withIndex("by_slug", (q) =>
        q.eq("organizationId", membership.organizationId).eq("slug", slug),
      )
      .unique();
    if (!project) return null;
    const environments = await ctx.db
      .query("environments")
      .withIndex("by_project", (q) => q.eq("projectId", project._id))
      .collect();
    const environment = environments.find((e) => e.isProduction) ?? environments[0];
    if (!environment) return null;
    return {
      id: project._id,
      name: project.name,
      slug: project.slug,
      environment: { id: environment._id, name: environment.name },
    };
  },
});
