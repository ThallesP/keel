import { useEffect } from "react";

type Hotkey = {
  key: string;
  /** Require ⌘ (mac) or Ctrl. */
  mod?: boolean;
};

function isTyping(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName);
}

/** Window-level shortcut. Plain keys are ignored while typing in a field; ⌘/Ctrl combos are not. */
export function useHotkey({ key, mod = false }: Hotkey, handler: () => void) {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== key.toLowerCase()) return;
      if (mod !== (e.metaKey || e.ctrlKey)) return;
      if (!mod && isTyping(e.target)) return;
      e.preventDefault();
      handler();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [key, mod, handler]);
}
