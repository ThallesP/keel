import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import type { ProjectTail } from "@my-better-t-app/backend/convex/logs";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { getRouteApi } from "@tanstack/react-router";
import { useAction, useMutation, useQuery } from "convex/react";
import { Search } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

import { axiomRedirectUri, goToAxiom } from "@/lib/axiom-sign-in";

import { FollowingBadge, LogStream } from "./bottom-panel/log-stream";
import { useEnvironment } from "./environment";
import { attempt, errorMessage } from "./errors";
import { formatLogTime } from "./format";
import { Spinner } from "./primitives";

/**
 * The rail's Logs page: every service of the environment in one stream, searchable. Needs a log
 * store, so without an Axiom sink it is a gate with Sign in with Axiom (docs/logs.md). The
 * per-service Logs tab in the bottom panel keeps working on Docker either way.
 */

const route = getRouteApi("/_auth/p/$projectId");
const POLL_MS = 3000;
const TAIL = 300;

export function LogsPage() {
  const { projectId } = useEnvironment();
  const sink = useQuery(api.logSinks.get, { projectId });
  if (sink === undefined) {
    return (
      <div className="flex h-full items-center justify-center bg-bg">
        <Spinner />
      </div>
    );
  }
  if (sink?.kind !== "axiom") return <AxiomGate />;
  return <ProjectLogs sink={sink} />;
}

// ── Gate ────────────────────────────────────────────────────────────────────────────────────

const SAMPLE = [
  ["api", "GET /v1/projects 200 12ms"],
  ["worker", "job 4812 done in 340ms"],
  ["api", "POST /v1/deploy 202 48ms"],
  ["postgres", "checkpoint complete: wrote 118 buffers (0.7%)"],
  ["web", "ready on :3000"],
  ["api", "GET /healthz 200 1ms"],
  ["redis", "DB saved on disk"],
  ["worker", "picked job 4813 (emails.send)"],
  ["api", "GET /v1/nodes?env=prod 200 9ms"],
  ["worker", "retrying job 4790 in 30s: ECONNRESET"],
  ["web", "GET / 200 4ms"],
  ["api", "PATCH /v1/nodes/a81c 200 21ms"],
] as const;

/** A fake stream behind the card, blurred: what the page looks like once connected. */
function Backdrop() {
  const base = new Date(2026, 0, 1, 14, 2, 7).getTime();
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 overflow-hidden px-5 pt-14 font-mono text-2xs leading-[19px] whitespace-pre blur-[3px] select-none"
    >
      {Array.from({ length: 60 }, (_, i) => {
        const [svc, text] = SAMPLE[(i * 7) % SAMPLE.length]!;
        return (
          <div key={i} className="text-muted-foreground">
            <span className="pr-4 text-faint">{formatLogTime(base + i * 917)}</span>
            <span className="inline-block w-24 pr-3 text-[#5c5f9a]">{svc}</span>
            {text}
          </div>
        );
      })}
    </div>
  );
}

/**
 * Axiom's logo mark (axiom.co). The sign-in button wears Axiom's brand orange (`#de5820`, its
 * light-theme value) with this mark in white, like any third-party sign-in button; it is the one
 * place the accent is not Keel's blue.
 */
function AxiomMark({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={Math.round(size * (11 / 13))} viewBox="0 0 13 11" aria-hidden>
      <path
        d="m12.12 7.27-2.48-4.3a.8.8 0 0 0-.62-.37H7.48c-.36 0-.5-.25-.33-.56L8 .57A.38.38 0 0 0 7.67 0H5.52a.8.8 0 0 0-.62.36L.7 7.6a.8.8 0 0 0 0 .72l1.08 1.86c.18.31.47.32.65 0l.84-1.44c.18-.31.48-.31.66 0l.76 1.32c.11.2.4.36.62.36h4.98a.8.8 0 0 0 .62-.36l1.2-2.07a.8.8 0 0 0 0-.72m-3.34-.2c.18.3.03.56-.33.56H4.58c-.36 0-.5-.26-.33-.57L6.2 3.71c.18-.31.47-.31.65 0z"
        fill="currentColor"
      />
    </svg>
  );
}

