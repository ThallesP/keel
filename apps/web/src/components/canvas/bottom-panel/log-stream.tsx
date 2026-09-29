import { cn } from "@my-better-t-app/ui/lib/utils";
import { useEffect, useRef } from "react";

export type StreamLine = {
  key: string;
  time?: string;
  /** Fixed-width tag between time and text (replica, stream). */
  tag?: React.ReactNode;
  text: string;
  tone?: "muted" | "ink" | "primary" | "danger" | "warning";
};

const toneClass = {
  muted: "text-muted-foreground",
  ink: "text-ink",
  primary: "text-primary",
  danger: "text-danger",
  warning: "text-warning",
} as const;

type Props = {
  lines: StreamLine[];
  /** Keep scrolled to the bottom as lines arrive. */
  following?: boolean;
  /** Blinking primary block after the last line (while running). */
  cursor?: boolean;
  className?: string;
};

/** Mono 11px / 19px, `white-space: pre`, never wraps. Scrolls both ways. */
export function LogStream({ lines, following = true, cursor = false, className }: Props) {
  const endRef = useRef<HTMLDivElement>(null);
  const count = lines.length;
  useEffect(() => {
    if (following && count > 0) endRef.current?.scrollIntoView({ block: "end" });
  }, [following, count]);

  const last = lines.length - 1;
  return (
    <div
      className={cn(
        "min-h-0 flex-1 overflow-auto font-mono text-2xs leading-[19px] whitespace-pre",
        className,
      )}
    >
      {lines.map((l, i) => (
        <div key={l.key} className={toneClass[l.tone ?? "muted"]}>
          {l.time && <span className="pr-4 text-faint">{l.time}</span>}
          {l.tag}
          {l.text}
          {cursor && i === last && <Cursor />}
        </div>
      ))}
      {cursor && lines.length === 0 && <Cursor />}
      <div ref={endRef} />
    </div>
  );
}

function Cursor() {
  return <span className="ml-1.5 inline-block h-3 w-1.5 animate-pulse bg-primary align-middle" />;
}

export function FollowingBadge({ active = true }: { active?: boolean }) {
  return (
    <span className="flex items-center gap-1.5 text-2xs text-muted-foreground">
      <span className={cn("size-1.5 rounded-full", active ? "bg-primary" : "bg-faint")} />
      {active ? "following" : "paused"}
    </span>
  );
}
