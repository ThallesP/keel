import { cn } from "@my-better-t-app/ui/lib/utils";
import { Lock } from "lucide-react";
import { useState } from "react";

import { useGetNodeTracing, useSetNodeTracing } from "@/api/gen";
import { succeeded } from "@/lib/panel-write";

import { CopyPrompt } from "../../copy-prompt";
import { Spinner } from "../../primitives";

/**
 * The service's tracing switch, in its Settings tab (docs/logs.md "Traces"). On, Keel adds the
 * OTEL_* variables listed here on the next Ship; the service's own variables win over them.
 * The code side is the agent prompt's job.
 */
export function TracingSection({ nodeId }: { nodeId: string }) {
  const { data } = useGetNodeTracing({ path: { id: nodeId } });
  const setTracing = useSetNodeTracing();
  const [busy, setBusy] = useState(false);
  if (data === undefined) {
    return (
      <div className="flex h-16 items-center justify-center">
        <Spinner />
      </div>
    );
  }
  // null: not a service with a runtime, or not the caller's.
  const { tracing } = data;
  if (!tracing) return null;

  const { enabled } = tracing;
  const blocked = tracing.traces !== "on" && !enabled;
  const note =
    tracing.traces === "off"
      ? "Sign in with Axiom on Observability first: spans need somewhere to go."
      : tracing.traces === "old"
        ? "This Axiom connection predates traces: Sign in with Axiom again on Observability."
        : enabled
          ? "Keel sets these on the next Ship. Your own variables win."
          : "Have your coding agent instrument the repo with the prompt, then turn this on and Ship.";

  const toggle = async () => {
    setBusy(true);
    await succeeded(setTracing.mutateAsync({ path: { id: nodeId }, body: { on: !enabled } }));
    setBusy(false);
  };

  return (
    <section className="shrink-0 border-b border-line">
      <div className="flex items-center gap-6 px-5 py-3">
        <div className="min-w-0 flex-1 pl-2">
          <h3 className="text-sm font-medium text-ink">Tracing</h3>
          <p className="mt-0.5 text-xs text-muted-foreground">{note}</p>
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <CopyPrompt nodeId={nodeId} />
          <button
            type="button"
            role="switch"
            aria-checked={enabled}
            aria-label="OpenTelemetry tracing"
            disabled={busy || blocked}
            onClick={() => void toggle()}
            className={cn(
              "relative h-4 w-7 shrink-0 rounded-full transition-colors disabled:opacity-50",
              enabled ? "bg-primary" : "bg-line",
            )}
          >
            <span
              className={cn(
                "absolute top-0.5 left-0.5 size-3 rounded-full bg-bg shadow-sm transition-transform",
                enabled && "translate-x-3",
              )}
            />
          </button>
        </div>
      </div>
      {enabled &&
        (tracing.env ?? []).map((v) => (
          <div
            key={v.key}
            className="flex h-8 items-center gap-2 border-t border-line/60 px-5 font-mono text-xs"
          >
            <span
              className={cn(
                "flex w-52 shrink-0 items-center gap-1.5 truncate pl-2",
                v.overridden ? "text-faint line-through" : "text-muted-foreground",
              )}
            >
              {v.key}
              {v.secret && <Lock size={10} className="shrink-0 text-faint" aria-label="secret" />}
            </span>
            <span className="min-w-0 flex-1 truncate pl-2 text-faint">{v.value}</span>
            <span className="shrink-0 font-sans text-2xs text-faint">
              {v.overridden ? "replaced by yours" : "set by Keel"}
            </span>
          </div>
        ))}
    </section>
  );
}
