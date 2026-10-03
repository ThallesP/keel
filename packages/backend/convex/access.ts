import { ConvexError } from "convex/values";

import { components } from "./_generated/api";
import type { Id } from "./_generated/dataModel";
import type { MutationCtx, QueryCtx } from "./_generated/server";
import { authComponent } from "./auth";

export type Ctx = QueryCtx | MutationCtx;

export async function requireUser(ctx: Ctx) {
  const user = await authComponent.safeGetAuthUser(ctx);
  if (!user) throw new ConvexError("Not authenticated");
  return user;
}

export type Membership = {
  user: NonNullable<Awaited<ReturnType<typeof authComponent.safeGetAuthUser>>>;
  organizationId: string;
  role: string;
};

/**
 * The signed-in user's organization membership, or null (signed out, or no organization yet).
 * Members and organizations are Better Auth's organization-plugin rows, read from the component.
 * One organization per install for now, so a user has at most one membership.
 */
export async function currentMembership(ctx: Ctx): Promise<Membership | null> {
  const user = await authComponent.safeGetAuthUser(ctx);
  if (!user) return null;
  const member = (await ctx.runQuery(components.betterAuth.adapter.findOne, {
    model: "member",
    where: [{ field: "userId", value: user._id }],
  })) as { organizationId: string; role: string } | null;
  return member ? { user, organizationId: member.organizationId, role: member.role } : null;
}

export const NO_ORGANIZATION =
  "You're not in an organization yet. Ask a member for an invite link.";

export async function requireMembership(ctx: Ctx) {
  const membership = await currentMembership(ctx);
  if (!membership) throw new ConvexError(NO_ORGANIZATION);
  return membership;
}

/** Project of the signed-in user's organization, or null (missing, foreign, or signed out). */
export async function ownedProject(ctx: Ctx, id: Id<"projects">) {
  const membership = await currentMembership(ctx);
  if (!membership) return null;
  const project = await ctx.db.get(id);
  return project && project.organizationId === membership.organizationId ? project : null;
}

export async function ownedEnvironment(ctx: Ctx, id: Id<"environments">) {
  const environment = await ctx.db.get(id);
  if (!environment) return null;
  const project = await ownedProject(ctx, environment.projectId);
  return project ? { environment, project } : null;
}

export async function ownedNode(ctx: Ctx, id: Id<"nodes">) {
  const node = await ctx.db.get(id);
  if (!node) return null;
  const scope = await ownedEnvironment(ctx, node.environmentId);
  return scope ? { node, ...scope } : null;
}

export async function requireEnvironment(ctx: Ctx, id: Id<"environments">) {
  const scope = await ownedEnvironment(ctx, id);
  if (!scope) throw new ConvexError("Environment not found");
  return scope;
}

export async function requireNode(ctx: Ctx, id: Id<"nodes">) {
  const scope = await ownedNode(ctx, id);
  if (!scope) throw new ConvexError("Node not found");
  return scope;
}

const NAME_RE = /^[a-z0-9-]{1,40}$/;
const ENV_KEY_RE = /^[A-Z_][A-Z0-9_]{0,63}$/;
const IMAGE_RE = /^[a-z0-9][a-z0-9._\-/:@]{0,199}$/;

export function validName(name: string) {
  if (!NAME_RE.test(name)) throw new ConvexError("Name: 1–40 chars, a-z 0-9 and - only");
  return name;
}

export function validEnvKey(key: string) {
  if (!ENV_KEY_RE.test(key)) throw new ConvexError("Key: UPPER_SNAKE_CASE only");
  return key;
}

/** Image references only; never a command, mount or socket. */
export function validImage(image: string) {
  if (!IMAGE_RE.test(image)) throw new ConvexError("Image must look like repo/name:tag");
  return image;
}

export function validPort(port: number | undefined) {
  if (port !== undefined && (!Number.isInteger(port) || port < 1 || port > 65535)) {
    throw new ConvexError("Port must be 1–65535");
  }
  return port;
}
