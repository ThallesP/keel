import { cn } from "@my-better-t-app/ui/lib/utils";
import { Check, Copy, ExternalLink } from "lucide-react";
import { useState } from "react";

import type { EndpointView } from "@/api/gen";

/**
 * Where an endpoint answers, with a copy button: an https link once it serves (open in a new
 * tab), else the plain `host:port` a database client or a game takes.
 */
export function EndpointAddress({
  endpoint,
  className,
}: {
  endpoint: EndpointView;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  const { address } = endpoint;
  const copy = () => {
    void navigator.clipboard.writeText(address).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };
  const text = address.replace(/^https:\/\//, "");
  return (
    <span className={cn("flex min-w-0 items-center gap-1", className)}>
      {endpoint.protocol === "http" && endpoint.state === "live" ? (
        <a
          href={address}
          target="_blank"
          rel="noreferrer"
          className="flex min-w-0 items-center gap-1 text-primary hover:underline"
        >
          <span className="truncate">{text}</span>
          <ExternalLink size={10} strokeWidth={1.6} className="shrink-0" aria-hidden />
        </a>
      ) : (
        <span className="truncate text-ink">
          {text}
          {endpoint.protocol !== "http" && <span className="text-faint">/{endpoint.protocol}</span>}
        </span>
      )}
      <button
        type="button"
        onClick={copy}
        aria-label={`Copy ${text}`}
        className="flex size-4 shrink-0 items-center justify-center rounded-sm text-faint hover:bg-surface-2 hover:text-ink"
      >
        {copied ? (
          <Check size={10} strokeWidth={2} aria-hidden />
        ) : (
          <Copy size={10} strokeWidth={1.6} aria-hidden />
        )}
      </button>
    </span>
  );
}
