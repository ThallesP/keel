import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useAction, useMutation, useQuery } from "convex/react";
import { useState, type ReactNode } from "react";
import { toast } from "sonner";

import { axiomRedirectUri, goToAxiom } from "@/lib/axiom-sign-in";

import { useEnvironment } from "../environment";
import { attempt } from "../errors";
import { formatDuration, formatLogTime } from "../format";
import { Spinner } from "../primitives";
import { route, type Tab } from "./chrome";

/**
 * Sign in with Axiom, drawn over a blurred fake of the tab it unlocks. One sign-in sets up both
 * the logs and the traces dataset (docs/logs.md). A project connected before traces existed has
 * no traces dataset; its Traces tab shows the same card as a reconnect.
 */

export function AxiomGate({ tab }: { tab: Tab }) {
  return (
    <GateFrame backdrop={tab === "logs" ? <LogsBackdrop /> : <TracesBackdrop />}>
      <AxiomSignIn
        tab={tab}
        title="Set up Axiom"
        copy="Traces and logs from every service in one place, searchable, kept after containers are gone."
      />
    </GateFrame>
  );
}

/** Connected, but before traces existed: signing in again adds the traces dataset. */
export function TracesReconnect() {
  return (
    <GateFrame backdrop={<TracesBackdrop />}>
      <AxiomSignIn
        tab="traces"
        title="Turn on traces"
        copy="Traces need an Axiom dataset of their own. Sign in again to add it; logs keep flowing meanwhile."
      />
    </GateFrame>
  );
}

function GateFrame({ backdrop, children }: { backdrop: ReactNode; children: ReactNode }) {
  return (
    <div className="relative flex min-h-0 flex-1 items-center justify-center overflow-hidden bg-bg">
      {backdrop}
      <div className="absolute inset-0 bg-bg/40" />
      <div className="relative w-[380px] rounded-lg border border-line bg-bg p-6 shadow-[0_12px_40px_rgba(11,18,32,0.10)]">
        {children}
      </div>
    </div>
  );
}

// ── Backdrops ───────────────────────────────────────────────────────────────────────────────

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

const SAMPLE_BASE = new Date(2026, 0, 1, 14, 2, 7).getTime();

/** A fake stream behind the card, blurred: what the Logs tab looks like once connected. */
function LogsBackdrop() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 overflow-hidden px-5 pt-3 font-mono text-2xs leading-[19px] whitespace-pre blur-[3px] select-none"
    >
      {Array.from({ length: 60 }, (_, i) => {
        const [svc, text] = SAMPLE[(i * 7) % SAMPLE.length]!;
        return (
          <div key={i} className="text-muted-foreground">
            <span className="pr-4 text-faint">{formatLogTime(SAMPLE_BASE + i * 917)}</span>
            <span className="inline-block w-24 pr-3 text-[#5c5f9a]">{svc}</span>
            {text}
          </div>
        );
      })}
    </div>
  );
}

const SAMPLE_TRACES = [
  ["GET /v1/projects", "api", 12],
  ["POST /v1/deploy", "api", 48],
  ["emails.send", "worker", 340],
  ["GET /", "web", 4],
  ["GET /healthz", "api", 1],
  ["PATCH /v1/nodes/:id", "api", 21],
  ["GET /v1/nodes", "api", 9],
  ["billing.sync", "worker", 180],
] as const;

/** Fake trace rows behind the card, blurred: what the Traces tab looks like once connected. */
function TracesBackdrop() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 overflow-hidden px-5 pt-4 text-xs blur-[3px] select-none"
    >
      {Array.from({ length: 40 }, (_, i) => {
        const [name, svc, ms] = SAMPLE_TRACES[(i * 5) % SAMPLE_TRACES.length]!;
        return (
          <div key={i} className="flex h-8 items-center gap-4 border-b border-line">
            <span className="w-20 font-mono text-2xs text-faint">
              {formatLogTime(SAMPLE_BASE + i * 1733).slice(0, 8)}
            </span>
            <span className="w-56 text-ink">{name}</span>
            <span className="w-24 text-muted-foreground">{svc}</span>
            <span className="h-1.5 rounded-full bg-primary/30" style={{ width: ms / 2 + 8 }} />
            <span className="font-mono text-2xs text-faint">{formatDuration(ms)}</span>
          </div>
        );
      })}
    </div>
  );
}

// ── Sign in ─────────────────────────────────────────────────────────────────────────────────

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

/** The card: Sign in with Axiom, or the org picker while a sign-in with several orgs is pending. */
function AxiomSignIn({ tab, title, copy }: { tab: Tab; title: string; copy: string }) {
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
    if (r) goToAxiom(r.url, { slug, view: tab });
    else setBusy(null);
  };

  const pick = async (orgId: string) => {
    setBusy(orgId);
    const r = await attempt(chooseOrg({ projectId, orgId }));
    setBusy(null);
    if (r) toast.success(`Logs and traces now go to Axiom · ${r.org} · ${r.dataset}`);
  };

  if (orgs) {
    return (
      <>
        <h2 className="text-md font-semibold text-ink">Pick an Axiom organization</h2>
        <p className="mt-1.5 text-sm text-muted-foreground">
          Keel creates the <span className="font-mono text-xs text-ink">keel-{slug}</span> and{" "}
          <span className="font-mono text-xs text-ink">keel-{slug}-traces</span> datasets there.
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
    );
  }
  return (
    <>
      <h2 className="text-md font-semibold text-ink">{title}</h2>
      <p className="mt-1.5 text-sm text-muted-foreground">{copy}</p>
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
  );
}