function AxiomGate() {
  const { projectId } = useEnvironment();
  const { projectId: slug } = route.useParams();
  const orgs = useQuery(api.logSinks.pendingOrgs, { projectId });
  const begin = useAction(api.logSinks.beginAxiomSignIn);
  const chooseOrg = useAction(api.logSinks.chooseAxiomOrg);
  const cancel = useMutation(api.logSinks.cancelAxiomSignIn);
  const [busy, setBusy] = useState<string | null>(null);

  const signIn = async () => {
    setBusy("signin");
    const r = await attempt(begin({ projectId, redirectUri: axiomRedirectUri() }));
    if (r) goToAxiom(r.url, slug);
    else setBusy(null);
  };

  const pick = async (orgId: string) => {
    setBusy(orgId);
    const r = await attempt(chooseOrg({ projectId, orgId }));
    setBusy(null);
    if (r) toast.success(`Logs now stream to Axiom · ${r.org} · ${r.dataset}`);
  };

  return (
    <div className="relative flex h-full items-center justify-center overflow-hidden bg-bg">
      <Backdrop />
      <div className="absolute inset-0 bg-bg/40" />
      <div className="relative w-[380px] rounded-lg border border-line bg-bg p-6 shadow-[0_12px_40px_rgba(11,18,32,0.10)]">
        {orgs ? (
          <>
            <h2 className="text-md font-semibold text-ink">Pick an Axiom organization</h2>
            <p className="mt-1.5 text-sm text-muted-foreground">
              Keel creates the <span className="font-mono text-xs text-ink">keel-{slug}</span>{" "}
              dataset there.
            </p>
            <div className="mt-4 flex flex-col gap-1.5">
              {orgs.map((o) => (
                <button
                  key={o.id}
                  type="button"
                  disabled={busy !== null}
                  onClick={() => void pick(o.id)}
                  className="flex h-9 items-center justify-between rounded-md border border-line px-3 text-sm text-ink hover:bg-surface-2 disabled:opacity-60"
                >
                  {o.name}
                  {busy === o.id && <Spinner />}
                </button>
              ))}
            </div>
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => void attempt(cancel({ projectId }))}
              className="mt-3 text-xs text-muted-foreground hover:text-ink"
            >
              Cancel
            </button>
          </>
        ) : (
          <>
            <h2 className="text-md font-semibold text-ink">Set up Axiom</h2>
            <p className="mt-1.5 text-sm text-muted-foreground">
              Logs from every service in one searchable stream, kept after containers are gone.
            </p>
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => void signIn()}
              className="mt-5 flex h-9 w-full items-center justify-center gap-2 rounded-md bg-[#de5820] text-sm font-medium text-white hover:bg-[#c94d19] disabled:opacity-60"
            >
              {busy === "signin" ? <Spinner className="text-white" /> : <AxiomMark />}
              Sign in with Axiom
            </button>
          </>
        )}
      </div>
    </div>
  );
}

// ── Connected ───────────────────────────────────────────────────────────────────────────────

function useDebounced<T>(value: T, ms: number) {
  const [out, setOut] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setOut(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return out;
}

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

type Sink = { dataset: string; org: string | null };

function ProjectLogs({ sink }: { sink: Sink }) {
  const { projectId, environmentId } = useEnvironment();
  const nodes = useQuery(api.nodes.list, { environmentId });
  const disconnect = useMutation(api.logSinks.disconnect);
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
      <div className="flex h-11 shrink-0 items-center justify-between gap-4 border-b border-line px-5">
        <span className="flex min-w-0 items-center gap-2.5 text-2xs">
          <span className="text-sm font-medium text-ink">Logs</span>
          <span className="truncate font-mono text-faint">
            via Axiom · {sink.org ? `${sink.org} · ` : ""}
            {sink.dataset} · last {TAIL} lines
          </span>
        </span>
        <span className="flex items-center gap-3.5 text-2xs">
          <label className="flex h-7 w-64 items-center gap-2 rounded-md border border-line px-2 focus-within:border-primary">
            <Search size={12} strokeWidth={1.6} className="text-faint" aria-hidden />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search messages"
              spellCheck={false}
              aria-label="Search messages"
              className="min-w-0 flex-1 bg-transparent font-mono text-2xs text-ink outline-none placeholder:font-sans placeholder:text-faint"
            />
          </label>
          <button type="button" onClick={() => setFollowing((v) => !v)}>
            <FollowingBadge active={following} />
          </button>
          <button
            type="button"
            onClick={() =>
              void attempt(disconnect({ projectId })).then(() => toast("Logs back to Docker"))
            }
            className="text-muted-foreground hover:text-danger"
          >
            Disconnect
          </button>
        </span>
      </div>
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
