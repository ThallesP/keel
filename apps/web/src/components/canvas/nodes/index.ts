import type { NodeTypes } from "@xyflow/react";

import { CacheNode } from "./cache-node";
import { DatabaseNode } from "./database-node";
import { GroupNode } from "./group-node";
import { ServiceNode } from "./service-node";
import { VolumeNode } from "./volume-node";

export const nodeTypes = {
  service: ServiceNode,
  database: DatabaseNode,
  cache: CacheNode,
  volume: VolumeNode,
  group: GroupNode,
} satisfies NodeTypes;
