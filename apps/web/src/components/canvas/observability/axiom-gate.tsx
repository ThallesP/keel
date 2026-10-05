import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useAction, useMutation, useQuery } from "convex/react";
import { useState } from "react";
import { toast } from "sonner";

import { axiomRedirectUri, goToAxiom } from "@/lib/axiom-sign-in";

import { attempt } from "../errors";
import { formatDuration, formatLogTime } from "../format";
import { Spinner } from "../primitives";
import { route } from "./chrome";

/**
 * Sign in with Axiom: one sign-in sets up both the logs and the traces dataset, for every project
 * of the organization (docs/logs.md). Without a sink the Observability page is a gate, the card
 * drawn over a blurred fake stream. A sink connected before traces existed has no traces dataset;
 * the page carries a banner that runs the same sign-in again.
 */

export function AxiomGate() {
  return (
    <div className="relative flex min-h-0 flex-1 items-center justify-center overflow-hidden bg-bg">
      <Backdrop />
      <div className="absolute inset-0 bg-bg/40" />
      <div className="relative w-[380px] rounded-lg border border-line bg-bg p-6 shadow-[0_12px_40px_rgba(11,18,32,0.10)]">
        <AxiomSignIn
          title="Set up Axiom"
          copy="Requests and logs from every service in one stream, searchable, each line one click from its trace. One sign-in covers every project."
        />
      </div>
    </div>
  );
}

/** Connected, but before traces existed: signing in again adds the traces dataset. */
export function TracesBanner() {
  const orgs = useQuery(api.logSinks.pendingOrgs, {});
  if (orgs) {
    return (
      <div className="w-[380px] rounded-lg border border-line p-6">
        <AxiomSignIn title="" copy="" />
      </div>
    );
  }
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-line px-4 py-3">
      <span className="text-xs text-muted-foreground">
        <span className="font-medium text-ink">Traces are off.</span> They need an Axiom dataset of
        their own; sign in again to add it. Logs keep flowing meanwhile.
      </span>
      <SignInButton compact />
    </div>
  );
}

// ── Backdrop ────────────────────────────────────────────────────────────────────────────────

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

const SAMPLE_REQUESTS = [
  ["GET /v1/projects", "api", 12],
  ["POST /v1/deploy", "api", 48],
  ["emails.send", "worker", 340],
  ["GET /", "web", 4],
] as const;

const SAMPLE_BASE = new Date(2026, 0, 1, 14, 2, 7).getTime();

/** A fake stream behind the card, blurred: what the page looks like once connected. */
function Backdrop() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 overflow-hidden px-5 pt-3 font-mono text-2xs leading-[22px] whitespace-pre blur-[3px] select-none"
    >
      {Array.from({ length: 50 }, (_, i) => {
        const time = formatLogTime(SAMPLE_BASE - i * 917);
        if (i % 4 === 1) {
          const [name, svc, ms] = SAMPLE_REQUESTS[(i * 3) % SAMPLE_REQUESTS.length]!;
          return (
            <div key={i} className="text-ink">
              <span className="pr-4 text-faint">{time}</span>
              <span className="inline-block w-24 pr-3 text-[#5c5f9a]">{svc}</span>
              <span className="mr-3 rounded-sm bg-primary-soft px-1 text-primary">req</span>
              {name}
              <span className="pl-3 text-faint">{formatDuration(ms)}</span>
            </div>
          );
        }
        const [svc, text] = SAMPLE[(i * 7) % SAMPLE.length]!;
        return (
          <div key={i} className="text-muted-foreground">
            <span className="pr-4 text-faint">{time}</span>
            <span className="inline-block w-24 pr-3 text-[#5c5f9a]">{svc}</span>
            <span className="mr-3 inline-block w-[22px]" />
            {text}
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
 * place the accent is not Keel's blue. Elsewhere (Settings) the mark is ink.
 */
export function AxiomMark({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={Math.round(size * (11 / 13))} viewBox="0 0 13 11" aria-hidden>
      <path
        d="m12.12 7.27-2.48-4.3a.8.8 0 0 0-.62-.37H7.48c-.36 0-.5-.25-.33-.56L8 .57A.38.38 0 0 0 7.67 0H5.52a.8.8 0 0 0-.62.36L.7 7.6a.8.8 0 0 0 0 .72l1.08 1.86c.18.31.47.32.65 0l.84-1.44c.18-.31.48-.31.66 0l.76 1.32c.11.2.4.36.62.36h4.98a.8.8 0 0 0 .62-.36l1.2-2.07a.8.8 0 0 0 0-.72m-3.34-.2c.18.3.03.56-.33.56H4.58c-.36 0-.5-.26-.33-.57L6.2 3.71c.18-.31.47-.31.65 0z"
        fill="currentColor"
      />
    </svg>
  );
}

/** Starts the sign-in: Axiom's authorize page, then /axiom/callback back to this project. */
function useSignIn() {
  const { projectId: slug } = route.useParams();
  const begin = useAction(api.logSinks.beginAxiomSignIn);
  const [busy, setBusy] = useState(false);
  const signIn = async () => {
    setBusy(true);
    const r = await attempt(begin({ redirectUri: axiomRedirectUri() }));
    if (r) goToAxiom(r.url, { slug });
    else setBusy(false);
  };
  return { busy, signIn };
}

/** In Axiom's brand orange with its mark, like any third-party sign-in button. */
export function SignInButton({ compact = false }: { compact?: boolean }) {
  const { busy, signIn } = useSignIn();
  return (
    <button
      type="button"
      disabled={busy}
      onClick={() => void signIn()}
      className={
        compact
          ? "flex h-7 shrink-0 items-center gap-1.5 rounded-md bg-[#de5820] px-2.5 text-2xs font-medium text-white hover:bg-[#c94d19] disabled:opacity-60"
          : "mt-5 flex h-9 w-full items-center justify-center gap-2 rounded-md bg-[#de5820] text-sm font-medium text-white hover:bg-[#c94d19] disabled:opacity-60"
      }
    >
      {busy ? <Spinner className="text-white" /> : <AxiomMark size={compact ? 12 : 14} />}
      Sign in with Axiom
    </button>
  );
}

/** The card: Sign in with Axiom, or the org picker while a sign-in with several orgs is pending. */
function AxiomSignIn({ title, copy }: { title: string; copy: string }) {
  const orgs = useQuery(api.logSinks.pendingOrgs, {});
  const chooseOrg = useAction(api.logSinks.chooseAxiomOrg);
  const cancel = useMutation(api.logSinks.cancelAxiomSignIn);
  const [busy, setBusy] = useState<string | null>(null);

  const pick = async (orgId: string) => {
    setBusy(orgId);
    const r = await attempt(chooseOrg({ orgId }));
    setBusy(null);
    if (r)
      toast.success(`Every project's logs and traces now go to Axiom · ${r.org} · ${r.dataset}`);
  };

  if (orgs) {
    return (
      <>
        <h2 className="text-md font-semibold text-ink">Pick an Axiom organization</h2>
        <p className="mt-1.5 text-sm text-muted-foreground">
          Logs and traces of every project go to the{" "}
          <span className="font-mono text-xs whitespace-nowrap text-ink">keel-logs</span> and{" "}
          <span className="font-mono text-xs whitespace-nowrap text-ink">keel-traces</span> datasets
          there.
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
          onClick={() => void attempt(cancel({}))}
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
      <SignInButton />
    </>
  );
}
