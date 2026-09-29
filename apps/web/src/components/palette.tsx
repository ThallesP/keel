import { Dialog, DialogContent, DialogTitle } from "@my-better-t-app/ui/components/dialog";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { ChevronRight, CornerDownLeft } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";

/**
 * Keyboard-first command palette. One input, one list, pages stacked like a menu.
 *
 * Keys: ↑↓ move · ↵ select · ⌫ on empty input goes back · Esc closes · typing filters.
 * A page with `onSubmit` also accepts free text: whatever is typed becomes the first row
 * ("Deploy nginx:alpine"), so the fastest path is always type → ↵.
 *
 * Meant to be reused for every "pick one" flow (add node, switch project / environment,
 * jump to service). Build a `PalettePage` and hand it to `<Palette root>`.
 */

export type PaletteItem = {
  id: string;
  label: string;
  hint?: string;
  icon?: ReactNode;
  /** Extra words the filter matches on. */
  keywords?: string[];
  /** Rendered muted; selecting still calls `onSelect` (use it for "coming soon" toasts). */
  disabled?: boolean;
  /** Return a page to drill into it; return nothing to finish and close. */
  onSelect: () => PalettePage | void;
};

export type PalettePage = {
  title: string;
  placeholder?: string;
  items: PaletteItem[];
  /** Free-text handler. Called with the trimmed query when the synthetic row is selected. */
  onSubmit?: (text: string) => PalettePage | void;
  /** Label for the synthetic free-text row, e.g. (q) => `Deploy ${q}`. Defaults to the text. */
  submitLabel?: (text: string) => string;
  submitHint?: string;
  submitIcon?: ReactNode;
  /** Mask the input (tokens, passwords). Filtering still works, the text just is not shown. */
  secret?: boolean;
};

const SUBMIT_ID = "__submit__";

function matches(item: PaletteItem, needle: string) {
  if (!needle) return true;
  const hay = [item.label, item.hint ?? "", ...(item.keywords ?? [])].join(" ").toLowerCase();
  return needle.split(/\s+/).every((word) => hay.includes(word));
}

