import { v } from "convex/values";

import { mutation, query } from "./_generated/server";
import { requireUser } from "./access";
import { authComponent } from "./auth";

const DEFAULT = { name: "acme-support", slug: "acme-support" };

/** First-use bootstrap: one project + a production environment per user. Returns the slug. */
export const ensureDefault = mutation({
  args: {},
  handler: async (ctx) => {
    const user = await requireUser(ctx);
    const existing = await ctx.db
      .query("projects")
      .withIndex("by_owner", (q) => q.eq("ownerId", user._id))
      .first();
    if (existing) return existing.slug;
    const projectId = await ctx.db.insert("projects", { ...DEFAULT, ownerId: user._id });
    await ctx.db.insert("environments", { projectId, name: "production", isProduction: true });
    return DEFAULT.slug;
  },
});

/** Project + its production environment, or null when missing / not owned. */
export const getBySlug = query({
  args: { slug: v.string() },
  handler: async (ctx, { slug }) => {
    const user = await authComponent.safeGetAuthUser(ctx);
    if (!user) return null;
    const project = await ctx.db
      .query("projects")
      .withIndex("by_slug", (q) => q.eq("ownerId", user._id).eq("slug", slug))
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
