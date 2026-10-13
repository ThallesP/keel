import { useState } from "react";

import type { Hover } from "../../charts/layout";
import { ResourcePanel } from "../../observability/resource-panel";
import { RangePicker, type TimeRange } from "../../time-range";
import type { RuntimeNode } from "../../types";

export function MetricsTab({ node }: { node: RuntimeNode }) {
  const [range, setRange] = useState<TimeRange>("1h");
  const [hover, setHover] = useState<Hover>(null);
  const shared = { nodeId: node.id, range, hover, onHover: setHover };
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-3 overflow-auto px-5 py-3">
      <div className="flex items-center justify-between">
        <span className="text-2xs text-faint">
          Every replica summed · Ships marked on the time axis
        </span>
        <RangePicker value={range} onChange={setRange} />
      </div>
      <div className="grid grid-cols-3 gap-3">
        <ResourcePanel kind="cpu" {...shared} />
        <ResourcePanel kind="memory" {...shared} />
        <ResourcePanel kind="network" {...shared} />
      </div>
    </div>
  );
}
