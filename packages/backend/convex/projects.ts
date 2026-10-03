import { ConvexError, v } from "convex/values";

import { components } from "./_generated/api";
import { mutation, query, type MutationCtx } from "./_generated/server";
import { currentMembership, type Ctx, NO_ORGANIZATION, requireUser } from "./access";
import { uniqueName } from "./nodeHelpers";

const DEFAULT = { name: "acme-support", slug: "acme-support" };
/** The one organization of an install. Auto-created; nothing renames it yet. */
const ORGANIZATION = { name: "Default", slug: "default" };

async function organizationExists(ctx: Ctx) {
  const existing = await ctx.runQuery(components.betterAuth.adapter.findMany, {
    model: "organization",
    paginationOpts: { numItems: 1, cursor: null },
  });
  return existing.page.length > 0;
}

/**
 * The signed-in user's membership, founding the organization if there is none yet: the user
 * creates it and owns it. That is the first account of a fresh install, or whoever signs in first
 * after an upgrade from before organizations existed. Once one exists, an account without a
 * membership is one whose invite did not go through; it needs a new invite link.
 */
async function joinOrFound(ctx: MutationCtx) {
  const user = await requireUser(ctx);
  const membership = await currentMembership(ctx);
  if (membership) return membership;
  if (await organizationExists(ctx)) throw new ConvexError(NO_ORGANIZATION);
  const org = (await ctx.runMutation(components.betterAuth.adapter.create, {
    input: { model: "organization", data: { ...ORGANIZATION, createdAt: Date.now() } },
  })) as { _id: string };
  await ctx.runMutation(components.betterAuth.adapter.create, {
    input: {
      model: "member",
      data: { organizationId: org._id, userId: user._id, role: "owner", createdAt: Date.now() },
    },
  });
  return { user, organizationId: org._id, role: "owner" };
}

/** A project is never without its production environment. */
async function insertProject(
  ctx: MutationCtx,
  organizationId: string,
  project: { name: string; slug: string },
) {
  const projectId = await ctx.db.insert("projects", { ...project, organizationId });
  const environmentId = await ctx.db.insert("environments", {
    projectId,
    name: "production",
    isProduction: true,
  });
  return { projectId, environmentId };
}

/**
 * First-use bootstrap: the install's organization (see joinOrFound), then one project + a
 * production environment in it. Returns the project slug.
 */
export const ensureDefault = mutation({
  args: {},
  handler: async (ctx) => {
    const membership = await joinOrFound(ctx);
    const { user } = membership;
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
    await insertProject(ctx, membership.organizationId, DEFAULT);
    return DEFAULT.slug;
  },
});

/** "My API" → "my-api": what URLs and the CLI name a project by. */
function slugOf(name: string) {
  return name
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .slice(0, 40)
    .replace(/^-+|-+$/g, "");
}

/**
 * A new project with its production environment, slug derived from the name. A taken slug is an
 * error, not a suffix: whoever asked for it (often an agent) gets told, instead of a project
 * named other than asked. Founds the organization like ensureDefault, so `keel project create`
 * works on a fresh install whose first account never opened the dashboard's home page.
 */
export const create = mutation({
  args: { name: v.string() },
  handler: async (ctx, args) => {
    const { organizationId } = await joinOrFound(ctx);
    const name = args.name.trim();
    if (name.length === 0 || name.length > 60) {
      throw new ConvexError("Project name: 1–60 characters");
    }
    const slug = slugOf(name);
    if (!slug) throw new ConvexError("Project name needs a letter or digit (a-z, 0-9)");
    const taken = await ctx.db
      .query("projects")
      .withIndex("by_slug", (q) => q.eq("organizationId", organizationId).eq("slug", slug))
      .first();
    if (taken) throw new ConvexError(`Project "${slug}" already exists`);
    const { projectId, environmentId } = await insertProject(ctx, organizationId, { name, slug });
    return {
      id: projectId,
      name,
      slug,
      environments: [{ id: environmentId, name: "production", isProduction: true }],
    };
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

/**
 * Every project of the signed-in user's organization with its environments, production first.
 * Throws instead of returning [] without a membership: the CLI tells "no projects" from "not in
 * an organization". Before the organization is founded there is nothing to be left out of, so
 * that is [] (and `keel project create` founds it).
 */
export const list = query({
  args: {},
  handler: async (ctx) => {
    const membership = await currentMembership(ctx);
    if (!membership) {
      await requireUser(ctx); // signed out: "Not authenticated"
      if (await organizationExists(ctx)) throw new ConvexError(NO_ORGANIZATION);
      return [];
    }
    const { organizationId } = membership;
    const projects = await ctx.db
      .query("projects")
      .withIndex("by_organization", (q) => q.eq("organizationId", organizationId))
      .collect();
    return await Promise.all(
      projects.map(async (p) => {
        const environments = await ctx.db
          .query("environments")
          .withIndex("by_project", (q) => q.eq("projectId", p._id))
          .collect();
        environments.sort((a, b) => Number(b.isProduction) - Number(a.isProduction));
        return {
          id: p._id,
          name: p.name,
          slug: p.slug,
          environments: environments.map((e) => ({
            id: e._id,
            name: e.name,
            isProduction: e.isProduction,
          })),
        };
      }),
    );
  },
});
