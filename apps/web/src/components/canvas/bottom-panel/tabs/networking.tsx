import { cn } from "@my-better-t-app/ui/lib/utils";
import { Plus, X } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { useGetControlPlane } from "@/api/gen";

import { type ExposeOptions, useCanvasActions } from "../../actions";
import { EndpointAddress } from "../../endpoint-address";
import type { Endpoint, RuntimeNode } from "../../types";

const PROTOCOLS = [
  { id: "http", label: "HTTPS" },
  { id: "tcp", label: "TCP" },
  { id: "udp", label: "UDP" },
] as const;

type Protocol = (typeof PROTOCOLS)[number]["id"];

/** The API's own sentence for a port it refuses (`MsgPortRange`). */
const PORT_RANGE = "Port must be 1–65535";

const chipClass =
  "inline-flex h-4 w-11 shrink-0 items-center justify-center rounded-sm bg-surface-2 font-sans text-[10px] font-semibold tracking-[0.06em] text-muted-foreground";
const inputClass =
  "h-7 min-w-0 rounded-sm border border-line bg-bg px-2 font-mono text-xs text-ink outline-none placeholder:font-sans placeholder:text-faint focus:border-primary";
const buttonClass =
  "flex h-6 shrink-0 items-center gap-1.5 rounded-sm border border-line bg-bg px-2 text-2xs text-ink hover:border-primary hover:text-primary disabled:opacity-50";

/**
 * How the internet reaches this node: keel-proxy on the control plane, https on 80/443 by domain,
 * raw tcp/udp on a port of the control plane (docs/networking.md). Changes apply at once, like
 * the toolbar's Expose; they never wait for Ship.
 */
export function NetworkingSection({ node }: { node: RuntimeNode }) {
  // The control plane's public IP is fixed for the life of the process: read once.
  const ip = useGetControlPlane({
    query: { staleTime: Infinity, meta: { realtime: false } },
  }).data?.publicIp;
  const [adding, setAdding] = useState(false);
  const { endpoints } = node.data;
  const raw = endpoints
    .filter((e) => e.protocol !== "http")
    .map((e) => `${e.publicPort}/${e.protocol}`);
  const where = ip ? `the control plane (${ip})` : "the control plane";
  const note =
    endpoints.length === 0
      ? "Private: only nodes on this canvas reach it."
      : `Served by ${where}. Its firewall or router must let in ${[
          ...(endpoints.some((e) => e.protocol === "http") ? ["80 and 443"] : []),
          ...raw,
        ].join(", ")}.`;

  return (
    <section className="shrink-0 border-b border-line">
      <div className="flex items-center gap-6 px-5 py-3">
        <div className="min-w-0 flex-1 pl-2">
          <h3 className="text-sm font-medium text-ink">Public networking</h3>
          <p className="mt-0.5 text-xs text-muted-foreground">{note}</p>
        </div>
        {!adding && (
          <button type="button" className={buttonClass} onClick={() => setAdding(true)}>
            <Plus size={11} strokeWidth={1.6} aria-hidden />
            Add endpoint
          </button>
        )}
      </div>
      {endpoints.map((e) => (
        <EndpointRow key={`${e.protocol}:${e.address}`} nodeId={node.id} endpoint={e} />
      ))}
      {adding && <AddEndpoint node={node} ip={ip ?? undefined} onDone={() => setAdding(false)} />}
    </section>
  );
}

const stateText = { live: "live", starting: "getting a certificate…", failed: "failed" } as const;

function EndpointRow({ nodeId, endpoint: e }: { nodeId: string; endpoint: Endpoint }) {
  const actions = useCanvasActions();
  const label = PROTOCOLS.find((p) => p.id === e.protocol)!.label;
  return (
    <div className="flex h-8 items-center gap-3 border-t border-line/60 px-5 font-mono text-xs">
      <span className="flex min-w-0 flex-1 items-center gap-3 pl-2">
        <span className={chipClass}>{label}</span>
        <EndpointAddress endpoint={e} />
        <span className="shrink-0 text-faint">→ :{e.port}</span>
      </span>
      <span
        className={cn(
          "max-w-1/2 truncate font-sans text-2xs",
          e.state === "failed" ? "text-danger" : e.state === "live" ? "text-success" : "text-faint",
        )}
        title={e.error}
      >
        {e.state === "failed"
          ? (e.error ?? "failed")
          : e.protocol === "http"
            ? stateText[e.state]
            : e.state}
      </span>
      <button
        type="button"
        aria-label={`Close ${e.address}`}
        title="Close this endpoint"
        onClick={() => void actions.unexpose(nodeId, e)}
        className="flex size-5 shrink-0 items-center justify-center rounded-sm text-faint hover:bg-surface-2 hover:text-danger"
      >
        <X size={11} strokeWidth={1.8} aria-hidden />
      </button>
    </div>
  );
}

