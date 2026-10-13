import type { ReactNode } from "react";

import { HEIGHT } from "../charts/layout";

export function PanelMessage({ title, children }: { title: string; children: ReactNode }) {
  return (
    <figure className="flex min-w-0 flex-col rounded-lg border border-line px-4 pt-3 pb-3">
      <figcaption className="text-xs font-medium text-ink">{title}</figcaption>
      <div
        className="mt-2 flex items-center justify-center px-6 text-center text-2xs text-faint"
        style={{ height: HEIGHT }}
      >
        {children}
      </div>
    </figure>
  );
}
