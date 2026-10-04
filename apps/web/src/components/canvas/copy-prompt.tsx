import { api } from "@my-better-t-app/backend/convex/_generated/api";
import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useQuery } from "convex/react";
import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

/**
 * Copies the agent prompt (convex/tracingPrompt.ts): a coding agent pastes it, instruments the
 * repo with OpenTelemetry and checks it locally with `keel run` + `keel traces`. Names the service
 * (`nodeId`) or the project (`environmentId`) when given. `keel tracing prompt` prints the same.
 */
export function CopyPrompt({
  nodeId,
  environmentId,
  className,
}: {
  nodeId?: Id<"nodes">;
  environmentId?: Id<"environments">;
  className?: string;
}) {
  const prompt = useQuery(api.tracing.prompt, { nodeId, environmentId });
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (!prompt) return;
    try {
      await navigator.clipboard.writeText(prompt);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Could not copy; keel tracing prompt prints the same text");
    }
  };
  return (
    <button
      type="button"
      onClick={() => void copy()}
      disabled={!prompt}
      title="For Claude Code, Cursor, Codex…: it instruments the repo and checks the result with keel run"
      className={cn(
        "flex h-6 shrink-0 items-center gap-1.5 rounded-sm border border-line bg-bg px-2 text-2xs text-ink hover:border-primary hover:text-primary disabled:opacity-50",
        className,
      )}
    >
      {copied ? (
        <Check size={11} strokeWidth={2} aria-hidden />
      ) : (
        <Copy size={11} strokeWidth={1.6} aria-hidden />
      )}
      {copied ? "Copied" : "Copy agent prompt"}
    </button>
  );
}
