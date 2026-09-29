import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { useReactFlow } from "@xyflow/react";
import type { FunctionReturnType } from "convex/server";
import { useMutation, useQuery } from "convex/react";
import { Braces, Eye, EyeOff, Link2, Lock, LockOpen, Pencil, Plus, Trash2 } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import { attempt } from "../../errors";
import { asNodeId } from "../../mapping";
import { Kbd } from "../../primitives";
import { useCanvasDispatch } from "../../store";
import type { CanvasNode, InfraNode } from "../../types";
import { defaultKey, ReferencePalette, refText, type ReferenceSource } from "../reference-palette";

type Variable = FunctionReturnType<typeof api.variables.list>[number];
type Part = Variable["parts"][number];
type Save = (key: string, value: string, secret: boolean) => Promise<boolean>;

/** Mirrors `ENV_KEY_RE` in `packages/backend/convex/access.ts`. */
const KEY_RE = /^[A-Z_][A-Z0-9_]{0,63}$/;
const MASK = "••••••••••••";

const inputClass =
  "h-7 min-w-0 rounded-sm border border-line bg-bg px-2 font-mono text-xs text-ink outline-none placeholder:font-sans placeholder:text-faint focus:border-primary aria-invalid:border-danger";

/** Selecting a node from the panel: open its Variables tab, then move the canvas selection. */
function useJumpTo() {
  const flow = useReactFlow<CanvasNode>();
  const dispatch = useCanvasDispatch();
  return (nodeId: string) => {
    dispatch({ type: "openTab", nodeId, tab: "variables" });
    flow.setNodes((nodes) => nodes.map((n) => ({ ...n, selected: n.id === nodeId })));
  };
}

/**
 * Key + value inputs with a Reference picker and a secret toggle. The composer row at the top of
 * the tab and the inline row editor are the same component. ↵ saves, Esc cancels / clears.
 * Pasting `KEY=value` into the key splits it.
 */
