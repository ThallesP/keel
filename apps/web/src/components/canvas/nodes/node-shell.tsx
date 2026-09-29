import { cn } from "@my-better-t-app/ui/lib/utils";
import { useRef, useState } from "react";

import { useCanvasActions } from "../actions";
import { useRenaming } from "../store";
import type { InfraNodeType, NodeStatus } from "../types";
import { IconTile } from "./icons";
import { NodeToolbar } from "./node-toolbar";

type Props = {
  id: string;
  type: InfraNodeType;
  name: string;
  /** Under the name, muted: domain, image, engine. Omit for a one-line header. */
  subtitle?: React.ReactNode;
  status: NodeStatus;
  selected: boolean;
  /** Status line(s). */
  children: React.ReactNode;
  /** Optional strip below a divider, e.g. an attached volume. */
  footer?: React.ReactNode;
};

/**
 * The one shell every infra node uses. Railway-style: icon + name (+ subtitle) header, a status
 * line in the body, optional footer strip. The icon carries the type; there is no kind label
 * and no corner status dot — the status line's dots do that job.
 */
export function NodeShell({ id, type, name, subtitle, status, selected, children, footer }: Props) {
  const { renamingId, setRenamingId } = useRenaming();
  return (
    <div
      className={cn(
        "relative flex w-[220px] flex-col rounded-[10px] border bg-bg",
        "shadow-[0_1px_2px_rgba(11,18,32,0.05),0_4px_12px_rgba(11,18,32,0.04)]",
        status === "error" ? "border-danger" : "border-line",
        status === "pending" && "border-dashed",
        selected &&
          "border-primary shadow-[0_0_0_0.5px_var(--color-primary),0_0_0_3px_rgba(31,75,255,0.14),0_4px_14px_rgba(11,18,32,0.06)]",
      )}
    >
      <NodeToolbar nodeId={id} visible={selected} />
      <div className="flex items-start gap-2.5 px-3.5 pt-3">
        <IconTile type={type} />
        <div className="flex min-w-0 flex-1 flex-col gap-px pt-[3px]">
          {renamingId === id ? (
            <NameEditor id={id} name={name} />
          ) : (
            <span
              className="truncate text-sm leading-4 font-semibold text-ink"
              onDoubleClick={() => setRenamingId(id)}
            >
              {name}
            </span>
          )}
          {subtitle && <span className="truncate text-2xs text-muted-foreground">{subtitle}</span>}
        </div>
      </div>
      <div className="flex flex-col gap-1.5 px-3.5 pt-3.5 pb-3">{children}</div>
      {footer && (
        <div className="flex items-center gap-2 border-t border-line px-3.5 py-2 font-mono text-2xs text-muted-foreground">
          {footer}
        </div>
      )}
    </div>
  );
}

/** Mirrors `NAME_RE` in `packages/backend/convex/access.ts`. */
const NAME_RE = /^[a-z0-9-]{1,40}$/;

/**
 * Inline name editor in the card header. Enter commits, Esc cancels, blur commits when valid.
 * Typed text is lowercased so what you see is what the server accepts.
 */
function NameEditor({ id, name }: { id: string; name: string }) {
  const actions = useCanvasActions();
  const { setRenamingId } = useRenaming();
  const [value, setValue] = useState(name);
  const valid = NAME_RE.test(value);
  // Enter unmounts the input, which fires blur; commit once.
  const closed = useRef(false);

  const close = (commit: boolean) => {
    if (closed.current) return;
    closed.current = true;
    if (commit && valid && value !== name) void actions.rename(id, value);
    setRenamingId(null);
  };

  return (
    <input
      autoFocus
      value={value}
      maxLength={40}
      spellCheck={false}
      aria-label="Node name"
      aria-invalid={!valid}
      onFocus={(e) => e.currentTarget.select()}
      onChange={(e) => setValue(e.target.value.toLowerCase())}
      onBlur={() => close(valid)}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === "Enter" && valid) close(true);
        else if (e.key === "Escape") close(false);
      }}
      className="nodrag nopan h-4 w-full bg-transparent text-sm leading-4 font-semibold text-ink outline-none aria-invalid:text-danger"
    />
  );
}
