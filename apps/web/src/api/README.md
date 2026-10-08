<!--
PAGE MIGRATION WORKLIST (delete each line when its file no longer imports convex, better-auth,
@my-better-t-app/backend, @/lib/auth-client or @/lib/config; delete this block when empty).
These are the only files `tsc --noEmit` still fails on; everything else compiles.

  src/components/auth-forms.tsx                         Q1 → useGetSignUpOpen
  src/components/sign-in-form.tsx                       B2 → useAuth().signIn
  src/components/sign-up-form.tsx                       B3 → useAuth().signUp
  src/components/invite-dialog.tsx                      B5 → useCreateInvitation (no organizationId)
  src/components/canvas/account-menu.tsx                Q2 Q3 B4 → useSession, useAuth().signOut
  src/components/canvas/actions.tsx                     M3–M13 + optimistic overlay (web-data.md §7, §8.5)
  src/components/canvas/copy-prompt.tsx                 Q20 → useGetTracingPrompt
  src/components/canvas/environment.tsx                 Id<…> → string
  src/components/canvas/errors.ts                       ConvexError → re-export errorMessage from @/lib/api; attempt → {ok,data} (§9.4)
  src/components/canvas/mapping.ts                      NodeView, Deployment from @/api/types; d.id; drop asNodeId
  src/components/canvas/project-switcher.tsx            Q21 M2 → useListProjects, useCreateProject
  src/components/canvas/settings.tsx                    Q4 Q5 M16 → useGetLogSink, useSession, useDisconnectLogSink
  src/components/canvas/topbar.tsx                      Q10 → useListNodes
  src/components/canvas/use-data.ts                     Q12 Q13 → useGetEnvironmentSummary, useGetLatestDeployment
  src/components/canvas/use-deployment-link.ts          Q14 → useGetDeployment (enabled: !!id)
  src/components/canvas/use-synced-graph.ts             Q11 → useListNodes + overlay
  src/components/canvas/bottom-panel/reference-palette.tsx   ReferenceSource from @/api/types
  src/components/canvas/bottom-panel/tabs/deployments.tsx    Q15 → useListNodeDeployments
  src/components/canvas/bottom-panel/tabs/logs.tsx           A1 → useTailNodeLogs (poll 3 s)
  src/components/canvas/bottom-panel/tabs/networking.tsx     Q16 → useGetControlPlane
  src/components/canvas/bottom-panel/tabs/tracing.tsx        Q17 A2 → useGetNodeTracing, useSetNodeTracing
  src/components/canvas/bottom-panel/tabs/variables.tsx      Q18 Q19 M14 M15 → useListVariables, useListReferenceableVariables, useSetVariable, useDeleteVariable
  src/components/canvas/observability/axiom-gate.tsx    Q7 Q8 M17 A3 A4 → useListPendingAxiomOrgs, useCancelAxiomSignIn, useBeginAxiomSignIn, useChooseAxiomOrg
  src/components/canvas/observability/charts.tsx        TraceBucket, TraceStats from @/api/types
  src/components/canvas/observability/chrome.tsx        Q9 → useListNodes
  src/components/canvas/observability/correlate.ts      Attribute from @/api/types
  src/components/canvas/observability/explorer.tsx      A5 A6 → useListEnvironmentLogs, useGetTraceOverview (poll 10 s)
  src/components/canvas/observability/lamp.tsx          Id<…> → string
  src/components/canvas/observability/log-context.tsx   A7 A8 → useListLogsAround, useListTracesAround
  src/components/canvas/observability/page.tsx          Q6 → useGetLogSink
  src/components/canvas/observability/stream.tsx        ProjectLine, TraceSummary from @/api/types
  src/components/canvas/observability/trace.tsx         A9 → useGetTrace (+ asTrace)
  src/routes/_auth/axiom/callback.tsx                   A10 → useCompleteAxiomSignIn (keep the run-once ref)
  src/routes/_auth/device.tsx                           Q22 B7 B8 B9 → useSession, useClaimDeviceCode, useApproveDevice, useDenyDevice
  src/routes/_auth/p/$projectId.tsx                     Q23 → useGetProjectBySlug (data?.project)
  src/routes/invite.$invitationId.tsx                   G3 Q24 Q25 B6 → SessionGate, useSession, useGetInvitation, useAuth().acceptInvitation

