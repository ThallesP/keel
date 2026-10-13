import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@my-better-t-app/ui/components/dropdown-menu";
import { Plus } from "lucide-react";

import type { Panel } from "@/gen/api";

import { PANEL_KINDS } from "./kinds";

export function AddPanel({ onAdd }: { onAdd: (panel: Panel) => void }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="flex min-h-40 items-center justify-center gap-1.5 rounded-lg border border-dashed border-line text-xs text-muted-foreground outline-none hover:border-primary hover:text-primary focus-visible:border-primary">
        <Plus size={13} strokeWidth={1.6} aria-hidden />
        Add panel
      </DropdownMenuTrigger>
      <DropdownMenuContent align="center" className="min-w-40">
        {Object.values(PANEL_KINDS).map((k) => (
          <DropdownMenuItem
            key={k.kind}
            onClick={() =>
              onAdd({
                id: Math.random().toString(36).slice(2, 10),
                kind: k.kind,
                nodeId: "",
                search: "",
                wide: k.wide,
              })
            }
          >
            {k.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
