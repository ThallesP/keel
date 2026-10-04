import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useAction, useQuery } from "convex/react";
import { Lock } from "lucide-react";
import { useState } from "react";

import { CopyPrompt } from "../../copy-prompt";
import { attempt } from "../../errors";
import { SectionLabel } from "../../primitives";

/**
 * The service's tracing switch, under its variables (docs/logs.md "Traces"). On, Keel adds the
 * OTEL_* variables listed here on the next Ship; the service's own variables win over them.
 * The code side is the agent prompt's job.
 */
export function TracingSection({ nodeId }: { nodeId: Id<"nodes"> }) {
  const tracing = useQuery(api.tracing.forNode, { nodeId });
  const enable = useAction(api.tracing.enable);
  const [busy, setBusy] = useState(false);
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
    await attempt(enable({ nodeId, on: !enabled }));
    setBusy(false);
  };

  return (
    <section className="shrink-0 border-t border-line">
      <div className="flex h-10 items-center gap-3 px-5">
        <SectionLabel className="pl-2">Tracing</SectionLabel>
        <span className="min-w-0 truncate text-2xs text-faint">{note}</span>
        <div className="ml-auto flex shrink-0 items-center gap-3">
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
        tracing.env.map((v) => (
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
