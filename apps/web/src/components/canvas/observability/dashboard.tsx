import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  arrayMove,
  rectSortingStrategy,
  SortableContext,
  sortableKeyboardCoordinates,
} from "@dnd-kit/sortable";
import { type ReactNode, useState } from "react";

import type { Panel } from "@/gen/api";

import type { Hover } from "../charts/layout";
import type { TimeRange } from "../time-range";
import { AddPanel } from "./add-panel";
import { LogsPanel } from "./logs-panel";
import { RequestsPanel } from "./requests-panel";
import { ResourcePanel } from "./resource-panel";
import { ServersPanel } from "./servers-panel";
import { SortablePanel } from "./sortable-panel";
import type { PanelViewProps } from "./types";
import { useDashboard } from "./use-dashboard";

const views: Record<Panel["kind"], (props: PanelViewProps) => ReactNode> = {
  cpu: (p) => <ResourcePanel kind="cpu" nodeId={p.panel.nodeId} {...p} />,
  memory: (p) => <ResourcePanel kind="memory" nodeId={p.panel.nodeId} {...p} />,
  network: (p) => <ResourcePanel kind="network" nodeId={p.panel.nodeId} {...p} />,
  requests: (p) => <RequestsPanel kind="requests" {...p} />,
  latency: (p) => <RequestsPanel kind="latency" {...p} />,
  logs: (p) => <LogsPanel {...p} />,
  servers: (p) => <ServersPanel {...p} />,
};

export function Dashboard({ range, editing }: { range: TimeRange; editing: boolean }) {
  const { panels, save } = useDashboard();
  const [hover, setHover] = useState<Hover>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const handleDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id) return;
    const from = panels.findIndex((p) => p.id === active.id);
    const to = panels.findIndex((p) => p.id === over.id);
    void save(arrayMove(panels, from, to));
  };

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
      <SortableContext items={panels} strategy={rectSortingStrategy}>
        <div className="grid grid-cols-2 gap-3 p-5">
          {panels.map((panel) => (
            <SortablePanel
              key={panel.id}
              panel={panel}
              editing={editing}
              onChange={(next) => void save(panels.map((p) => (p.id === next.id ? next : p)))}
              onRemove={() => void save(panels.filter((p) => p.id !== panel.id))}
            >
              {views[panel.kind]({ panel, range, hover, onHover: setHover })}
            </SortablePanel>
          ))}
          {editing && <AddPanel onAdd={(panel) => void save([...panels, panel])} />}
        </div>
      </SortableContext>
    </DndContext>
  );
}
