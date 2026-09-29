import { useStore } from "@xyflow/react";

import { useSummary } from "./use-data";

export function StatusBar() {
  const summary = useSummary();
  const servers = summary?.servers ?? 0;
  const zoom = useStore((s) => s.transform[2]);

  return (
    <footer className="flex h-7 shrink-0 items-center justify-end border-t border-line bg-bg pr-3.5 pl-16 text-2xs">
      <div className="flex items-center gap-4 font-mono text-faint">
        {summary && (
          <span>
            {servers} {servers === 1 ? "server" : "servers"}
          </span>
        )}
        <span>{Math.round(zoom * 100)}%</span>
      </div>
    </footer>
  );
}
