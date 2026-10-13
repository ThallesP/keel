import type { Panel } from "@/gen/api";

import type { Hover } from "../charts/layout";
import type { TimeRange } from "../time-range";

export type PanelViewProps = {
  panel: Panel;
  range: TimeRange;
  hover: Hover;
  onHover: (i: Hover) => void;
};
