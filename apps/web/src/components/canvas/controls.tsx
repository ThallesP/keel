import { cn } from "@my-better-t-app/ui/lib/utils";
import { Panel, useReactFlow, useStore } from "@xyflow/react";
import { Maximize, Minus, Plus } from "lucide-react";

import { floatingSurface } from "./primitives";

const iconButton = "flex items-center justify-center text-ink hover:bg-surface-2";

export function Controls() {
  const flow = useReactFlow();
  const zoom = useStore((s) => s.transform[2]);

  return (
    <Panel position="bottom-left" className="m-4! flex items-center gap-1.5">
      <div className={cn(floatingSurface, "h-8 overflow-hidden")}>
        <button
          type="button"
          aria-label="Zoom out"
          className={cn(iconButton, "size-[30px]")}
          onClick={() => void flow.zoomOut({ duration: 150 })}
        >
          <Minus size={11} strokeWidth={1.6} aria-hidden />
        </button>
        <button
          type="button"
          className="w-10 text-center font-mono text-2xs text-ink hover:bg-surface-2"
          onClick={() => void flow.zoomTo(1, { duration: 150 })}
          title="Reset zoom"
        >
          {Math.round(zoom * 100)}%
        </button>
        <button
          type="button"
          aria-label="Zoom in"
          className={cn(iconButton, "size-[30px]")}
          onClick={() => void flow.zoomIn({ duration: 150 })}
        >
          <Plus size={11} strokeWidth={1.6} aria-hidden />
        </button>
      </div>
      <button
        type="button"
        aria-label="Fit view"
        className={cn(floatingSurface, iconButton, "size-8 justify-center")}
        onClick={() => void flow.fitView({ padding: 0.2, duration: 200 })}
      >
        <Maximize size={13} strokeWidth={1.5} aria-hidden />
      </button>
    </Panel>
  );
}
