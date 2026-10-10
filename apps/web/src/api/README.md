# `src/api`: the dashboard's data layer

The dashboard talks to `keel serve` over a same-origin JSON API under `/api` and one WebSocket
(`/api/ws`) that only says "refetch these paths". Everything a component needs is a generated
hook; freshness, errors and the session are handled underneath.

| Module           | What it is                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `@/api/gen`      | **Generated** by Kubb from `openapi.json` (gitignored; `bun run api:generate`, which `dev`, `build` and `check-types` run first). One barrel: types under the server's names (`NodeView`, `EnvironmentLogLine`, `Span`, …), zod schemas (`…Schema`), fetch functions (`listNodes`), TanStack Query hooks (`useListNodes`, `useCreateNode`), keys and options (`listNodesQueryKey`, `listNodesQueryOptions`). Hook names are `use` + the operationId. Import types from here; a nullable read's item is `NonNullable<…>` where it is used. |
| `@/lib/api`      | `ApiError`, `isApiError`, `errorMessage`; `setupApiClient` (called once in `main.tsx`).                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `@/lib/session`  | `useSession`, `SessionGate`, `useAuth` (sign in / up / out, accept invitation, refresh).                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `@/lib/realtime` | `RealtimeProvider` (mounted in `main.tsx`), `useRealtimeStatus`, `useRealtime().reconnect`.                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `@/lib/query`    | `createQueryClient`, topic matching, `KeelQueryMeta` (`meta.realtime`), the session cache transitions.                                                                                                                                                                                                                                                                                                                                                                                                                                    |

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
    query: { staleTime: Infinity, meta: { realtime: false } },
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
