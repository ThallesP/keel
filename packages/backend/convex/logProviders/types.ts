// Shared shape of what the Logs tab renders, whatever backs it. Every provider (Docker on the
// manager, Axiom, later ClickHouse) returns this; the tab never knows which one answered.

export type LogLine = {
  time: number;
  text: string;
  stream: "stdout" | "stderr";
  /** Swarm task that wrote the line; "" when unknown. */
  task: string;
};

export type Replica = {
  task: string;
  /** Swarm slot: stable replica number across restarts of the same replica. */
  slot: number;
  state: string;
};

export type LogSource = "docker" | "axiom";

export type Tail = { source: LogSource; lines: LogLine[]; replicas: Replica[] };
