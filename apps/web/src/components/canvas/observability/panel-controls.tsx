import { Columns2, RectangleHorizontal, X } from "lucide-react";
import type { ReactNode } from "react";

import { type Panel, useListNodes } from "@/gen/api";

import { useEnvironment } from "../environment";
import { PANEL_KINDS } from "./kinds";

export type PanelControlsProps = {
  panel: Panel;
  onChange: (panel: Panel) => void;
  onRemove: () => void;
  handle: ReactNode;
};

export function PanelControls({ panel, onChange, onRemove, handle }: PanelControlsProps) {
  const { environmentId } = useEnvironment();
  const { data: services = [] } = useListNodes(
    { path: { id: environmentId } },
    { query: { select: (l) => l.nodes.filter((n) => n.type !== "volume" && n.type !== "group") } },
  );
  const kind = PANEL_KINDS[panel.kind];
  const WidthIcon = panel.wide ? Columns2 : RectangleHorizontal;

  return (
    <div className="mb-1.5 flex h-7 items-center gap-2 text-2xs">
      {handle}
      <span className="font-medium text-ink">{kind.label}</span>
      {kind.perService && (
        <select
          aria-label="Service"
          value={panel.nodeId}
          onChange={(e) => onChange({ ...panel, nodeId: e.target.value })}
          className="h-6 rounded-md border border-line bg-bg px-1.5 text-2xs text-ink outline-none focus:border-primary"
        >
          <option value="">Every service</option>
          {services.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      )}
      {panel.kind === "logs" && (
        <input
          key={panel.search}
          aria-label="Only lines containing"
          placeholder="Only lines containing…"
          defaultValue={panel.search}
          maxLength={200}
          spellCheck={false}
          onBlur={(e) =>
            e.target.value !== panel.search && onChange({ ...panel, search: e.target.value })
          }
          onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
          className="h-6 w-48 rounded-md border border-line bg-bg px-1.5 font-mono text-2xs text-ink outline-none placeholder:font-sans placeholder:text-faint focus:border-primary"
        />
      )}
      <span className="flex-1" />
      <button
        type="button"
        aria-label={panel.wide ? "Half width" : "Full width"}
        title={panel.wide ? "Half width" : "Full width"}
        onClick={() => onChange({ ...panel, wide: !panel.wide })}
        className="flex size-6 items-center justify-center rounded-sm text-faint hover:bg-surface-2 hover:text-ink"
      >
        <WidthIcon size={13} strokeWidth={1.6} aria-hidden />
      </button>
      <button
        type="button"
        aria-label="Remove panel"
        title="Remove panel"
        onClick={onRemove}
        className="flex size-6 items-center justify-center rounded-sm text-faint hover:bg-surface-2 hover:text-danger"
      >
        <X size={13} strokeWidth={1.6} aria-hidden />
      </button>
    </div>
  );
}
