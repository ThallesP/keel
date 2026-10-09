import { cn } from "@my-better-t-app/ui/lib/utils";
import { useMemo, useState } from "react";

import { type LogReplica, type LogTail, type ServiceLogLine, useTailNodeLogs } from "@/api/gen";
import { errorMessage } from "@/lib/api";

import { formatLogTime } from "../../format";
import type { InfraNode } from "../../types";
import { FollowingBadge, LogStream } from "../log-stream";
import { PanelMain } from "../panel-frame";

const TAIL = 300;

/**
 * Service logs are the one thing polled, not pushed: they never touch a table, so no write
 * invalidates them (`meta.realtime: false`). One call per poll, no retries: a failure shows its
 * message until the next poll succeeds.
 */
function useServiceLogs(nodeId: string, enabled: boolean) {
  const { data, error } = useTailNodeLogs(
    { path: { id: nodeId }, query: { tail: TAIL } },
    {
      query: {
        enabled,
        refetchInterval: 3_000,
        staleTime: 0,
        retry: false,
        meta: { realtime: false },
      },
    },
  );
  return { data: data ?? null, error: error ? errorMessage(error) : null };
}

/** One muted hue per replica so interleaved lines read apart. Never the accent. */
const REPLICA_TONES = [
  "text-[#4f7a5c]",
  "text-[#8a5f3c]",
  "text-[#5c5f9a]",
  "text-[#8a4f6e]",
  "text-[#3c7a8a]",
  "text-[#8a7a3c]",
] as const;

/** task id → `r<slot>` label. Tasks Swarm has already pruned fall back to a short id. */
function replicaLabels(replicas: LogReplica[]) {
  const bySlot = new Map<number, number>();
  const labels = new Map<string, { text: string; tone: string }>();
  for (const r of replicas) {
    if (!bySlot.has(r.slot)) bySlot.set(r.slot, bySlot.size);
    const idx = bySlot.get(r.slot) ?? 0;
    labels.set(r.task, {
      text: `r${r.slot}`,
      tone: REPLICA_TONES[idx % REPLICA_TONES.length] ?? "text-faint",
    });
  }
  return (task: string) => labels.get(task) ?? { text: task.slice(0, 5), tone: "text-faint" };
}

function ReplicaTag({ text, tone }: { text: string; tone: string }) {
  return <span className={cn("inline-block w-9 pr-3 text-right", tone)}>{text}</span>;
}

function rawText(l: ServiceLogLine) {
  const stamp = l.time ? new Date(l.time).toISOString() : "";
  return `${stamp} ${l.task} [${l.stream}] ${l.text}`;
}

type BodyProps = {
  node: InfraNode;
  data: LogTail | null;
  error: string | null;
  raw: boolean;
  following: boolean;
};

function LogBody({ node, data, error, raw, following }: BodyProps) {
  const replicas = data?.replicas;
  const label = useMemo(() => replicaLabels(replicas ?? []), [replicas]);
  if (error) return <p className="text-xs text-danger">{error}</p>;
  if (node.type === "volume") return <p className="text-xs text-faint">Volumes have no logs.</p>;
  if (data === null) {
    return (
      <p className="text-xs text-faint">
        {node.data.status === "pending" ? "Not deployed yet." : "Loading…"}
      </p>
    );
  }
  const { lines } = data;
  if (lines.length === 0) return <p className="text-xs text-faint">No log output.</p>;
  const tagged = (replicas?.length ?? 0) > 1 || lines.some((l) => l.task !== "");
  return (
    <LogStream
      following={following}
      lines={lines.map((l, i) => ({
        key: `${l.time}:${i}`,
        time: raw || !l.time ? undefined : formatLogTime(l.time),
        tag: raw || !tagged ? undefined : <ReplicaTag {...label(l.task)} />,
        text: raw ? rawText(l) : l.text,
        tone: "muted",
      }))}
    />
  );
}

export function LogsTab({ node }: { node: InfraNode }) {
  const deployable = node.type !== "volume";
  const [raw, setRaw] = useState(false);
  const [following, setFollowing] = useState(true);
  const { data, error } = useServiceLogs(node.id, deployable && node.data.status !== "pending");
  const running = node.type !== "volume" ? node.data.running : 0;

  return (
    <PanelMain className="gap-1">
      <div className="flex items-center justify-between pb-1.5 text-2xs">
        <span className="flex items-center gap-2.5 font-mono">
          <span className="font-medium text-ink">svc-{node.id.slice(0, 8)}</span>
          <span className="text-faint">
            {running} {running === 1 ? "replica" : "replicas"} · last {TAIL} lines
            {data?.source === "axiom" && " · via Axiom"}
          </span>
        </span>
        <span className="flex items-center gap-3.5">
          <button
            type="button"
            aria-pressed={raw}
            onClick={() => setRaw((v) => !v)}
            className={cn("hover:text-ink", raw ? "text-ink" : "text-muted-foreground")}
          >
            Raw
          </button>
          <button type="button" onClick={() => setFollowing((v) => !v)}>
            <FollowingBadge active={following} />
          </button>
        </span>
      </div>
      <LogBody node={node} data={data} error={error} raw={raw} following={following} />
    </PanelMain>
  );
}
