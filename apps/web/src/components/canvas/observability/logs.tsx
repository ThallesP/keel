import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import type { ProjectTail } from "@my-better-t-app/backend/convex/logs";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useAction, useQuery } from "convex/react";
import { useEffect, useMemo, useState } from "react";

import { FollowingBadge, LogStream } from "../bottom-panel/log-stream";
import { useEnvironment } from "../environment";
import { errorMessage } from "../errors";
import { formatLogTime } from "../format";
import { useDebounced } from "../use-debounced";
import { DisconnectButton, PageHeader, SearchField, type Sink, viaAxiom } from "./chrome";

/** Logs tab: every service of the environment in one stream, searchable. Axiom only. */

const POLL_MS = 3000;
const TAIL = 300;

/** Polled like the per-service tab; the previous result stays up while a new search loads. */
function useRecent(environmentId: Id<"environments">, search: string) {
  const recent = useAction(api.logs.recent);
  const [data, setData] = useState<ProjectTail | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const next = await recent({ environmentId, search, tail: TAIL });
        if (!cancelled) {
          setData(next);
          setError(null);
        }
      } catch (err) {
        if (!cancelled) setError(errorMessage(err));
      }
    };
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [environmentId, search, recent]);
  return { data, error };
}

/** One muted hue per service so interleaved lines read apart. Never the accent. */
const SERVICE_TONES = [
  "text-[#5c5f9a]",
  "text-[#4f7a5c]",
  "text-[#8a5f3c]",
  "text-[#8a4f6e]",
  "text-[#3c7a8a]",
  "text-[#8a7a3c]",
] as const;

export function ProjectLogs({ sink }: { sink: Sink }) {
  const { environmentId } = useEnvironment();
  const nodes = useQuery(api.nodes.list, { environmentId });
  const [search, setSearch] = useState("");
  const [following, setFollowing] = useState(true);
  const { data, error } = useRecent(environmentId, useDebounced(search.trim(), 300));

  const service = useMemo(() => {
    const byId = new Map<string, { text: string; tone: string }>();
    (nodes ?? []).forEach((n, i) =>
      byId.set(n.id, {
        text: n.name,
        tone: SERVICE_TONES[i % SERVICE_TONES.length] ?? "text-faint",
      }),
    );
    return (id: string) => byId.get(id) ?? { text: id.slice(0, 8), tone: "text-faint" };
  }, [nodes]);

  return (
    <div className="flex h-full flex-col bg-bg">
      <PageHeader tab="logs" meta={`${viaAxiom(sink, sink.dataset)} · last ${TAIL} lines`}>
        <SearchField value={search} onChange={setSearch} placeholder="Search messages" />
        <button type="button" onClick={() => setFollowing((v) => !v)}>
          <FollowingBadge active={following} />
        </button>
        <DisconnectButton />
      </PageHeader>
      <div className="flex min-h-0 flex-1 flex-col px-5 py-3">
        {error ? (
          <p className="text-xs text-danger">{error}</p>
        ) : data === null ? (
          <p className="text-xs text-faint">Loading…</p>
        ) : data.lines.length === 0 ? (
          <p className="text-xs text-faint">
            {search.trim() ? "No lines match." : "No log output yet."}
          </p>
        ) : (
          <LogStream
            following={following}
            lines={data.lines.map((l, i) => {
              const s = service(l.serviceId);
              return {
                key: `${l.time}:${i}`,
                time: l.time ? formatLogTime(l.time) : undefined,
                tag: (
                  <span className={cn("inline-block w-28 truncate pr-3 align-bottom", s.tone)}>
                    {s.text}
                  </span>
                ),
                text: l.text,
                tone: l.stream === "stderr" ? "warning" : "muted",
              };
            })}
          />
        )}
      </div>
    </div>
  );
}
