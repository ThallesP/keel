import { cn } from "@my-better-t-app/ui/lib/utils";
import { Plus, X } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import {
  type EndpointView,
  exposeRequestSchema,
  useExposeNode,
  useGetControlPlane,
} from "@/gen/api";
import { errorMessage } from "@/lib/api";
import { formValues } from "@/lib/form";

import { useCanvasActions } from "../../actions";
import { EndpointAddress } from "../../endpoint-address";
import type { RuntimeNode } from "../../types";

const PROTOCOLS = [
  { id: "http", label: "HTTPS" },
  { id: "tcp", label: "TCP" },
  { id: "udp", label: "UDP" },
] as const;

type Protocol = (typeof PROTOCOLS)[number]["id"];

const firstProtocol: Record<RuntimeNode["type"], Protocol> = {
  service: "http",
  database: "tcp",
  cache: "tcp",
};

const stateLabel: Record<Protocol, Record<EndpointView["state"], string>> = {
  http: { live: "live", starting: "getting a certificate…", failed: "failed" },
  tcp: { live: "live", starting: "starting", failed: "failed" },
  udp: { live: "live", starting: "starting", failed: "failed" },
};

const stateTone: Record<EndpointView["state"], string> = {
  live: "text-success",
  starting: "text-faint",
  failed: "text-danger",
};

const chipClass =
  "inline-flex h-4 w-11 shrink-0 items-center justify-center rounded-sm bg-surface-2 font-sans text-[10px] font-semibold tracking-[0.06em] text-muted-foreground";
const inputClass =
  "h-7 min-w-0 rounded-sm border border-line bg-bg px-2 font-mono text-xs text-ink outline-none placeholder:font-sans placeholder:text-faint focus:border-primary invalid:border-danger [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none";
const buttonClass =
  "flex h-6 shrink-0 items-center gap-1.5 rounded-sm border border-line bg-bg px-2 text-2xs text-ink hover:border-primary hover:text-primary disabled:opacity-50";

export function NetworkingSection({ node }: { node: RuntimeNode }) {
  const { data: ip } = useGetControlPlane({
    query: { staleTime: Infinity, meta: { realtime: false }, select: (c) => c.publicIp },
  });
  const [adding, setAdding] = useState(false);
  const { endpoints } = node.data;
  const where = ip ? `the control plane (${ip})` : "the control plane";
  const openPorts = new Set(
    endpoints.map((e) => (e.protocol === "http" ? "80 and 443" : `${e.publicPort}/${e.protocol}`)),
  );
  const note =
    endpoints.length === 0
      ? "Private: only nodes on this canvas reach it."
      : `Served by ${where}. Its firewall or router must let in ${[...openPorts].join(", ")}.`;

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
      {adding && (
        <AddEndpoint
          node={node}
          target={ip || "the control plane's public IP"}
          onDone={() => setAdding(false)}
        />
      )}
    </section>
  );
}

function EndpointRow({ nodeId, endpoint: e }: { nodeId: string; endpoint: EndpointView }) {
  const actions = useCanvasActions();
  const { label } = PROTOCOLS.find((p) => p.id === e.protocol)!;
  const status = e.state === "failed" && e.error ? e.error : stateLabel[e.protocol][e.state];
  return (
    <div className="flex h-8 items-center gap-3 border-t border-line/60 px-5 font-mono text-xs">
      <span className="flex min-w-0 flex-1 items-center gap-3 pl-2">
        <span className={chipClass}>{label}</span>
        <EndpointAddress endpoint={e} />
        <span className="shrink-0 text-faint">→ :{e.port}</span>
      </span>
      <span
        className={cn("max-w-1/2 truncate font-sans text-2xs", stateTone[e.state])}
        title={e.error}
      >
        {status}
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

function AddEndpoint({
  node,
  target,
  onDone,
}: {
  node: RuntimeNode;
  target: string;
  onDone: () => void;
}) {
  const [protocol, setProtocol] = useState(firstProtocol[node.type]);
  const [domain, setDomain] = useState("");
  const expose = useExposeNode({
    mutation: { onSuccess: onDone, onError: (err) => toast.error(errorMessage(err)) },
  });

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const body = exposeRequestSchema.parse({ protocol, ...formValues(e.currentTarget) });
    expose.mutate({ path: { id: node.id }, body });
  };

  return (
    <form
      className="border-t border-line/60 px-5 py-2"
      onSubmit={submit}
      onKeyDown={(e) => {
        if (e.key === "Escape") onDone();
      }}
    >
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
                "h-5 rounded-[3px] px-2 text-[10px] font-semibold tracking-[0.06em] text-muted-foreground hover:text-ink",
                protocol === p.id && "bg-surface-2 text-ink",
              )}
            >
              {p.label}
            </button>
          ))}
        </div>
        {protocol === "http" ? (
          <input
            autoFocus
            name="domain"
            className={cn(inputClass, "flex-1")}
            placeholder="app.example.com (empty: a generated domain)"
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            aria-label="Domain"
          />
        ) : (
          <input
            autoFocus
            name="publicPort"
            type="number"
            min={1}
            max={65535}
            className={cn(inputClass, "w-36")}
            placeholder="public port (empty: same)"
            aria-label="Public port"
          />
        )}
        <span className="shrink-0 font-mono text-xs text-faint">→ :</span>
        <input
          name="port"
          type="number"
          min={1}
          max={65535}
          defaultValue={node.data.port}
          className={cn(inputClass, "w-20")}
          placeholder="port"
          aria-label="Container port"
        />
        {protocol !== "http" && <span className="flex-1" />}
        <button type="submit" className={buttonClass} disabled={expose.isPending}>
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
          <span className="font-mono text-ink">{target}</span> with an A record. The certificate
          follows once it resolves.
        </p>
      )}
    </form>
  );
}
