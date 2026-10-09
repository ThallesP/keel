import { cn } from "@my-better-t-app/ui/lib/utils";
import { useReactFlow } from "@xyflow/react";
import { Braces, Eye, EyeOff, Link2, Lock, LockOpen, Pencil, Plus, Trash2 } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import {
  type ReferenceKey,
  type ReferenceSource,
  useDeleteVariable,
  useListReferenceableVariablesSuspense,
  useListVariablesSuspense,
  useSetVariable,
} from "@/api/gen";
import type { VariableRef, VariableView } from "@/api/types";
import { succeeded } from "@/lib/panel-write";

import { Kbd } from "../../primitives";
import { useCanvasDispatch } from "../../store";
import type { CanvasNode, InfraNode } from "../../types";
import { ReferencePalette, refText } from "../reference-palette";

type Save = (key: string, value: string, secret: boolean) => Promise<boolean>;

/** Mirrors `envKeyRE` in `internal/domain/validate.go`. */
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
  taken: string[];
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
  const clash = taken.includes(trimmed);
  const valid = KEY_RE.test(trimmed) && !clash;

  // Inserts at the caret the value input had before the palette took focus.
  const onPick = useCallback((source: ReferenceSource, k: ReferenceKey) => {
    const input = valueRef.current;
    setValue((v) => {
      const at = input?.selectionStart ?? v.length;
      return v.slice(0, at) + refText(source.name, k.key) + v.slice(input?.selectionEnd ?? at);
    });
    setKey((current) => current || k.as);
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
function RefChip({ reference, jump }: { reference: VariableRef; jump: () => void }) {
  const { node, key, nodeId, missing } = reference;
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
  variable: VariableView;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const [revealed, setRevealed] = useState(false);
  const jumpTo = useJumpTo();
  const { parts } = variable;
  const hasRef = parts.some((p) => p.ref);
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
              {parts.map((p, i) => {
                const { ref } = p;
                return ref ? (
                  <RefChip key={i} reference={ref} jump={() => ref.nodeId && jumpTo(ref.nodeId)} />
                ) : (
                  <span key={i}>{variable.secret && !revealed ? "•••" : p.text}</span>
                );
              })}
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

export function VariablesTab({ node }: { node: InfraNode }) {
  const path = { id: node.id };
  const {
    data: { variables },
  } = useListVariablesSuspense({ path });
  const {
    data: { sources, suggestions },
  } = useListReferenceableVariablesSuspense({ path });
  const setVariable = useSetVariable();
  const deleteVariable = useDeleteVariable();
  const [editing, setEditing] = useState<string | null>(null);

  const keys = variables.map((v) => v.key);

  // The key travels in the body (it is user input); a rename keeps the row's place.
  const save =
    (previousKey?: string): Save =>
    (key, value, secret) =>
      succeeded(setVariable.mutateAsync({ path, body: { key, value, secret, previousKey } }));

  return (
    <div className="flex min-w-0 flex-1 flex-col overflow-auto">
      <Editor sources={sources} taken={keys} onSave={save()} className="border-b border-line" />
      {suggestions.length > 0 && (
        <div className="flex h-9 shrink-0 items-center gap-1.5 border-b border-line px-5 text-2xs text-faint">
          <span className="pr-1">Connect</span>
          {suggestions.slice(0, 4).map((s) => (
            <button
              key={s.nodeId}
              type="button"
              onClick={() => void save()(s.as, s.value, false)}
              className="flex h-[22px] items-center gap-1 rounded-sm border border-dashed border-line px-1.5 font-mono text-[10px] text-muted-foreground hover:border-primary hover:bg-primary-soft hover:text-primary"
            >
              <Plus size={10} strokeWidth={2} aria-hidden />
              {s.node}.{s.key}
            </button>
          ))}
        </div>
      )}
      {variables.length === 0 && (
        <p className="px-5 py-4 text-xs text-faint">
          No variables yet. Type one above, paste <code className="font-mono">KEY=value</code>, or
          use <span className="text-primary">Reference</span> to pull one from another service.
        </p>
      )}
      {variables.map((v) =>
        editing === v.key ? (
          <Editor
            key={v.key}
            autoFocus
            initial={v}
            sources={sources}
            taken={keys.filter((k) => k !== v.key)}
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
            onRemove={() =>
              void succeeded(deleteVariable.mutateAsync({ path, body: { key: v.key } }))
            }
          />
        ),
      )}
    </div>
  );
}
