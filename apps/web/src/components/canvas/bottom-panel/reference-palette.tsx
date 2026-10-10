import { Braces, Lock } from "lucide-react";
import { useMemo, type RefObject } from "react";

import type { ReferenceKey, ReferenceSource } from "@/gen/api";
import { Palette, type PalettePage } from "@/components/palette";

import { NodeTypeIcon } from "../nodes/icons";

/** `${{ postgres.DATABASE_URL }}`. Mirrors `canvasRefRE` in `internal/domain/reference.go`. */
export const refText = (node: string, key: string) => `\${{ ${node}.${key} }}`;

function pages(
  sources: ReferenceSource[],
  pick: (source: ReferenceSource, key: ReferenceKey) => void,
): PalettePage {
  const keysOf = (source: ReferenceSource): PalettePage => ({
    title: source.name,
    placeholder: "Which variable?",
    items: source.keys.map((k) => ({
      id: k.key,
      label: k.key,
      hint: [k.provided ? "generated" : "variable", k.secret && "secret"]
        .filter(Boolean)
        .join(" · "),
      icon: k.secret ? (
        <Lock size={13} strokeWidth={1.5} aria-hidden />
      ) : (
        <Braces size={13} strokeWidth={1.5} aria-hidden />
      ),
      onSelect: () => pick(source, k),
    })),
  });

  return {
    title: "Reference",
    placeholder: "Which service?",
    items:
      sources.length === 0
        ? [
            {
              id: "none",
              label: "Nothing to reference yet",
              hint: "Add a database or another service first",
              icon: <Braces size={13} strokeWidth={1.5} aria-hidden />,
              disabled: true,
              onSelect: () => undefined,
            },
          ]
        : sources.map((s) => ({
            id: s.nodeId,
            label: s.name,
            hint: s.image ?? s.type,
            // Typing a key (`DATABASE_URL`) narrows to the nodes that have it.
            keywords: s.keys.map((k) => k.key.toLowerCase()),
            icon: <NodeTypeIcon type={s.type} />,
            onSelect: () => keysOf(s),
          })),
  };
}

/**
 * Service → variable picker for `${{ node.KEY }}` references. Built on `Palette`, so it is
 * keyboard-first: type a node or a key, ↵, ↵. `onPick` must be stable (`useCallback`).
 */
export function ReferencePalette({
  open,
  onOpenChange,
  sources,
  onPick,
  finalFocus,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sources: ReferenceSource[];
  onPick: (source: ReferenceSource, key: ReferenceKey) => void;
  finalFocus?: RefObject<HTMLElement | null>;
}) {
  const root = useMemo(() => pages(sources, onPick), [sources, onPick]);
  return <Palette open={open} onOpenChange={onOpenChange} root={root} finalFocus={finalFocus} />;
}
