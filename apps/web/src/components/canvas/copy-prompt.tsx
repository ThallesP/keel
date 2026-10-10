import { cn } from "@my-better-t-app/ui/lib/utils";
import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { useGetTracingPrompt } from "@/api/gen";

/**
 * Copies the agent prompt (`GET /api/tracing/prompt`): a coding agent pastes it, instruments the
 * repo with OpenTelemetry and checks it locally with `keel run` + `keel traces`. Names the service
 * (`nodeId`) or the project (`environmentId`) when given. `keel tracing prompt` prints the same.
 */
export function CopyPrompt({
  nodeId,
  environmentId,
  className,
}: {
  nodeId?: string;
  environmentId?: string;
  className?: string;
}) {
  // Static text: fetched on mount, never invalidated by writes.
  const { data: prompt } = useGetTracingPrompt(
    { query: { nodeId, environmentId } },
    { query: { meta: { realtime: false }, select: (p) => p.prompt } },
  );
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
