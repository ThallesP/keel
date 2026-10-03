/* eslint-disable */
/**
 * Generated `api` utility.
 *
 * THIS CODE IS AUTOMATICALLY GENERATED.
 *
 * To regenerate, run `npx convex dev`.
 * @module
 */

import type * as access from "../access.js";
import type * as auth from "../auth.js";
import type * as deployments from "../deployments.js";
import type * as environments from "../environments.js";
import type * as events from "../events.js";
import type * as healthCheck from "../healthCheck.js";
import type * as http from "../http.js";
import type * as logProviders_axiom from "../logProviders/axiom.js";
import type * as logProviders_docker from "../logProviders/docker.js";
import type * as logProviders_types from "../logProviders/types.js";
import type * as logSinks from "../logSinks.js";
import type * as logs from "../logs.js";
import type * as nodeHelpers from "../nodeHelpers.js";
import type * as nodes from "../nodes.js";
import type * as nodesInternal from "../nodesInternal.js";
import type * as organizations from "../organizations.js";
import type * as privateData from "../privateData.js";
import type * as projects from "../projects.js";
import type * as reconcile from "../reconcile.js";
import type * as status from "../status.js";
import type * as swarm from "../swarm.js";
import type * as timeRange from "../timeRange.js";
import type * as traceProviders_axiom from "../traceProviders/axiom.js";
import type * as traceProviders_types from "../traceProviders/types.js";
import type * as traces from "../traces.js";
import type * as variables from "../variables.js";
import type * as worker from "../worker.js";

import type {
  ApiFromModules,
  FilterApi,
  FunctionReference,
} from "convex/server";

declare const fullApi: ApiFromModules<{
  access: typeof access;
  auth: typeof auth;
  deployments: typeof deployments;
  environments: typeof environments;
  events: typeof events;
  healthCheck: typeof healthCheck;
  http: typeof http;
  "logProviders/axiom": typeof logProviders_axiom;
  "logProviders/docker": typeof logProviders_docker;
  "logProviders/types": typeof logProviders_types;
  logSinks: typeof logSinks;
  logs: typeof logs;
  nodeHelpers: typeof nodeHelpers;
  nodes: typeof nodes;
  nodesInternal: typeof nodesInternal;
  organizations: typeof organizations;
  privateData: typeof privateData;
  projects: typeof projects;
  reconcile: typeof reconcile;
  status: typeof status;
  swarm: typeof swarm;
  timeRange: typeof timeRange;
  "traceProviders/axiom": typeof traceProviders_axiom;
  "traceProviders/types": typeof traceProviders_types;
  traces: typeof traces;
  variables: typeof variables;
  worker: typeof worker;
}>;

/**
 * A utility for referencing Convex functions in your app's public API.
 *
 * Usage:
 * ```js
 * const myFunctionReference = api.myModule.myFunction;
 * ```
 */
export declare const api: FilterApi<
  typeof fullApi,
  FunctionReference<any, "public">
>;

/**
 * A utility for referencing Convex functions in your app's internal API.
 *
 * Usage:
 * ```js
 * const myFunctionReference = internal.myModule.myFunction;
 * ```
 */
export declare const internal: FilterApi<
  typeof fullApi,
  FunctionReference<any, "internal">
>;

export declare const components: {
  betterAuth: import("../betterAuth/_generated/component.js").ComponentApi<"betterAuth">;
};