function Editor({
  initial,
  sources,
  taken,
  onSave,
  onCancel,
  autoFocus = false,
  className,
}: {
  initial?: { key: string; value: string; secret: boolean };
  sources: ReferenceSource[];
  /** Keys already used on this node (minus the one being edited). */
  taken: Set<string>;
  onSave: Save;
  onCancel?: () => void;
  autoFocus?: boolean;
  className?: string;
}) {
  const [key, setKey] = useState(initial?.key ?? "");
  const [value, setValue] = useState(initial?.value ?? "");
  const [secret, setSecret] = useState(initial?.secret ?? false);
  const [picking, setPicking] = useState(false);
  const keyRef = useRef<HTMLInputElement>(null);
  const valueRef = useRef<HTMLInputElement>(null);

  const trimmed = key.trim();
  const clash = taken.has(trimmed);
  const valid = KEY_RE.test(trimmed) && !clash;

  // Inserts at the caret the value input had before the palette took focus.
  const onPick = useCallback((source: ReferenceSource, k: string) => {
    const input = valueRef.current;
    setValue((v) => {
      const at = input?.selectionStart ?? v.length;
      return v.slice(0, at) + refText(source.name, k) + v.slice(input?.selectionEnd ?? at);
    });
    setKey((current) => current || defaultKey(source.name, k));
  }, []);

  const reset = () => {
    setKey("");
    setValue("");
    setSecret(false);
  };

  const submit = async () => {
    if (!valid) return;
    if (!(await onSave(trimmed, value, secret)) || initial) return;
    reset();
    keyRef.current?.focus();
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") void submit();
    if (e.key === "Escape") {
      if (onCancel) onCancel();
      else {
        reset();
        (e.target as HTMLElement).blur();
      }
    }
  };

  const onPaste = (e: React.ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData("text");
    const match = /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$/.exec(text);
    if (!match || text.includes("\n")) return;
    e.preventDefault();
    setKey(match[1]!.toUpperCase());
    setValue(match[2]!.trim().replace(/^(["'])(.*)\1$/, "$2"));
    valueRef.current?.focus();
  };

  return (
    <div className={cn("flex h-11 shrink-0 items-center gap-2 pr-5 pl-5", className)}>
      <input
        ref={keyRef}
        autoFocus={autoFocus}
        value={key}
        onChange={(e) => setKey(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        placeholder={initial ? "KEY" : "NEW_VARIABLE"}
        aria-label="Variable key"
        aria-invalid={clash || (trimmed !== "" && !valid)}
        title={clash ? `${trimmed} already exists` : undefined}
        spellCheck={false}
        className={cn(inputClass, "w-52 shrink-0")}
      />
      <div
        className={cn(
          inputClass,
          "flex flex-1 items-center gap-1 pr-0.5 focus-within:border-primary",
        )}
      >
        <input
          ref={valueRef}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="value, or reference another service"
          aria-label="Variable value"
          spellCheck={false}
          type={secret && !initial ? "password" : "text"}
          className="h-full min-w-0 flex-1 bg-transparent outline-none placeholder:font-sans placeholder:text-faint"
        />
        <button
          type="button"
          onClick={() => setPicking(true)}
          className="flex h-[22px] shrink-0 items-center gap-1 rounded-xs px-1.5 font-sans text-2xs font-medium text-primary hover:bg-primary-soft"
        >
          <Braces size={11} strokeWidth={1.8} aria-hidden />
          Reference
        </button>
      </div>
      <button
        type="button"
        aria-label={secret ? "Secret: value is masked" : "Not secret"}
        aria-pressed={secret}
        title={secret ? "Secret" : "Mark as secret"}
        onClick={() => setSecret((s) => !s)}
        className={cn(
          "flex size-7 shrink-0 items-center justify-center rounded-sm border",
          secret
            ? "border-primary/40 bg-primary-soft text-primary"
            : "border-line text-faint hover:bg-surface-2 hover:text-ink",
        )}
      >
        {secret ? <Lock size={12} aria-hidden /> : <LockOpen size={12} aria-hidden />}
      </button>
      <button
        type="button"
        disabled={!valid}
        onClick={() => void submit()}
        className="flex h-7 shrink-0 items-center gap-1.5 rounded-sm bg-primary px-2.5 text-2xs font-medium text-on-primary hover:bg-primary-strong disabled:bg-surface-2 disabled:text-faint"
      >
        {initial ? "Save" : "Add"}
        <Kbd className="text-current opacity-70">↵</Kbd>
      </button>
      {onCancel && (
        <button
          type="button"
          onClick={onCancel}
          className="h-7 shrink-0 px-1 text-2xs text-faint hover:text-ink"
        >
          Cancel
        </button>
      )}
      <ReferencePalette
        open={picking}
        onOpenChange={setPicking}
        sources={sources}
        onPick={onPick}
        finalFocus={valueRef}
      />
    </div>
  );
}

/** `${{ postgres.DATABASE_URL }}` as a chip; clicking it jumps to that node. */
function RefChip({ part, jump }: { part: Extract<Part, { ref: unknown }>; jump: () => void }) {
  const { node, key, nodeId, missing } = part.ref;
  const label = node ? `${node}.${key}` : key;
  return (
    <button
      type="button"
      onClick={nodeId && node ? jump : undefined}
      title={missing ? `${label} does not exist; resolves to ""` : `From ${node ?? "this node"}`}
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1 rounded-sm px-1.5 align-middle font-mono text-[10px]",
        missing
          ? "bg-danger-soft text-danger line-through decoration-danger/50"
          : "bg-primary-soft text-primary hover:bg-primary/15",
        !(nodeId && node) && "cursor-default",
      )}
    >
      <Link2 size={10} strokeWidth={2} aria-hidden />
      {label}
    </button>
  );
}

function Row({
  variable,
  onEdit,
  onRemove,
}: {
  variable: Variable;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const [revealed, setRevealed] = useState(false);
  const jumpTo = useJumpTo();
  const hasRef = variable.parts.some((p) => "ref" in p);
  const maskable = variable.secret || variable.resolvedSecret;

  return (
    <div
      onDoubleClick={onEdit}
      className="group flex h-9 shrink-0 items-center gap-2 border-b border-line pr-5 pl-5 last:border-0 hover:bg-surface-2/50"
    >
      <span className="flex w-52 shrink-0 items-center gap-1.5 truncate pl-2 font-mono text-xs text-ink">
        {variable.key}
        {variable.secret && <Lock size={10} className="shrink-0 text-faint" aria-label="secret" />}
      </span>
      <span className="flex min-w-0 flex-1 items-center gap-2 pl-2 font-mono text-xs text-muted-foreground">
        {hasRef ? (
          <>
            <span className="flex min-w-0 shrink-0 items-center gap-0.5 whitespace-pre">
              {variable.parts.map((p, i) =>
                "ref" in p ? (
                  <RefChip key={i} part={p} jump={() => p.ref.nodeId && jumpTo(p.ref.nodeId)} />
                ) : (
                  <span key={i}>{variable.secret && !revealed ? "•••" : p.text}</span>
                ),
              )}
            </span>
            <span className="min-w-0 truncate text-faint">
              → {variable.resolvedSecret && !revealed ? MASK : variable.resolved || '""'}
            </span>
          </>
        ) : (
          <span className={cn("truncate", variable.secret && !revealed && "tracking-[0.15em]")}>
            {variable.secret && !revealed ? MASK : variable.value}
          </span>
        )}
      </span>
      <span className="flex shrink-0 items-center gap-0.5">
        {maskable && (
          <button
            type="button"
            aria-label={revealed ? "Hide value" : "Reveal value"}
            onClick={() => setRevealed((v) => !v)}
            className="rounded-sm p-1 text-faint hover:bg-surface-2 hover:text-ink"
          >
            {revealed ? <EyeOff size={12} aria-hidden /> : <Eye size={12} aria-hidden />}
          </button>
        )}
        <span className="flex opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">
          <button
            type="button"
            aria-label={`Edit ${variable.key}`}
            onClick={onEdit}
            className="rounded-sm p-1 text-faint hover:bg-surface-2 hover:text-ink"
          >
            <Pencil size={12} aria-hidden />
          </button>
          <button
            type="button"
            aria-label={`Remove ${variable.key}`}
            onClick={onRemove}
            className="rounded-sm p-1 text-faint hover:bg-danger-soft hover:text-danger"
          >
            <Trash2 size={12} aria-hidden />
          </button>
        </span>
      </span>
    </div>
  );
}

/** One-click references for a service: each other node's connection var it does not use yet. */
function suggestionsFor(node: InfraNode, variables: Variable[], sources: ReferenceSource[]) {
  if (node.type !== "service") return [];
  const used = new Set(
    variables.flatMap((v) =>
      v.parts.flatMap((p) => ("ref" in p && p.ref.nodeId ? [p.ref.nodeId] : [])),
    ),
  );
  const keys = new Set(variables.map((v) => v.key));
  return sources.flatMap((source) => {
    const offered = source.keys.find((k) => k.provided && k.key !== "HOST" && k.key !== "PORT");
    if (!offered || used.has(source.nodeId)) return [];
    const as = defaultKey(source.name, offered.key);
    return keys.has(as) ? [] : [{ source, key: offered.key, as }];
  });
}

export function VariablesTab({ node }: { node: InfraNode }) {
  const id = asNodeId(node.id);
  const variables = useQuery(api.variables.list, { nodeId: id });
  const sources = useQuery(api.variables.referenceable, { nodeId: id }) ?? [];
  const setVariable = useMutation(api.variables.set);
  const removeVariable = useMutation(api.variables.remove);
  const [editing, setEditing] = useState<string | null>(null);

  const rows = variables ?? [];
  const keys = new Set(rows.map((v) => v.key));
  const suggestions = suggestionsFor(node, rows, sources).slice(0, 4);

  const save =
    (previousKey?: string): Save =>
    async (key, value, secret) =>
      (await attempt(setVariable({ nodeId: id, key, value, secret, previousKey }))) !== undefined;

  return (
    <div className="flex min-w-0 flex-1 flex-col overflow-auto">
      <Editor sources={sources} taken={keys} onSave={save()} className="border-b border-line" />
      {suggestions.length > 0 && (
        <div className="flex h-9 shrink-0 items-center gap-1.5 border-b border-line px-5 text-2xs text-faint">
          <span className="pr-1">Connect</span>
          {suggestions.map((s) => (
            <button
              key={s.source.nodeId}
              type="button"
              onClick={() => void save()(s.as, refText(s.source.name, s.key), false)}
              className="flex h-[22px] items-center gap-1 rounded-sm border border-dashed border-line px-1.5 font-mono text-[10px] text-muted-foreground hover:border-primary hover:bg-primary-soft hover:text-primary"
            >
              <Plus size={10} strokeWidth={2} aria-hidden />
              {s.source.name}.{s.key}
            </button>
          ))}
        </div>
      )}
      {variables && rows.length === 0 && (
        <p className="px-5 py-4 text-xs text-faint">
          No variables yet. Type one above, paste <code className="font-mono">KEY=value</code>, or
          use <span className="text-primary">Reference</span> to pull one from another service.
        </p>
      )}
      {rows.map((v) =>
        editing === v.key ? (
          <Editor
            key={v.key}
            autoFocus
            initial={v}
            sources={sources}
            taken={new Set([...keys].filter((k) => k !== v.key))}
            onSave={async (key, value, secret) => {
              const ok = await save(v.key)(key, value, secret);
              if (ok) setEditing(null);
              return ok;
            }}
            onCancel={() => setEditing(null)}
            className="border-b border-line bg-primary-soft/40"
          />
        ) : (
          <Row
            key={v.key}
            variable={v}
            onEdit={() => setEditing(v.key)}
            onRemove={() => void attempt(removeVariable({ nodeId: id, key: v.key }))}
          />
        ),
      )}
    </div>
  );
}
