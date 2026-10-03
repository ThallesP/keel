import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { getRouteApi } from "@tanstack/react-router";
import { useMutation, useQuery } from "convex/react";
import { Search } from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { toast } from "sonner";

import { useEnvironment } from "../environment";
import { attempt } from "../errors";

// Pieces the Observability page's views share: the 44px header, the search field, Disconnect,
// service names.

export const route = getRouteApi("/_auth/p/$projectId");

/** What logSinks.get returns for an Axiom sink; never the token. */
export type Sink = { domain: string; dataset: string; traces: string | null; org: string | null };

/** "via Axiom · org · keel-x + keel-x-traces" */
export const viaAxiom = (sink: Sink) =>
  `via Axiom · ${sink.org ? `${sink.org} · ` : ""}${sink.dataset}${sink.traces ? ` + ${sink.traces}` : ""}`;

/** Title and mono meta left, the view's controls right. */
export function PageHeader({ meta, children }: { meta?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex h-11 shrink-0 items-center justify-between gap-4 border-b border-line px-5">
      <span className="flex min-w-0 items-center gap-2.5">
        <span className="text-sm font-medium text-ink">Observability</span>
        {meta && <span className="truncate font-mono text-2xs text-faint">{meta}</span>}
      </span>
      {children && <span className="flex shrink-0 items-center gap-3.5 text-2xs">{children}</span>}
    </div>
  );
}

export function SearchField({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
}) {
  return (
    <label className="flex h-7 w-64 items-center gap-2 rounded-md border border-line px-2 focus-within:border-primary">
      <Search size={12} strokeWidth={1.6} className="text-faint" aria-hidden />
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        aria-label={placeholder}
        className="min-w-0 flex-1 bg-transparent font-mono text-2xs text-ink outline-none placeholder:font-sans placeholder:text-faint"
      />
    </label>
  );
}

/** Back to Docker logs and no traces. Shipped data stays in Axiom. */
export function DisconnectButton() {
  const { projectId } = useEnvironment();
  const disconnect = useMutation(api.logSinks.disconnect);
  return (
    <button
      type="button"
      onClick={() =>
        void attempt(disconnect({ projectId })).then(() => toast("Axiom disconnected"))
      }
      className="text-muted-foreground hover:text-danger"
    >
      Disconnect
    </button>
  );
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

export type ServiceLabel = { text: string; tone: string };

/**
 * Names and tones of the environment's services. Log lines name a service by node id; spans by
 * their OTel `service.name`, which matches a node when the app sets it to the node's name, and
 * then wears that node's tone.
 */
export function useServices() {
  const { environmentId } = useEnvironment();
  const nodes = useQuery(api.nodes.list, { environmentId });
  return useMemo(() => {
    const byId = new Map<string, ServiceLabel>();
    const byName = new Map<string, ServiceLabel>();
    (nodes ?? []).forEach((n, i) => {
      const label = { text: n.name, tone: SERVICE_TONES[i % SERVICE_TONES.length] ?? "text-faint" };
      byId.set(n.id, label);
      byName.set(n.name, label);
    });
    return {
      /** A log line's service, by node id. */
      ofLine: (id: string): ServiceLabel =>
        byId.get(id) ?? { text: id.slice(0, 8), tone: "text-faint" },
      /** A span's service, by OTel service.name. */
      ofSpan: (name: string): ServiceLabel =>
        byName.get(name) ?? { text: name, tone: "text-faint" },
    };
  }, [nodes]);
}
