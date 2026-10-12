// The TanStack Query cache and the rules that keep it fresh: topic matching (realtime and
// Keel-Invalidate), and the session cache transitions (signed out, session changed). No React
// here; src/lib/realtime.tsx, src/lib/api.ts and src/lib/session.tsx build on it.
//
// Query keys come from the generated hooks: key[0] is the resolved request path
// (`/api/environments/abc/nodes`), key[1] the query parameters object when there are any.
// A topic is a path prefix (`/api/environments/abc`); docs/go/ARCHITECTURE.md, "Realtime".
import { type Query, QueryClient } from "@tanstack/react-query";

import { type Me, getMeQueryKey, getMeQueryOptions } from "@/gen/api";

/**
 * `meta` every query may carry. `realtime: false` opts a query out of every invalidation
 * (socket topics, Keel-Invalidate, reconnect): polled and one-shot reads under an invalidated
 * prefix (logs, trace overview, trace detail) set it with their own `refetchInterval` /
 * `staleTime`, so a deploy's stream of writes does not re-run `docker service logs` or an Axiom
 * query (web-data.md §9.2).
 */
export type KeelQueryMeta = Record<string, unknown> & { realtime?: boolean };

declare module "@tanstack/react-query" {
  interface Register {
    queryMeta: KeelQueryMeta;
  }
}

/** A failure that retrying cannot fix: the server answered 4xx. */
function isClientError(error: unknown): boolean {
  const status = (error as { status?: unknown } | null)?.status;
  return typeof status === "number" && status >= 400 && status < 500;
}

/**
 * The app's QueryClient. Realtime pushes keep queries fresh, so the stale time is short and only
 * covers bursts (several components mounting the same key); focus refetches catch anything a
 * dropped socket missed. 4xx answers are not retried (they will not change); network and 5xx
 * failures are, three times. Mutations never retry.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        refetchOnWindowFocus: true,
        retry: (failureCount, error) => !isClientError(error) && failureCount < 3,
      },
      mutations: { retry: false },
    },
  });
}

/** The request path a query key starts with, or undefined for keys that are not API reads. */
export function keyPath(key: readonly unknown[]): string | undefined {
  return typeof key[0] === "string" ? key[0] : undefined;
}

/**
 * Whether a query key falls under a topic: its path is the topic or below it, on a segment
 * boundary (`/api/nodes/abc` covers `/api/nodes/abc/variables`, never `/api/nodes/abcd`).
 */
export function keyMatchesTopic(key: readonly unknown[], topic: string): boolean {
  const path = keyPath(key);
  if (path === undefined || topic === "") return false;
  const prefix = topic.endsWith("/") ? topic : `${topic}/`;
  return path === topic || path.startsWith(prefix);
}

/** Queries that invalidations may refetch (everything but `meta.realtime === false`). */
export function isRealtimeQuery(query: Query): boolean {
  return query.meta?.realtime !== false;
}

/**
 * Refetches the active queries under any of the topics (inactive ones are marked stale and
 * refetch when next used). Resolves once those refetches settled; never rejects.
 */
export function invalidateTopics(
  queryClient: QueryClient,
  topics: readonly string[],
): Promise<void> {
  if (topics.length === 0) return Promise.resolve();
  return queryClient.invalidateQueries(
    {
      predicate: (q) => isRealtimeQuery(q) && topics.some((t) => keyMatchesTopic(q.queryKey, t)),
      refetchType: "active",
    },
    { throwOnError: false },
  );
}

/** Refetches every active realtime query: what a socket reconnect does (a gap may hide writes). */
export function invalidateAll(queryClient: QueryClient): Promise<void> {
  return queryClient.invalidateQueries(
    { predicate: isRealtimeQuery, refetchType: "active" },
    { throwOnError: false },
  );
}

/** Parses a `Keel-Invalidate` header value: comma-separated topics. */
export function parseTopics(header: string | null | undefined): string[] {
  if (!header) return [];
  return header
    .split(",")
    .map((t) => t.trim())
    .filter((t) => t.startsWith("/"));
}

/** `GET /api/me` for a signed-out caller. */
export const SIGNED_OUT: Me = { user: null, organization: null };

const isMeQuery = (q: Query) => keyPath(q.queryKey) === getMeQueryKey()[0];

/**
 * The one definition of the session query, shared by every observer (useSession, the realtime
 * provider) so they agree on its options. A network or server failure is retried for as long as
 * it lasts (backing off to 30 s): like the Convex client, the app waits for the server instead of
 * settling on an error screen.
 */
export function meQueryOptions() {
  return {
    ...getMeQueryOptions(),
    retry: (_count: number, error: unknown) => !isClientError(error),
    retryDelay: (attempt: number) => Math.min(1_000 * 2 ** attempt, 30_000),
  };
}

/**
 * The session ended (sign-out, an expired cookie answering 401, the socket's 4501): the session
 * query says signed out at once, so every gate flips to the sign-in form in place (URL kept), and
 * every other cached answer is dropped (it belonged to that session's organization).
 */
export async function markSignedOut(queryClient: QueryClient): Promise<void> {
  await queryClient.cancelQueries();
  queryClient.setQueryData(getMeQueryKey(), SIGNED_OUT);
  queryClient.removeQueries({ predicate: (q) => !isMeQuery(q) });
}

/**
 * The session changed (signed in, signed up, joined or founded an organization): every cached
 * answer except the session is reset (active ones refetch under the new identity), and the
 * session is refetched in place, so a gate swaps straight from the form to the page without a
 * loading flash. Resolves when the new session is in the cache.
 */
export async function refreshSession(queryClient: QueryClient): Promise<Me> {
  await queryClient.cancelQueries();
  const [me] = await Promise.all([
    queryClient.fetchQuery({ ...meQueryOptions(), staleTime: 0 }),
    queryClient.resetQueries({ predicate: (q) => !isMeQuery(q) }, { throwOnError: false }),
  ]);
  return me;
}

/** The cached session, if loaded. */
export function cachedSession(queryClient: QueryClient): Me | undefined {
  return queryClient.getQueryData<Me>(getMeQueryKey());
}
