import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { getRouteApi } from "@tanstack/react-router";
import { useMutation } from "convex/react";
import { Search } from "lucide-react";
import type { ReactNode } from "react";
import { toast } from "sonner";

import { useEnvironment } from "../environment";
import { attempt } from "../errors";

// Pieces every Observability tab shares: the 44px header with the tab switch, the search field,
// Disconnect.

export const route = getRouteApi("/_auth/p/$projectId");

export type Tab = "traces" | "logs";

/** What logSinks.get returns for an Axiom sink; never the token. */
export type Sink = { domain: string; dataset: string; traces: string | null; org: string | null };

const TABS: { id: Tab; label: string }[] = [
  { id: "traces", label: "Traces" },
  { id: "logs", label: "Logs" },
];

/** Tabs left with a mono meta line, the tab's controls right. Same tab style as the bottom panel. */
export function PageHeader({
  tab,
  meta,
  children,
}: {
  tab: Tab;
  meta?: ReactNode;
  children?: ReactNode;
}) {
  const navigate = route.useNavigate();
  return (
    <div className="flex h-11 shrink-0 items-center justify-between gap-4 border-b border-line px-5">
      <span className="flex min-w-0 items-center gap-[22px]">
        <nav className="flex h-11 items-center gap-[18px]" aria-label="Observability">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              aria-current={t.id === tab ? "page" : undefined}
              onClick={() =>
                void navigate({ search: (prev) => ({ ...prev, view: t.id, trace: undefined }) })
              }
              className={cn(
                "flex h-11 items-center border-b-2 text-sm",
                t.id === tab
                  ? "border-ink font-medium text-ink"
                  : "border-transparent text-muted-foreground hover:text-ink",
              )}
            >
              {t.label}
            </button>
          ))}
        </nav>
        {meta && <span className="truncate font-mono text-2xs text-faint">{meta}</span>}
      </span>
      {children && <span className="flex shrink-0 items-center gap-3.5 text-2xs">{children}</span>}
    </div>
  );
}

/** "via Axiom · org · dataset" */
export const viaAxiom = (sink: Sink, dataset: string) =>
  `via Axiom · ${sink.org ? `${sink.org} · ` : ""}${dataset}`;

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
