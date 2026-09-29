import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { LogLine, Replica, Tail } from "@my-better-t-app/backend/convex/logs";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useAction } from "convex/react";
import { useEffect, useMemo, useState } from "react";

import { errorMessage } from "../../errors";
import { formatLogTime } from "../../format";
import { asNodeId } from "../../mapping";
import type { InfraNode } from "../../types";
import { FollowingBadge, LogStream } from "../log-stream";
import { PanelMain } from "../panel-frame";

const POLL_MS = 3000;
const TAIL = 300;

/** Docker logs are the one thing polled, not subscribed: they never touch a table. */
function useServiceLogs(nodeId: string, enabled: boolean) {
  const tail = useAction(api.logs.tail);
  const [data, setData] = useState<Tail | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    const load = async () => {
      try {
        const next = await tail({ nodeId: asNodeId(nodeId), tail: TAIL });
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
  }, [nodeId, enabled, tail]);
  return { data, error };
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
function replicaLabels(replicas: Replica[]) {
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

function rawText(l: LogLine) {
  const stamp = l.time ? new Date(l.time).toISOString() : "";
  return `${stamp} ${l.task} [${l.stream}] ${l.text}`;
}

type BodyProps = {
  node: InfraNode;
  data: Tail | null;
  error: string | null;
  raw: boolean;
  following: boolean;
};

function LogBody({ node, data, error, raw, following }: BodyProps) {
  const label = useMemo(() => replicaLabels(data?.replicas ?? []), [data?.replicas]);
  if (error) return <p className="text-xs text-danger">{error}</p>;
  if (node.type === "volume") return <p className="text-xs text-faint">Volumes have no logs.</p>;
  if (data === null) {
    return (
      <p className="text-xs text-faint">
        {node.data.status === "pending" ? "Not deployed yet." : "Loading…"}
      </p>
    );
  }
  if (data.lines.length === 0) return <p className="text-xs text-faint">No log output.</p>;
  const tagged = data.replicas.length > 1 || data.lines.some((l) => l.task !== "");
  return (
    <LogStream
      following={following}
      lines={data.lines.map((l, i) => ({
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