/**
 * One row: protocol, then a domain (https) or a public port (tcp/udp), then the container port.
 * Empty fields take the defaults: a generated sslip.io domain, the container port as the public
 * one when it is free, the node's own port. ↵ adds, Esc cancels.
 */
function AddEndpoint({ node, ip, onDone }: { node: RuntimeNode; ip?: string; onDone: () => void }) {
  const actions = useCanvasActions();
  const [protocol, setProtocol] = useState<Protocol>(node.type === "service" ? "http" : "tcp");
  const [domain, setDomain] = useState("");
  const [publicPort, setPublicPort] = useState("");
  const [port, setPort] = useState(node.data.port ? String(node.data.port) : "");
  const [busy, setBusy] = useState(false);

  // Empty = the default (omitted). Text that is not a number never reaches the API: JSON would
  // carry NaN (or Infinity) as null, which reads as "use the default" instead of the error
  // Convex gave. Fractions and out-of-range numbers are sent; the server refuses them.
  const num = (s: string) => (s.trim() === "" ? undefined : Number(s));
  const bad = (n: number | undefined) => n !== undefined && !Number.isFinite(n);
  const submit = async () => {
    const options: ExposeOptions = { protocol, port: num(port) };
    if (protocol === "http") options.domain = domain.trim() || undefined;
    else options.publicPort = num(publicPort);
    if (bad(options.port) || bad(options.publicPort)) {
      toast.error(PORT_RANGE);
      return;
    }
    setBusy(true);
    const ok = await actions.expose(node.id, options);
    setBusy(false);
    if (ok) onDone();
  };
  const keys = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") void submit();
    if (e.key === "Escape") onDone();
  };

  return (
    <div className="border-t border-line/60 px-5 py-2">
      <div className="flex items-center gap-2 pl-2">
        <div className="flex shrink-0 rounded-sm border border-line p-0.5" role="radiogroup">
          {PROTOCOLS.map((p) => (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={protocol === p.id}
              onClick={() => setProtocol(p.id)}
              className={cn(
                "h-5 rounded-[3px] px-2 text-[10px] font-semibold tracking-[0.06em]",
                protocol === p.id
                  ? "bg-surface-2 text-ink"
                  : "text-muted-foreground hover:text-ink",
              )}
            >
              {p.label}
            </button>
          ))}
        </div>
        {protocol === "http" ? (
          <input
            autoFocus
            className={cn(inputClass, "flex-1")}
            placeholder="app.example.com (empty: a generated domain)"
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            onKeyDown={keys}
            aria-label="Domain"
          />
        ) : (
          <input
            autoFocus
            className={cn(inputClass, "w-36")}
            placeholder="public port (empty: same)"
            inputMode="numeric"
            value={publicPort}
            onChange={(e) => setPublicPort(e.target.value)}
            onKeyDown={keys}
            aria-label="Public port"
          />
        )}
        <span className="shrink-0 font-mono text-xs text-faint">→ :</span>
        <input
          className={cn(inputClass, "w-20")}
          placeholder="port"
          inputMode="numeric"
          value={port}
          onChange={(e) => setPort(e.target.value)}
          onKeyDown={keys}
          aria-label="Container port"
        />
        {protocol !== "http" && <span className="flex-1" />}
        <button type="button" className={buttonClass} disabled={busy} onClick={() => void submit()}>
          Add
        </button>
        <button
          type="button"
          className="text-2xs text-muted-foreground hover:text-ink"
          onClick={onDone}
        >
          Cancel
        </button>
      </div>
      {protocol === "http" && domain.trim() !== "" && (
        <p className="mt-1.5 pl-2 text-2xs text-muted-foreground">
          Point <span className="font-mono text-ink">{domain.trim()}</span> at{" "}
          <span className="font-mono text-ink">{ip ?? "the control plane's public IP"}</span> with
          an A record. The certificate follows once it resolves.
        </p>
      )}
    </div>
  );
}
