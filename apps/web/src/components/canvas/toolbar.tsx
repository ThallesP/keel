import { cn } from "@my-better-t-app/ui/lib/utils";
import { Panel, useReactFlow } from "@xyflow/react";
import { Plus, Search } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import { useCanvasActions } from "./actions";
import { AddDialog, type AddChoice } from "./add-dialog";
import { floatingSurface, Kbd } from "./primitives";
import type { CanvasNode } from "./types";
import { useHotkey } from "./use-hotkey";

function AddButton() {
  const [open, setOpen] = useState(false);
  const flow = useReactFlow<CanvasNode>();
  const actions = useCanvasActions();
  useHotkey(
    { key: "n" },
    useCallback(() => setOpen(true), []),
  );

  // New nodes land a third of the way down the viewport; the server picks the name.
  const add = useCallback(
    ({ type, ...options }: AddChoice) => {
      const el = document.querySelector<HTMLElement>(".react-flow");
      const rect = el?.getBoundingClientRect();
      const center = rect
        ? flow.screenToFlowPosition({
            x: rect.left + rect.width / 2,
            y: rect.top + rect.height / 3,
          })
        : { x: 0, y: 0 };
      void actions.create(
        type,
        { x: Math.round(center.x - 110), y: Math.round(center.y - 40) },
        options,
      );
    },
    [flow, actions],
  );

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={cn(
          floatingSurface,
          "h-8 gap-[7px] pr-3 pl-2.5 text-sm font-medium hover:bg-surface-2",
        )}
      >
        <Plus size={12} strokeWidth={1.8} aria-hidden />
        Add
        <Kbd>N</Kbd>
      </button>
      <AddDialog open={open} onOpenChange={setOpen} onPick={add} />
    </>
  );
}

function SearchField() {
  const ref = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const flow = useReactFlow<CanvasNode>();
  useHotkey(
    { key: "k", mod: true },
    useCallback(() => ref.current?.focus(), []),
  );

  // Typing selects matching nodes; Enter zooms to them. Client-side only.
  const apply = (q: string, focus: boolean) => {
    setQuery(q);
    const needle = q.trim().toLowerCase();
    const matches = flow
      .getNodes()
      .filter((n) => n.type !== "group" && needle && n.data.name.toLowerCase().includes(needle));
    flow.setNodes((nodes) =>
      nodes.map((n) => ({ ...n, selected: matches.some((m) => m.id === n.id) })),
    );
    if (focus && matches.length) void flow.fitView({ nodes: matches, padding: 0.6, duration: 300 });
  };

  return (
    <label className={cn(floatingSurface, "h-8 w-60 gap-2 px-2.5 focus-within:border-primary")}>
      <Search size={13} strokeWidth={1.6} className="text-faint" aria-hidden />
      <input
        ref={ref}
        value={query}
        onChange={(e) => apply(e.target.value, false)}
        onKeyDown={(e) => {
          if (e.key === "Enter") apply(query, true);
          if (e.key === "Escape") ref.current?.blur();
        }}
        placeholder="Search services…"
        className="min-w-0 flex-1 bg-transparent text-sm text-ink outline-none placeholder:text-faint"
      />
      <Kbd>⌘K</Kbd>
    </label>
  );
}

export function Toolbar() {
  return (
    <Panel position="top-left" className="m-4! flex items-center gap-1.5">
      <AddButton />
      <SearchField />
    </Panel>
  );
}
