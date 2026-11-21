// The dashboard's WebSocket: one centrifuge connection per tab to `/api/ws` that turns server
// pushes into TanStack Query refetches. Components never touch it; they call generated hooks and
// stay fresh (docs/go/ARCHITECTURE.md "Realtime"; protocol in internal/adapters/realtime).
//
// - Signed-out tabs open no socket. The connection follows the session query (`GET /api/me`): it
//   opens when there is a user and is replaced when the user or organization changes, because
//   the server picks the channel (`org:<id>`) at connect time. Connect auth is the session
//   cookie; the client sends no token and subscribes to nothing itself.
// - `{"type":"invalidate","topics":[…]}` publications refetch every active query whose key path
//   is under a topic (batched for 50 ms, on top of the server's 100 ms coalescing).
// - Every (re)connect after the first refetches every active query: a gap may hide writes.
// - Close code 4501 "signed out" is terminal: the session query flips to signed out (the gates
//   show sign-in) and the cache is dropped. 4001 "membership changed" and centrifuge's own
//   3xxx codes reconnect (centrifuge does it); any other terminal close is retried here.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Centrifuge, type DisconnectedContext } from "centrifuge";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import type { Me } from "@/gen/api";

import { invalidateAll, invalidateTopics, markSignedOut, meQueryOptions } from "./query";

/** Client-side batching of invalidation topics (web-data.md §9.1). */
const BATCH_MS = 50;
/** Delay before reopening after a terminal close that is not "signed out". */
const RETRY_TERMINAL_MS = 5_000;

type RealtimeContextValue = {
  /** Drops the connection and opens a new one (the server re-reads the session). */
  reconnect: () => void;
};

const RealtimeContext = createContext<RealtimeContextValue>({ reconnect: () => {} });

type Publication = { type?: unknown; topics?: unknown };

/** What the server subscribes a connection to (user, organization); `null` when signed out. */
export function sessionIdentity(me: Me | undefined): string | null {
  return me?.user ? `${me.user.id}/${me.organization?.id ?? ""}` : null;
}

export function RealtimeProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions());
  // Changing user or organization needs a new connection.
  const identity = sessionIdentity(me);

  const [epoch, setEpoch] = useState(0);
  const reconnect = useCallback(() => setEpoch((n) => n + 1), []);
  // The first connect of the tab's life finds the cache fresh (queries fetched as the page
  // mounted); every later one may follow a gap.
  const everConnected = useRef(false);

  useEffect(() => {
    if (!identity) return;
    let closed = false;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let batchTimer: ReturnType<typeof setTimeout> | undefined;
    const pending = new Set<string>();

    const flush = () => {
      batchTimer = undefined;
      const topics = [...pending];
      pending.clear();
      void invalidateTopics(queryClient, topics);
    };

    const { protocol, host } = window.location;
    const centrifuge = new Centrifuge(`${protocol === "https:" ? "wss:" : "ws:"}//${host}/api/ws`, {
      name: "keel-web",
      minReconnectDelay: 500,
      maxReconnectDelay: 10_000,
    });

    centrifuge.on("connected", () => {
      if (closed) return;
      if (everConnected.current) void invalidateAll(queryClient);
      everConnected.current = true;
    });
    centrifuge.on("disconnected", (ctx: DisconnectedContext) => {
      if (closed) return;
      if (ctx.code === 4501) {
        void markSignedOut(queryClient); // identity becomes null; this effect cleans up
        return;
      }
      // A terminal close we did not ask for (centrifuge reconnects on its own for 4001, 3xxx
      // shutdown / server error and transport drops): try again later.
      retryTimer = setTimeout(() => {
        if (!closed) centrifuge.connect();
      }, RETRY_TERMINAL_MS);
    });
    centrifuge.on("publication", (ctx) => {
      const data = ctx.data as Publication | null;
      if (!data || data.type !== "invalidate" || !Array.isArray(data.topics)) return;
      for (const t of data.topics) if (typeof t === "string") pending.add(t);
      batchTimer ??= setTimeout(flush, BATCH_MS);
    });

    centrifuge.connect();

    return () => {
      closed = true;
      clearTimeout(retryTimer);
      clearTimeout(batchTimer);
      centrifuge.removeAllListeners();
      centrifuge.disconnect();
    };
  }, [identity, epoch, queryClient]);

  const value = useMemo(() => ({ reconnect }), [reconnect]);
  return <RealtimeContext.Provider value={value}>{children}</RealtimeContext.Provider>;
}

/** A way to force a new connection. */
export function useRealtime(): RealtimeContextValue {
  return useContext(RealtimeContext);
}