Already migrated: src/main.tsx, src/lib/*, src/routes/__root.tsx, src/routes/_auth/route.tsx,
src/routes/index.tsx. Q/M/A/B/G numbers are the call sites in docs/go/spec/web-data.md §4.
-->

# `src/api`: the dashboard's data layer

The dashboard talks to `keel serve` over a same-origin JSON API under `/api` and one WebSocket
(`/api/ws`) that only says "refetch these paths". Everything a component needs is a generated
hook; freshness, errors and the session are handled underneath.

| Module           | What it is                                                                                                                                                                                                                                                                                                                                                                    |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `@/api/gen`      | **Generated** by Kubb from `openapi.json` (gitignored; `bun run api:generate`, which `dev`, `build` and `check-types` run first). One barrel: types, zod schemas (`…Schema`), fetch functions (`listNodes`), TanStack Query hooks (`useListNodes`, `useCreateNode`), keys and options (`listNodesQueryKey`, `listNodesQueryOptions`). Hook names are `use` + the operationId. |
| `@/api/types`    | The API types under the names components use (`NodeView`, `NodeStatus`, `Deployment`, `VariableView`, `VariablePart`, `ReferenceSource`, `TracingView`, `LogTail`, `ProjectLine`, `TimeRange`, `Span`, `Attribute`, `Trace`, `TraceOverview`, `TraceSummary`, `ProjectHome`, `Sink`, …). Import types from here, not from `gen`.                                              |
| `@/lib/api`      | `ApiError`, `isApiError`, `errorMessage`; `setupApiClient` (called once in `main.tsx`).                                                                                                                                                                                                                                                                                       |
| `@/lib/session`  | `useSession`, `SessionGate` (+ `Authenticated` / `Unauthenticated` / `AuthLoading`), `useAuth` (sign in / up / out, accept invitation, refresh).                                                                                                                                                                                                                              |
| `@/lib/realtime` | `RealtimeProvider` (mounted in `main.tsx`), `useRealtimeStatus`, `useRealtime().reconnect`.                                                                                                                                                                                                                                                                                   |
| `@/lib/query`    | `createQueryClient`, topic matching, `KeelQueryMeta` (`meta.realtime`), the session cache transitions.                                                                                                                                                                                                                                                                        |

Regenerate after the server's API changes: `make openapi` (repo root), then `bun run api:generate`.
The API reference is the server's `/api/docs`.

## Reading

```tsx
import { useListNodes, useGetProjectBySlug, useGetDeployment } from "@/api/gen";

const { data } = useListNodes({ path: { id: environmentId } });
const nodes = data?.nodes ?? []; // arrays are typed `T[] | null` on the wire

// Nullable reads are 200 with an envelope: undefined = loading, null = not found / not yours.
const { data: home } = useGetProjectBySlug({ path: { slug } });
if (home === undefined) return <Loader />;
if (home.project === null) return <NotFound />;

// "skip" = enabled: false
const { data: link } = useGetDeployment(
  { path: { id: deploymentId ?? "" } },
  { query: { enabled: !!deploymentId } },
);
```

- Query keys are `[path]` or `[path, queryParams]`, e.g. `["/api/environments/abc/nodes"]`. Use the
  generated key helpers for cache writes: `queryClient.setQueryData(listNodesQueryKey({ path: { id } }), …)`.
- **Freshness is automatic.** The socket refetches every active query under a changed path; a
  reconnect refetches everything; your own writes are refetched before their promise resolves
  (see Writing). Do not add `refetchInterval` to realtime-covered reads.
- Defaults: `staleTime` 5 s, refetch on window focus, 4xx never retried, network/5xx retried 3×.
- No `throwOnError`: read `error` (an `ApiError`) where the page shows one.

Polled and one-shot reads opt out of invalidation with `meta: { realtime: false }`:

```tsx
// Logs tab (A1): poll every 3 s while visible.
useTailNodeLogs(
  { path: { id }, query: { tail: 300 } },
  {
    query: { enabled: deployable, refetchInterval: 3_000, staleTime: 0, meta: { realtime: false } },
  },
);
// Explorer (A5/A6): keep the old page while a new search loads.
useGetTraceOverview(
  { path: { id: environmentId }, query: { range, search } },
  {
    query: {
      enabled: active && !!sink.traces,
      refetchInterval: 10_000,
      placeholderData: keepPreviousData,
      meta: { realtime: false },
    },
  },
);
// Detail views (A7–A9) and static reads (Q16, Q20): once.
const { data } = useGetTrace(
  { path: { id: environmentId, traceId }, query: { at } },
  {
    query: { staleTime: Infinity, meta: { realtime: false }, select: asTrace }, // asTrace from @/api/types
  },
);
```

`GET /api/auth/device?user_code=` (`useClaimDeviceCode`) binds the code as a side effect: give it
`{ staleTime: Infinity, retry: false, refetchOnWindowFocus: false, meta: { realtime: false } }`.

## Writing

```tsx
import { useCreateNode, useDeleteNode } from "@/api/gen";
import { errorMessage } from "@/lib/api";

const createNode = useCreateNode();
try {
  const { id, deploymentId } = await createNode.mutateAsync({
    path: { id: environmentId },
    body: { type: "service", image, position, deploy: true },
  });
  // Resolves after the queries this write changed were refetched (Keel-Invalidate), so the new
  // node is already in useListNodes' data here.
} catch (err) {
  toast.error(errorMessage(err)); // the server's sentence, e.g. `"web" is already taken`
}
```

- Variables are `{ path, query, body }` as the operation needs them. Writes that answer 204
  resolve `undefined`: success is "did not throw" (change `attempt` in `canvas/errors.ts` to return
  `{ ok: true, data } | { ok: false }`, web-data.md §9.4).
- Read-your-writes is built in: every non-GET answer names its topics in `Keel-Invalidate`, and
  the client refetches the active queries under them before resolving. The socket's message for
  the same write follows and refetches once more (deduped).
- Optimistic UI (only move and delete on the canvas) needs the overlay recipe in web-data.md §7;
  `queryClient.cancelQueries({ queryKey: listNodesQueryKey({ path: { id } }) })` in `onMutate`.

## Errors

Every non-2xx throws `ApiError` (`@/lib/api`):

```ts
err.status; // 409
err.code; // "NAME_TAKEN" (docs/go/ARCHITECTURE.md "Errors"); RFC 8628 `error` on device endpoints
err.message; // `Project "web" already exists` (problem `detail`; `error_description` on device endpoints)
err.problem; // the RFC 9457 body, when it is one

isApiError(err); // any API error
isApiError(err, "NOT_FOUND", "FORBIDDEN"); // one of these codes
errorMessage(err); // what to show for anything thrown (ApiError, Error, other)
```

The hooks' declared error type is Kubb's `ResponseError`; at runtime it is always `ApiError`
(a subclass of `Error`), so use `isApiError` / `errorMessage` rather than the declared type.
A 401 while signed in means the session ended: the cache flips to signed out by itself.

## Session

```tsx
import { SessionGate, useAuth, useSession } from "@/lib/session";

const { user, organization, isLoading } = useSession(); // undefined = loading, null = none
organization?.role; // "owner" | "admin" | "member"

<SessionGate
  loading={<Loader />}
  signedOut={
    <AuthShell>
      <AuthForms />
    </AuthShell>
  }
>
  <Outlet />
</SessionGate>;
// Drop-ins for convex/react: <Authenticated>, <Unauthenticated>, <AuthLoading>.

const auth = useAuth();
await auth.signIn({ email, password }); // throws ApiError: "Invalid email or password"
await auth.signUp({ email, password, name, invitationId });
await auth.acceptInvitation(invitationId);
await auth.signOut(); // never navigates; gates flip in place
await auth.refreshSession(); // after any other write that changed the org
```

Always use `useAuth()` for these, never the generated `signIn` / `signUp` / `signOut` /
`acceptInvitation` (or `useSignIn` …): the wrappers reset the cache (it belonged to the old
session) and move the socket to the new organization channel. `useSession` replaces
`api.auth.getCurrentUser` and `api.organizations.current`; there is no separate organization query.

## Realtime

Mounted once in `main.tsx`; components do not use it. It connects only while signed in, follows
the session's user and organization, refetches on `{"type":"invalidate","topics":[…]}` (a topic is
a path prefix: `/api/environments/abc` covers `/api/environments/abc/nodes`; `/api/nodes/` covers
every node), refetches everything on reconnect, and on close code 4501 flips to signed out.
`useRealtimeStatus()` → `"idle" | "connecting" | "connected" | "disconnected"` if a page wants to
show a live indicator.

## Dev

`make dev` (repo root) runs `keel serve` on `127.0.0.1:3400`; `bun run dev:web` runs Vite on
`127.0.0.1:3001`, proxying `/api` (with the WebSocket), `/worker`, `/otlp`, `/proxy` and `/config.js`
to it. `KEEL_DEV_API=http://127.0.0.1:<port>` points Vite at another `keel serve`.