export function Palette({
  open,
  onOpenChange,
  root,
  finalFocus,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  root: PalettePage;
  /** Where focus lands on close. Defaults to the element that opened it. */
  finalFocus?: RefObject<HTMLElement | null>;
}) {
  const [stack, setStack] = useState<PalettePage[]>([root]);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Every open starts fresh at the root, never mid-flow.
  useEffect(() => {
    if (!open) return;
    setStack([root]);
    setQuery("");
    setActive(0);
  }, [open, root]);

  const page = stack[stack.length - 1] ?? root;
  const needle = query.trim().toLowerCase();

  const rows = useMemo<PaletteItem[]>(() => {
    const filtered = page.items.filter((item) => matches(item, needle));
    if (!page.onSubmit || !query.trim()) return filtered;
    const text = query.trim();
    const submit: PaletteItem = {
      id: SUBMIT_ID,
      label: page.submitLabel ? page.submitLabel(text) : text,
      hint: page.submitHint,
      icon: page.submitIcon,
      onSelect: () => page.onSubmit?.(text),
    };
    return [submit, ...filtered];
  }, [page, needle, query]);

  useEffect(() => setActive(0), [rows.length, page]);

  useEffect(() => {
    const el = listRef.current?.children[active];
    if (el instanceof HTMLElement) el.scrollIntoView({ block: "nearest" });
  }, [active]);

  const close = () => onOpenChange(false);
  const select = (item: PaletteItem) => {
    const next = item.onSelect();
    if (next) {
      setStack((s) => [...s, next]);
      setQuery("");
      setActive(0);
    } else {
      close();
    }
  };
  const back = () => {
    if (stack.length <= 1) return close();
    setStack((s) => s.slice(0, -1));
    setQuery("");
    setActive(0);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setActive((i) => (rows.length ? (i + 1) % rows.length : 0));
        break;
      case "ArrowUp":
        e.preventDefault();
        setActive((i) => (rows.length ? (i - 1 + rows.length) % rows.length : 0));
        break;
      case "Enter": {
        e.preventDefault();
        const item = rows[active];
        if (item) select(item);
        break;
      }
      case "Backspace":
        if (query === "" && stack.length > 1) {
          e.preventDefault();
          back();
        }
        break;
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        initialFocus={inputRef}
        finalFocus={finalFocus}
        className="top-[22%] w-[440px] max-w-[calc(100%-2rem)] translate-y-0 gap-0 overflow-hidden rounded-[12px] border border-line bg-bg p-0 text-ink shadow-[0_1px_2px_rgba(11,18,32,0.06),0_16px_48px_rgba(11,18,32,0.14)] ring-0"
        onKeyDown={onKeyDown}
      >
        <DialogTitle className="sr-only">{page.title}</DialogTitle>
        <div className="flex h-11 items-center gap-1.5 border-b border-line px-3.5">
          {stack.map((p, i) => (
            <span key={i} className="flex shrink-0 items-center gap-1.5">
              {i > 0 && <ChevronRight size={11} strokeWidth={1.8} className="text-faint" />}
              <button
                type="button"
                tabIndex={-1}
                onClick={() => setStack((s) => s.slice(0, i + 1))}
                className={cn(
                  "text-2xs font-semibold tracking-[0.06em] uppercase",
                  i === stack.length - 1 ? "text-ink" : "text-faint hover:text-ink",
                )}
              >
                {p.title}
              </button>
            </span>
          ))}
          <input
            ref={inputRef}
            type={page.secret ? "password" : "text"}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={page.placeholder ?? "Type to filter…"}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            autoComplete="off"
            aria-activedescendant={rows[active] ? `palette-${rows[active].id}` : undefined}
            className="ml-2 min-w-0 flex-1 bg-transparent font-mono text-xs text-ink outline-none placeholder:font-sans placeholder:text-sm placeholder:text-faint"
          />
        </div>
        <div
          ref={listRef}
          role="listbox"
          className="flex max-h-[360px] flex-col gap-0.5 overflow-y-auto p-1.5"
        >
          {rows.length === 0 && (
            <div className="px-3 py-6 text-center text-sm text-faint">
              {page.onSubmit && page.items.length === 0 ? "Type, then ↵" : "No matches"}
            </div>
          )}
          {rows.map((item, i) => {
            const isActive = i === active;
            return (
              <button
                key={item.id}
                id={`palette-${item.id}`}
                type="button"
                role="option"
                aria-selected={isActive}
                tabIndex={-1}
                onMouseMove={() => setActive(i)}
                onClick={() => select(item)}
                className={cn(
                  "flex h-11 w-full items-center gap-3 rounded-lg px-2.5 text-left outline-none",
                  isActive && "bg-primary-soft",
                  item.disabled && "opacity-55",
                )}
              >
                <span className="flex size-[26px] shrink-0 items-center justify-center rounded-[7px] bg-surface-2 text-ink">
                  {item.icon}
                </span>
                <span className="flex min-w-0 flex-1 flex-col gap-px">
                  <span className="truncate text-sm leading-4 font-medium text-ink">
                    {item.label}
                  </span>
                  {item.hint && <span className="truncate text-2xs text-faint">{item.hint}</span>}
                </span>
                {isActive && (
                  <CornerDownLeft
                    size={12}
                    strokeWidth={1.8}
                    className="text-primary"
                    aria-hidden
                  />
                )}
              </button>
            );
          })}
        </div>
        <div className="flex h-8 items-center gap-3 border-t border-line px-3.5 font-mono text-2xs text-faint">
          <span>↑↓ move</span>
          <span>↵ select</span>
          {stack.length > 1 && <span>⌫ back</span>}
          <span className="ml-auto">esc</span>
        </div>
      </DialogContent>
    </Dialog>
  );
}
