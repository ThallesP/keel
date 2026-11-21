import { useSummary } from "./use-data";

export function StatusBar() {
  const summary = useSummary();
  const servers = summary?.servers ?? 0;

  return (
    <footer className="flex h-7 shrink-0 items-center justify-end border-t border-line bg-bg pr-3.5 pl-16 font-mono text-2xs text-faint">
      {summary && (
        <span>
          {servers} {servers === 1 ? "server" : "servers"}
        </span>
      )}
    </footer>
  );
}
