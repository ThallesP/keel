import { cn } from "@my-better-t-app/ui/lib/utils";
import { AlignLeft, Braces, ChartLine, Settings, Workflow, type LucideIcon } from "lucide-react";
import { useCallback, useState } from "react";

import { notWired } from "./not-wired";
import { SettingsDialog } from "./settings-dialog";
import { useHotkey } from "./use-hotkey";

type Item = { label: string; icon: LucideIcon; active?: boolean; onClick?: () => void };

const top: Item[] = [
  { label: "Canvas", icon: Workflow, active: true },
  { label: "Logs", icon: AlignLeft },
  { label: "Metrics", icon: ChartLine },
  { label: "Variables", icon: Braces },
];
function RailButton({ label, icon: Icon, active, onClick }: Item) {
  return (
    <button
      type="button"
      aria-label={label}
      aria-current={active ? "page" : undefined}
      title={label}
      onClick={() => !active && (onClick ?? (() => notWired(label)))()}
      className={cn(
        "flex size-8 items-center justify-center rounded-md",
        active
          ? "bg-primary-soft text-primary"
          : "text-muted-foreground hover:bg-surface-2 hover:text-ink",
      )}
    >
      <Icon size={16} strokeWidth={1.5} aria-hidden />
    </button>
  );
}

export function Rail() {
  const [settingsOpen, setSettingsOpen] = useState(false);
  const openSettings = useCallback(() => setSettingsOpen(true), []);
  useHotkey({ key: ",", mod: true }, openSettings);
  return (
    <nav className="flex w-[52px] shrink-0 flex-col items-center justify-between border-r border-line bg-bg py-3">
      <div className="flex flex-col gap-1.5">
        {top.map((item) => (
          <RailButton key={item.label} {...item} />
        ))}
      </div>
      <div className="flex flex-col gap-1.5">
        <RailButton label="Settings" icon={Settings} onClick={openSettings} />
      </div>
      <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} />
    </nav>
  );
}
