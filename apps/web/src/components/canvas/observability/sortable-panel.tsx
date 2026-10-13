import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { cn } from "@my-better-t-app/ui/lib/utils";
import { GripVertical } from "lucide-react";
import type { ReactNode } from "react";

import type { Panel } from "@/gen/api";

import { PanelControls } from "./panel-controls";

export function SortablePanel({
  panel,
  editing,
  onChange,
  onRemove,
  children,
}: {
  panel: Panel;
  editing: boolean;
  onChange: (panel: Panel) => void;
  onRemove: () => void;
  children: ReactNode;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: panel.id, disabled: !editing });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(
        "min-w-0",
        panel.wide && "col-span-2",
        isDragging && "relative z-10 opacity-80",
      )}
    >
      {editing && (
        <PanelControls
          panel={panel}
          onChange={onChange}
          onRemove={onRemove}
          handle={
            <button
              ref={setActivatorNodeRef}
              type="button"
              aria-label="Move panel"
              className="flex size-6 cursor-grab items-center justify-center rounded-sm text-faint hover:bg-surface-2 hover:text-ink active:cursor-grabbing"
              {...attributes}
              {...listeners}
            >
              <GripVertical size={13} strokeWidth={1.6} aria-hidden />
            </button>
          }
        />
      )}
      {children}
    </div>
  );
}
