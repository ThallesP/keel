// The API's types under the names components use, so no component imports from
// `@my-better-t-app/backend` or spells a generated `…Status200` name. Everything here is the wire
// shape from openapi.json (src/api/gen/types), renamed or narrowed:
//   - nullable reads (`{summary | null}`, `{tracing | null}`, …) export the non-null item type;
//   - `Attribute` is the hand-written `[key, value]` tuple (see below).
// Arrays the server sends are typed `T[] | null` (Go nil slices are allowed on the wire): read
// them with `?? []`.
import type * as G from "./gen";

// ── Canvas ──────────────────────────────────────────────────────────────────────────────────

/** One node of the canvas (`GET /api/environments/{id}/nodes`, creation order). */
export type NodeView = G.NodeView;
/** `healthy | done | deploying | stopping | error | stopped | pending` (status.ts in Convex). */
export type NodeStatus = G.NodeView["status"];
/** `service | database | cache | volume | group`. */
export type NodeType = G.NodeView["type"];
export type NodeConfig = G.NodeConfig;
export type NodeDeploy = G.NodeDeploy;
export type Position = G.Position;
/** A public endpoint of a node (http domain or tcp/udp public port). */
export type EndpointView = G.EndpointView;
export type EndpointProtocol = G.EndpointView["protocol"];
export type EndpointState = G.EndpointView["state"];
/** `GET /api/environments/{id}/summary` → `summary` when the environment is the caller's. */
export type EnvironmentSummary = NonNullable<G.EnvironmentSummary>;
export type CreateNodeRequest = G.CreateNodeRequest;
export type UpdateNodeRequest = G.UpdateNodeRequest;

// ── Deployments ─────────────────────────────────────────────────────────────────────────────

/** A deployment (`id`, never Convex's `_id`). */
export type Deployment = G.Deployment;
export type DeploymentStatus = G.Deployment["status"];
export type DeployStep = G.DeployStep;
export type DeployStepStatus = G.DeployStep["status"];
export type DeployLogLine = G.DeployLogLine;

// ── Variables ───────────────────────────────────────────────────────────────────────────────

export type VariableView = G.VariableView;
/** Exactly one of `text` / `ref` is set: discriminate with `"ref" in part` or `part.ref`. */
export type VariablePart = G.VariablePart;
export type VariableRef = G.VariableRef;
export type ReferenceSource = G.ReferenceSource;
export type ReferenceKey = G.ReferenceKey;

// ── Tracing (per node) ──────────────────────────────────────────────────────────────────────

/** `GET /api/nodes/{id}/tracing` → `tracing` when the node is a service the caller sees. */
export type TracingView = NonNullable<G.TracingView>;
export type TracingEnvVar = G.TracingEnvVar;

// ── Logs ────────────────────────────────────────────────────────────────────────────────────

/** One service log line (Logs tab). `task` is "" when unknown. */
export type LogLine = G.ServiceLogLine;
export type Replica = G.LogReplica;
/** `GET /api/nodes/{id}/logs` (Convex `logs.tail`'s `Tail`). */
export type LogTail = G.LogTail;
/** @deprecated Convex's name; use LogTail. */
export type Tail = LogTail;
/** A log line of any service of an environment; `serviceId` is the node id. */
export type ProjectLine = G.EnvironmentLogLine;
/** `GET /api/environments/{id}/logs` (Convex `logs.recent`). */
export type ProjectTail = G.EnvironmentLogs;
export type LogSource = G.LogTail["source"];

// ── Traces ──────────────────────────────────────────────────────────────────────────────────

/** `15m | 1h | 24h | 7d`. */
export type TimeRange = G.GetTraceOverviewQuery["range"];

/**
 * A span or resource attribute: `[key, value]`, sorted by key. The wire is always a pair; the
 * OpenAPI document can only say "array of 2 strings" (Huma emits no tuple), so the generated
 * types read `string[][]`. Responses carrying spans go through `asTrace` (or a cast) once, at the
 * hook boundary.
 */
export type Attribute = [key: string, value: string];
export type SpanEvent = Omit<G.SpanEvent, "attributes"> & { attributes: Attribute[] | null };
export type Span = Omit<G.Span, "attributes" | "resource" | "events"> & {
  attributes: Attribute[] | null;
  resource: Attribute[] | null;
  events: SpanEvent[] | null;
};
export type SpanStatus = G.Span["status"];
/** `GET /api/environments/{id}/traces/{traceId}`: one request, its spans and log lines. */
export type Trace = Omit<G.Trace, "spans"> & { spans: Span[] | null };
/** `GET /api/environments/{id}/traces`: rate, errors, latency and the latest requests. */
export type TraceOverview = G.TraceOverview;
export type TraceSummary = G.TraceSummary;
export type TraceBucket = G.TraceBucket;
export type TraceStats = G.TraceStats;

/** The generated `Trace` with its attributes typed as tuples (no runtime change). */
export function asTrace(trace: G.Trace): Trace;
export function asTrace(trace: G.Trace | undefined): Trace | undefined;
export function asTrace(trace: G.Trace | undefined): Trace | undefined {
  return trace as Trace | undefined;
}

// ── Projects ────────────────────────────────────────────────────────────────────────────────

/** `GET /api/projects` item. */
export type ProjectSummary = G.ProjectSummary;
export type ProjectEnvironment = G.ProjectEnvironment;
/** `GET /api/projects/by-slug/{slug}` → `project` when it exists and is the caller's. */
export type ProjectHome = NonNullable<G.ProjectHome>;

// ── Account and organization ────────────────────────────────────────────────────────────────

/** `GET /api/me`; read it through `useSession()` (src/lib/session.tsx). */
export type Me = G.Me;
export type User = G.User;
/** The caller's organization, with their `role` in it. */
export type Organization = G.Organization;
export type OrganizationRole = G.Organization["role"];
export type Member = G.Member;
export type Invitation = G.Invitation;
export type CreatedInvitation = G.CreatedInvitation;
/** `GET /api/invitations/{id}` → `invitation` (public: email and organization name). */
export type PublicInvitation = G.PublicInvitation;
export type DeviceStatus = G.DeviceStatus;

// ── Observability sink (organization-wide) ──────────────────────────────────────────────────

/** `GET /api/organization/log-sink` → `sink` when the organization has one (Axiom). */
export type LogSinkView = NonNullable<G.LogSinkView>;
/** What the Observability page needs of the sink (chrome.tsx's `Sink`). */
export type Sink = Pick<LogSinkView, "domain" | "dataset" | "traces" | "org">;
export type AxiomOrgChoice = G.AxiomOrgChoice;

// ── Misc ────────────────────────────────────────────────────────────────────────────────────

export type ControlPlane = G.ControlPlane;
export type Meta = G.Meta;
/** The RFC 9457 error body; failures surface as `ApiError` (src/lib/api.ts). */
export type Problem = G.Problem;
